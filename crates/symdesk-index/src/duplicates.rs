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
    vault_relative_with(PathFlavor::HOST, &vault_root.to_string_lossy(), path)
}

fn vault_relative_with(flavor: PathFlavor, vault_root: &str, path: &str) -> String {
    match flavor.rel(vault_root, path) {
        // `filepath.ToSlash` only rewrites the separator on Windows.
        Some(relative) if flavor.windows => relative.replace('\\', "/"),
        Some(relative) => relative,
        None => path.to_owned(),
    }
}

/// The path rules `filepath.Rel` follows on one operating system. Go's
/// `Rel` is purely lexical and decides on volume names and a leading
/// separator rather than `IsAbs`, so a rooted path without a drive letter is
/// *not* absolute on Windows yet still comparable with another rooted path.
#[derive(Clone, Copy)]
struct PathFlavor {
    windows: bool,
}

impl PathFlavor {
    const HOST: Self = Self {
        windows: cfg!(windows),
    };

    const fn separator(self) -> u8 {
        if self.windows { b'\\' } else { b'/' }
    }

    const fn is_separator(self, byte: u8) -> bool {
        byte == b'/' || (self.windows && byte == b'\\')
    }

    /// Go's `volumeNameLen` for a drive letter or a `\\host\share` prefix.
    fn volume_len(self, path: &str) -> usize {
        if !self.windows {
            return 0;
        }
        let bytes = path.as_bytes();
        if bytes.len() >= 2 && bytes[1] == b':' && bytes[0].is_ascii_alphabetic() {
            return 2;
        }
        if bytes.len() >= 5
            && self.is_separator(bytes[0])
            && self.is_separator(bytes[1])
            && !self.is_separator(bytes[2])
            && bytes[2] != b'.'
        {
            let mut host_end = 3;
            while host_end < bytes.len() && !self.is_separator(bytes[host_end]) {
                host_end += 1;
            }
            if host_end + 1 < bytes.len() && self.is_separator(bytes[host_end]) {
                let mut share_end = host_end + 1;
                while share_end < bytes.len() && !self.is_separator(bytes[share_end]) {
                    share_end += 1;
                }
                if share_end > host_end + 1 {
                    return share_end;
                }
            }
        }
        0
    }

    /// Go's `sameWord`: byte equality, case-insensitive on Windows.
    fn same_word(self, left: &str, right: &str) -> bool {
        if self.windows {
            left == right || left.to_lowercase() == right.to_lowercase()
        } else {
            left == right
        }
    }

    /// Go's `filepath.Clean`.
    fn clean(self, path: &str) -> String {
        let volume_len = self.volume_len(path);
        let volume = &path[..volume_len];
        let rest = &path[volume_len..];
        let separator = char::from(self.separator());
        if rest.is_empty() {
            if volume_len > 1 && path.as_bytes()[1] != b':' {
                return volume.replace('/', &separator.to_string());
            }
            return format!("{volume}.");
        }
        let rooted = self.is_separator(rest.as_bytes()[0]);
        let mut elements: Vec<&str> = Vec::new();
        for element in rest.split(|c: char| c.is_ascii() && self.is_separator(c as u8)) {
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
        let mut out = String::from(volume);
        if self.windows {
            out = out.replace('/', "\\");
        }
        if rooted {
            out.push(separator);
        }
        out.push_str(&elements.join(&separator.to_string()));
        if !rooted && elements.is_empty() {
            out.push('.');
        }
        out
    }

    /// Go's `filepath.Rel(base, target)`; `None` is Go's error return.
    fn rel(self, base_path: &str, target_path: &str) -> Option<String> {
        let base_clean = self.clean(base_path);
        let target_clean = self.clean(target_path);
        if self.same_word(&target_clean, &base_clean) {
            return Some(".".to_owned());
        }
        let base_volume = &base_clean[..self.volume_len(&base_clean)];
        let target_volume = &target_clean[..self.volume_len(&target_clean)];
        let mut base = &base_clean[base_volume.len()..];
        let target = &target_clean[target_volume.len()..];
        if base == "." {
            base = "";
        } else if base.is_empty() && self.volume_len(base_volume) > 2 {
            // A bare UNC volume stands for its root.
            base = "\\";
        }
        let separator = self.separator();
        let base_slashed = base.as_bytes().first() == Some(&separator);
        let target_slashed = target.as_bytes().first() == Some(&separator);
        if base_slashed != target_slashed || !self.same_word(base_volume, target_volume) {
            return None;
        }
        let (base_bytes, target_bytes) = (base.as_bytes(), target.as_bytes());
        let (base_len, target_len) = (base_bytes.len(), target_bytes.len());
        let (mut base_start, mut base_end, mut target_start, mut target_end) = (0, 0, 0, 0);
        loop {
            while base_end < base_len && base_bytes[base_end] != separator {
                base_end += 1;
            }
            while target_end < target_len && target_bytes[target_end] != separator {
                target_end += 1;
            }
            if !self.same_word(
                &target[target_start..target_end],
                &base[base_start..base_end],
            ) {
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
                .filter(|b| **b == separator)
                .count();
            let sep = char::from(separator);
            let mut out = String::from("..");
            for _ in 0..separators {
                out.push(sep);
                out.push_str("..");
            }
            if target_start != target_len {
                out.push(sep);
                out.push_str(&target[target_start..]);
            }
            return Some(out);
        }
        Some(target[target_start..].to_owned())
    }
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
    fn relative_paths_follow_filepath_rel_per_platform() {
        let unix = PathFlavor { windows: false };
        let windows = PathFlavor { windows: true };
        // A rooted path without a drive letter is relative on Windows but
        // still compares as rooted: Go falls back to the input unchanged.
        assert_eq!(
            vault_relative_with(windows, "/v/root", "rel/b.md"),
            "rel/b.md"
        );
        assert_eq!(vault_relative_with(unix, "/v/root", "rel/b.md"), "rel/b.md");
        assert_eq!(
            vault_relative_with(windows, "/v/root", "/v/root/a/b.md"),
            "a/b.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", r"C:\v\root\a\b.md"),
            "a/b.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", r"c:\V\ROOT\a\b.md"),
            "a/b.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", r"C:\v\other\b.md"),
            "../other/b.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", r"D:\v\root\a.md"),
            r"D:\v\root\a.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", r"C:\v\root"),
            "."
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", "rel/b.md"),
            "rel/b.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"C:\v\root", "C:/v/root/a/b.md"),
            "a/b.md"
        );
        assert_eq!(
            vault_relative_with(windows, r"\\host\share\v", r"\\host\share\v\a.md"),
            "a.md"
        );
        // Unix keeps case and treats a backslash as an ordinary byte.
        assert_eq!(
            vault_relative_with(unix, "/v/root", "/V/root/a.md"),
            "../../V/root/a.md"
        );
        assert_eq!(
            vault_relative_with(unix, "/v/root", r"/v/root/a\b.md"),
            r"a\b.md"
        );
        assert_eq!(
            vault_relative_with(unix, "/v/root", "/v/root/../x/b.md"),
            "../x/b.md"
        );
        assert_eq!(vault_relative_with(unix, "..", "/v"), "/v");
    }

    #[test]
    fn trim_space_matches_go_for_unicode_white_space() {
        assert_eq!(go_trim_space(" \u{a0}\u{3000}x\n"), "x");
        assert_eq!(go_trim_space("\u{200b}x"), "\u{200b}x");
    }
}
