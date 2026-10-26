#![deny(unsafe_code)]

//! SimHash near-duplicate queries over the sidecar, ported from Go's
//! `sidecar.DB.SimilarDocs` / `AllSimhashes` and `service.SimilarAll`.

use std::path::{Component, Path, PathBuf};

use serde::Serialize;
use symdesk_core::simhash;

use crate::{Sidecar, SidecarError};

/// Go's `service.DefaultDuplicateThreshold`: the one similarity percentage
/// every duplicate scan resolves an unset threshold to.
pub const DEFAULT_DUPLICATE_THRESHOLD: i64 = 85;

/// Go's `service.ResolveDuplicateThreshold`: a non-positive threshold means
/// "unset" and falls back to [`DEFAULT_DUPLICATE_THRESHOLD`].
#[must_use]
pub const fn resolve_duplicate_threshold(threshold: i64) -> i64 {
    if threshold <= 0 {
        DEFAULT_DUPLICATE_THRESHOLD
    } else {
        threshold
    }
}

/// One indexed document compared against a reference simhash (Go `sidecar.SimilarResult`).
///
/// Field order is the Go JSON and `%+v` order.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct SimilarResult {
    pub path: String,
    pub title: String,
    pub similarity: u32,
    pub simhash: String,
}

/// One member of a [`DuplicateGroup`] (Go `service.DuplicateMember`).
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct DuplicateMember {
    pub path: String,
    pub title: String,
    pub similarity: u32,
}

/// One cluster of mutually similar documents (Go `service.DuplicateGroup`).
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct DuplicateGroup {
    pub path: String,
    pub title: String,
    pub members: Vec<DuplicateMember>,
}

struct Node {
    path: String,
    title: String,
    body: String,
    hash: u64,
    visited: bool,
}

impl Sidecar {
    /// Returns indexed documents whose stored simhash is within `threshold`
    /// percent of `reference`, in path order (Go `DB.SimilarDocs`).
    ///
    /// A document whose stored simhash text equals `reference` is skipped,
    /// like Go; rows with an unparsable simhash are ignored.
    ///
    /// # Errors
    /// Returns an invalid-reference error or SQLite failures.
    pub fn similar_docs(
        &self,
        reference: &str,
        threshold: i64,
    ) -> Result<Vec<SimilarResult>, SidecarError> {
        if reference.is_empty() {
            return Ok(Vec::new());
        }
        let reference_hash = simhash::parse_hex(reference).map_err(|error| {
            SidecarError::Contract(format!("invalid reference simhash: {error}"))
        })?;
        let mut statement = self.connection.prepare(
            "SELECT path, title, COALESCE(simhash,'') FROM files WHERE simhash IS NOT NULL AND simhash != '' ORDER BY path ASC",
        )?;
        let rows = statement.query_map([], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
            ))
        })?;
        let mut results = Vec::new();
        for row in rows {
            let (path, title, stored) = row?;
            if stored == reference {
                continue;
            }
            let Ok(other) = simhash::parse_hex(&stored) else {
                continue;
            };
            let similarity = simhash::similarity(reference_hash, other);
            if i64::from(similarity) >= threshold {
                results.push(SimilarResult {
                    path,
                    title,
                    similarity,
                    simhash: stored,
                });
            }
        }
        Ok(results)
    }

    /// Scans every indexed document with a non-blank body and returns clusters
    /// of near-duplicates at or above `threshold` percent (Go `Service.SimilarAll`).
    ///
    /// Hashes are recomputed from the trimmed body, never taken from the stored
    /// simhash. Clusters are connected components grown breadth-first in path
    /// order; members are ordered by similarity to the representative, then path.
    ///
    /// # Errors
    /// Returns SQLite failures.
    pub fn similar_all(
        &self,
        vault_root: &Path,
        threshold: i64,
    ) -> Result<Vec<DuplicateGroup>, SidecarError> {
        let threshold = resolve_duplicate_threshold(threshold);
        let mut statement = self.connection.prepare(
            "SELECT files.path, files.title, fts_search.body FROM files JOIN fts_search ON fts_search.rowid = files.id WHERE trim(COALESCE(fts_search.body,'')) != '' ORDER BY files.path ASC",
        )?;
        let rows = statement.query_map([], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
            ))
        })?;
        let mut nodes = Vec::new();
        for row in rows {
            let (path, title, body) = row?;
            let body = go_trim_space(&body).to_owned();
            if body.is_empty() {
                continue;
            }
            nodes.push(Node {
                hash: simhash::compute(&body),
                path,
                title,
                body,
                visited: false,
            });
        }

        let similar = |left: &Node, right: &Node| {
            i64::from(simhash::similarity_for_content(
                left.hash,
                right.hash,
                &left.body,
                &right.body,
            ))
        };

        let mut groups: Vec<Vec<usize>> = Vec::new();
        for start in 0..nodes.len() {
            if nodes[start].visited {
                continue;
            }
            nodes[start].visited = true;
            let mut queue = std::collections::VecDeque::from([start]);
            let mut members = Vec::new();
            while let Some(current) = queue.pop_front() {
                members.push(current);
                for other in 0..nodes.len() {
                    if nodes[other].visited {
                        continue;
                    }
                    if similar(&nodes[current], &nodes[other]) >= threshold {
                        nodes[other].visited = true;
                        queue.push_back(other);
                    }
                }
            }
            if members.len() > 1 {
                groups.push(members);
            }
        }

        let mut result = Vec::with_capacity(groups.len());
        for members in groups {
            let representative = &nodes[members[0]];
            let mut group = DuplicateGroup {
                path: vault_relative(vault_root, &representative.path),
                title: representative.title.clone(),
                members: Vec::with_capacity(members.len() - 1),
            };
            for &index in &members[1..] {
                let member = &nodes[index];
                group.members.push(DuplicateMember {
                    path: vault_relative(vault_root, &member.path),
                    title: member.title.clone(),
                    similarity: simhash::similarity_for_content(
                        representative.hash,
                        member.hash,
                        &representative.body,
                        &member.body,
                    ),
                });
            }
            // Similarity descending, then path ascending; stable like Go's insertion sort.
            group.members.sort_by(|left, right| {
                right
                    .similarity
                    .cmp(&left.similarity)
                    .then_with(|| left.path.cmp(&right.path))
            });
            result.push(group);
        }
        Ok(result)
    }
}

