#![deny(unsafe_code)]

//! SimHash near-duplicate queries over the sidecar, ported from Go's
//! `sidecar.DB.SimilarDocs` / `AllSimhashes` and `service.SimilarAll`.

use std::path::Path;

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
    rel(&vault_root.to_string_lossy(), path).unwrap_or_else(|| path.to_owned())
}

/// Go's `filepath.Clean` for Unix paths.
fn clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let rooted = path.starts_with('/');
    let mut elements: Vec<&str> = Vec::new();
    for element in path.split('/') {
        match element {
            "" | "." => {}
            ".." => {
                if elements.last().is_some_and(|last| *last != "..") {
                    elements.pop();
                } else if !rooted {
                    elements.push("..");
                }
            }
            other => elements.push(other),
        }
    }
    let mut out = String::new();
    if rooted {
        out.push('/');
    }
    out.push_str(&elements.join("/"));
    if !rooted && elements.is_empty() {
        out.push('.');
    }
    out
}

/// Go's `filepath.Rel(base, target)`; `None` is Go's error return.
fn rel(base_path: &str, target_path: &str) -> Option<String> {
    let base_clean = clean(base_path);
    let target_clean = clean(target_path);
    if target_clean == base_clean {
        return Some(".".to_owned());
    }
    let mut base = base_clean.as_str();
    let target = target_clean.as_str();
    if base == "." {
        base = "";
    }
    let base_slashed = base.as_bytes().first() == Some(&b'/');
    let target_slashed = target.as_bytes().first() == Some(&b'/');
    if base_slashed != target_slashed {
        return None;
    }
    let (base_bytes, target_bytes) = (base.as_bytes(), target.as_bytes());
    let (base_len, target_len) = (base_bytes.len(), target_bytes.len());
    let (mut base_start, mut base_end, mut target_start, mut target_end) = (0, 0, 0, 0);
    loop {
        while base_end < base_len && base_bytes[base_end] != b'/' {
            base_end += 1;
        }
        while target_end < target_len && target_bytes[target_end] != b'/' {
            target_end += 1;
        }
        if target[target_start..target_end] != base[base_start..base_end] {
            break;
        }
        if base_end < base_len {
            base_end += 1;
        }
        if target_end < target_len {
            target_end += 1;
        }
        base_start = base_end;
        target_start = target_end;
    }
    if &base[base_start..base_end] == ".." {
        return None;
    }
    if base_start != base_len {
        let separators = base_bytes[base_start..]
            .iter()
            .filter(|b| **b == b'/')
            .count();
        let mut out = String::from("..");
        for _ in 0..separators {
            out.push_str("/..");
        }
        if target_start != target_len {
            out.push('/');
            out.push_str(&target[target_start..]);
        }
        return Some(out);
    }
    Some(target[target_start..].to_owned())
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
    fn relative_paths_follow_filepath_rel_on_unix() {
        let rel_of = |base: &str, target: &str| vault_relative(Path::new(base), target);
        // A rooted base with a relative target falls back to the input.
        assert_eq!(rel_of("/v/root", "rel/b.md"), "rel/b.md");
        // Unix keeps case and treats a backslash as an ordinary byte.
        assert_eq!(rel_of("/v/root", "/V/root/a.md"), "../../V/root/a.md");
        assert_eq!(rel_of("/v/root", r"/v/root/a\b.md"), r"a\b.md");
        assert_eq!(rel_of("/v/root", "/v/root/../x/b.md"), "../x/b.md");
        assert_eq!(rel_of("..", "/v"), "/v");
    }

    #[test]
    fn trim_space_matches_go_for_unicode_white_space() {
        assert_eq!(go_trim_space(" \u{a0}\u{3000}x\n"), "x");
        assert_eq!(go_trim_space("\u{200b}x"), "\u{200b}x");
    }
}
