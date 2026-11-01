//! Byte-preserving frontmatter backfill. Port of Go's
//! `vault.BackfillFrontmatterBytes` / `BackfillFrontmatterInRoot`
//! (`internal/vault/adopt.go`), the writer behind `symdesk vault adopt` and
//! the dataset-handle policy migration.
//!
//! Existing frontmatter keys, unknown fields, comments, key order, line
//! endings and body bytes are preserved. Go iterates the non-standard keys of
//! its `missing` map in randomized order; this port emits them sorted, the
//! only deterministic choice, and the standard keys (`title`, `created`,
//! `tags`) always come first in that order.

use std::{collections::BTreeMap, fs, path::Path};

use noyalib::Value;

use crate::mutations::{MutationError, write_atomic, yaml_value_string};

/// Go: `BackfillFrontmatterBytes`.
///
/// # Errors
/// Returns the Go wording `marshal <key>: ...` when a value cannot be encoded.
pub fn backfill_frontmatter_bytes(
    data: &[u8],
    missing: &BTreeMap<String, Value>,
) -> Result<Vec<u8>, String> {
    if missing.is_empty() {
        return Ok(data.to_vec());
    }
    let line_ending: &[u8] = if data.windows(2).any(|window| window == b"\r\n") {
        b"\r\n"
    } else {
        b"\n"
    };
    let mut lines = split(data, line_ending);

    let mut to_add: Vec<Vec<u8>> = Vec::new();
    let mut push = |key: &str, value: &Value| -> Result<(), String> {
        let rendered =
            yaml_value_string(value).map_err(|error| format!("marshal {key}: {error}"))?;
        to_add.push(format!("{key}: {rendered}").into_bytes());
        Ok(())
    };
    for key in ["title", "created", "tags"] {
        if let Some(value) = missing.get(key) {
            push(key, value)?;
        }
    }
    for (key, value) in missing {
        if matches!(key.as_str(), "title" | "created" | "tags") {
            continue;
        }
        push(key, value)?;
    }
    if to_add.is_empty() {
        return Ok(data.to_vec());
    }

    let (start, end) = bounds(&lines);
    if start != Some(0) || end.is_none() {
        // No frontmatter block at the top of the file: create one.
        let mut new_lines = vec![b"---".to_vec()];
        new_lines.extend(to_add);
        new_lines.push(b"---".to_vec());
        new_lines.extend(lines);
        return Ok(join(&new_lines, line_ending));
    }
    let end = end.unwrap_or_default();
    let tail = lines.split_off(end);
    lines.extend(to_add);
    lines.extend(tail);
    Ok(join(&lines, line_ending))
}

/// Go: `BackfillFrontmatterInRoot` on an already confined path: reads the file,
/// backfills and rewrites it atomically. Unchanged content is not written.
///
/// # Errors
/// Returns the Go wording `read file: ...` or the encode/write failure.
pub fn backfill_frontmatter(path: &Path, missing: &BTreeMap<String, Value>) -> Result<(), String> {
    let data = fs::read(path).map_err(|error| format!("read file: {error}"))?;
    let updated = backfill_frontmatter_bytes(&data, missing)?;
    if updated == data {
        return Ok(());
    }
    write_atomic(path, &updated).map_err(|error: MutationError| error.to_string())
}

fn split(data: &[u8], separator: &[u8]) -> Vec<Vec<u8>> {
    let mut result = Vec::new();
    let mut start = 0;
    while let Some(relative) = data[start..]
        .windows(separator.len())
        .position(|window| window == separator)
    {
        let end = start + relative;
        result.push(data[start..end].to_vec());
        start = end + separator.len();
    }
    result.push(data[start..].to_vec());
    result
}

fn join(lines: &[Vec<u8>], separator: &[u8]) -> Vec<u8> {
    let mut result = Vec::new();
    for (index, line) in lines.iter().enumerate() {
        if index != 0 {
            result.extend_from_slice(separator);
        }
        result.extend_from_slice(line);
    }
    result
}

/// Go `findFrontmatterBounds`: the first two `---` lines (trailing `\r` ignored).
fn bounds(lines: &[Vec<u8>]) -> (Option<usize>, Option<usize>) {
    let mut start = None;
    for (index, line) in lines.iter().enumerate() {
        let end = line
            .iter()
            .rposition(|&byte| byte != b'\r')
            .map_or(0, |position| position + 1);
        if &line[..end] == b"---" {
            if start.is_none() {
                start = Some(index);
            } else {
                return (start, Some(index));
            }
        }
    }
    (start, None)
}