/// Go's `strings.TrimSpace`: trims Unicode `White_Space` from both ends.
fn go_trim_space(value: &str) -> &str {
    value.trim_matches(char::is_whitespace)
}

/// Go's `vaultRel`: `filepath.Rel` with forward slashes, passing the input
/// through unchanged when no relative path exists.
fn vault_relative(vault_root: &Path, path: &str) -> String {
    match go_rel(vault_root, Path::new(path)) {
        Some(relative) => relative
            .to_string_lossy()
            .replace(std::path::MAIN_SEPARATOR, "/"),
        None => path.to_owned(),
    }
}

/// Lexical `filepath.Rel(base, target)`.
fn go_rel(base: &Path, target: &Path) -> Option<PathBuf> {
    if base.is_absolute() != target.is_absolute() {
        return None;
    }
    let base = clean_components(base);
    let target = clean_components(target);
    let common = base
        .iter()
        .zip(&target)
        .take_while(|(left, right)| left == right)
        .count();
    if base[common..].iter().any(|part| part.as_os_str() == "..") {
        return None;
    }
    let mut output = PathBuf::new();
    for _ in &base[common..] {
        output.push("..");
    }
    for part in &target[common..] {
        output.push(part);
    }
    if output.as_os_str().is_empty() {
        output.push(".");
    }
    Some(output)
}

fn clean_components(path: &Path) -> Vec<Component<'_>> {
    let mut parts: Vec<Component<'_>> = Vec::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => match parts.last() {
                Some(Component::Normal(_)) => {
                    parts.pop();
                }
                Some(Component::RootDir | Component::Prefix(_)) => {}
                _ => parts.push(component),
            },
            other => parts.push(other),
        }
    }
    parts
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn threshold_resolution_matches_go() {
        assert_eq!(resolve_duplicate_threshold(0), 85);
        assert_eq!(resolve_duplicate_threshold(-4), 85);
        assert_eq!(resolve_duplicate_threshold(1), 1);
        assert_eq!(resolve_duplicate_threshold(100), 100);
    }

    #[test]
    fn relative_paths_follow_filepath_rel() {
        let root = Path::new("/v/root");
        assert_eq!(vault_relative(root, "/v/root/a/b.md"), "a/b.md");
        assert_eq!(vault_relative(root, "/v/root"), ".");
        assert_eq!(vault_relative(root, "/v/other/b.md"), "../other/b.md");
        assert_eq!(vault_relative(root, "rel/b.md"), "rel/b.md");
        assert_eq!(vault_relative(root, "/v/root/./a//b.md"), "a/b.md");
    }

    #[test]
    fn trim_space_matches_go_for_unicode_white_space() {
        assert_eq!(go_trim_space(" \u{a0}\u{3000}x\n"), "x");
        assert_eq!(go_trim_space("\u{200b}x"), "\u{200b}x");
    }
}
