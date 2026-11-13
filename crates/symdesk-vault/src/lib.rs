#![deny(unsafe_code)]

//! Read-only contract-v1–v6 Markdown vault parsing.

pub mod activity_journal;
mod adopt;
mod base_write;
pub mod conflict;
pub mod dataset;
mod go_io;
mod go_string;
mod health_links;
pub mod history;
mod links;
mod markdown_links;
mod metadata;
mod mutations;
mod notebook_read;
mod notebook_write;
pub mod notes;
mod paths;
pub mod permissions;
mod resolver;
pub mod retention;
pub mod retention_state;
pub mod sha256;
mod tag_rewrite;
mod tags;
mod typed;
mod walk;

use std::{collections::BTreeMap, fmt::Write as _, path::Path};

use noyalib::Value;
use thiserror::Error;

#[cfg(test)]
use serde as _;
#[cfg(test)]
use serde_json as _;

pub use adopt::{backfill_frontmatter, backfill_frontmatter_bytes};
pub use base_write::{
    BASES_DIR, BaseWriteError, delete_base, delete_base_with_snapshot, delete_view,
    delete_view_with_snapshot, get_base, list_bases, migrate_legacy_views, save_base,
    save_base_with_snapshot, save_view, save_view_with_snapshot, slugify,
};
pub use conflict::{
    CONFLICT_COPY_SUFFIX, SYNC_CONFLICT_MARKER, derive_original_path, is_sync_conflict_base_name,
};
pub use go_io::{
    go_path_io_error, path_error as go_path_error, read as go_read,
    read_to_string as go_read_to_string,
};
pub use go_string::{lowercase as go_lowercase, quote as go_quote};
pub use health_links::{HealthLinkResolver, LinkInventory, normalize_health_link_target};
pub use history::{
    HistoryEntry, HistoryError, HistoryStore, TRASH_META_SUFFIX, TrashEntry, trash_rel_dir,
};
pub use links::extract_wikilinks;
pub use markdown_links::{MarkdownLinkResolver, extract_markdown_links, markdown_link_target};
pub use metadata::{
    SearchMetadata, SearchMetadataField, format_search_metadata, metadata_matches,
    search_metadata_from_document, strip_search_metadata,
};
pub use mutations::{
    MutationError, delete_frontmatter_value, render_go_yaml_double_quoted, render_go_yaml_map,
    render_go_yaml_meeting_map, render_go_yaml_string, set_frontmatter_key, set_frontmatter_value,
    write_atomic,
};
pub use notebook_read::{NotebookSource, list_notebooks, load_notebook, resolve_notebook_sources};
pub use notebook_write::{
    NotebookWriteError, add_notebook_source, new_notebook, new_notebook_with_query, notebook_slug,
    remove_notebook_source,
};
pub use notes::{NoteError, create_note, move_note, note_document, note_file_name, set_property};
pub use paths::{SecurePathError, secure_path};
pub use resolver::{ResolveDocument, ResolvedEdge, ResolvedNode, Resolver, resolve_graph};
/// Go `encoding/json` syntax check (the first error Go's scanner reports).
pub use retention::json_decoder::validate_syntax as go_json_validate_syntax;
pub use tag_rewrite::{
    BodyRewrite, FrontmatterTagRewrite, InlineTagRewrite, rewrite_document_tags_and_body,
    rewrite_inline_tags,
};
pub use tags::{TagSpan, extract_inline_tags, find_inline_tag_spans};
pub use typed::{
    Base, ComputedColumn, Coverage, DatasetHandle, Filter, FilterGroup, Notebook, PropertyConfig,
    Provenance, Sort, Template, TypedVaultError, View, parse_base, parse_dataset_handle,
    parse_notebook,
};
pub use walk::{
    WalkEntry, WalkEntryType, is_external_symlink, walk_all, walk_markdown, walk_markdown_with,
};

#[derive(Clone, Debug)]
pub struct Document {
    pub path: String,
    pub sha256: String,
    pub title: String,
    pub created: String,
    pub tags: Vec<String>,
    pub aliases: Vec<String>,
    pub frontmatter: BTreeMap<String, Value>,
    pub yaml_timestamps: BTreeMap<String, String>,
    pub body: String,
    pub links: Vec<String>,
    pub size: i64,
    pub document_date: String,
    pub person: String,
    pub status: String,
    pub due_date: String,
    pub confidence: i64,
    pub ocr_json_path: String,
    pub simhash: String,
    pub asn: Option<i64>,
    pub document_type: String,
    pub derived_from: String,
    pub derived: bool,
}

#[derive(Debug, Error)]
pub enum VaultError {
    #[error("invalid frontmatter in {path}: {detail}")]
    Frontmatter { path: String, detail: String },
    #[error("invalid asn in {path}: {reason}")]
    Asn { path: String, reason: &'static str },
}

/// Go's yaml.v3 wording for a frontmatter document that is a sequence or a
/// scalar instead of a mapping (`map[string]interface{}` target); `None` when
/// the document is anything else.
fn go_not_a_mapping(frontmatter: &[u8]) -> Option<String> {
    let kind = match noyalib::from_slice::<Value>(frontmatter).ok()? {
        Value::Sequence(_) => "!!seq".to_owned(),
        Value::String(text) => format!("!!str `{text}`"),
        Value::Bool(flag) => format!("!!bool `{flag}`"),
        Value::Number(number) => {
            let text = number.to_string();
            let tag = if text.contains(['.', 'e', 'E']) {
                "!!float"
            } else {
                "!!int"
            };
            format!("{tag} `{text}`")
        }
        _ => return None,
    };
    // The error carries the line of the node: the first line with content.
    let line = String::from_utf8_lossy(frontmatter)
        .lines()
        .position(|line| !line.trim().is_empty() && !line.trim_start().starts_with('#'))?
        + 1;
    Some(format!(
        "yaml: unmarshal errors:\n  line {line}: cannot unmarshal {kind} into map[string]interface {{}}"
    ))
}

/// Parses exact bytes without touching the filesystem.
///
/// # Errors
///
/// Returns a typed frontmatter or ASN validation error.
pub fn parse_bytes(path: &str, input: &[u8]) -> Result<Document, VaultError> {
    let digest = sha256::digest(input);
    let mut sha256 = String::with_capacity(64);
    for byte in digest {
        let _ = write!(sha256, "{byte:02x}");
    }
    let (frontmatter_bytes, body_bytes, frontmatter_found) = split_frontmatter(input);
    let frontmatter = if frontmatter_found && !frontmatter_bytes.is_empty() {
        let duplicates = duplicate_top_level_keys(&frontmatter_bytes);
        if !duplicates.is_empty() {
            // Go's YAML decoder reports every duplicate top-level key, in
            // document order, with its own unmarshal wording and both line
            // numbers.
            let lines = duplicates
                .iter()
                .map(|(key, first, second)| {
                    format!("line {second}: mapping key {key:?} already defined at line {first}")
                })
                .collect::<Vec<_>>()
                .join("\n  ");
            return Err(VaultError::Frontmatter {
                path: path.to_owned(),
                detail: format!("yaml: unmarshal errors:\n  {lines}"),
            });
        }

        noyalib::from_slice::<BTreeMap<String, Value>>(&frontmatter_bytes).map_err(|error| {
            VaultError::Frontmatter {
                path: path.to_owned(),
                detail: go_not_a_mapping(&frontmatter_bytes).unwrap_or_else(|| error.to_string()),
            }
        })?
    } else {
        BTreeMap::new()
    };
    let yaml_timestamps = top_level_yaml_timestamps(&frontmatter_bytes);

    let title = string_value(&frontmatter, &yaml_timestamps, "title").unwrap_or_else(|| {
        Path::new(path)
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or(path)
            .strip_suffix(".md")
            .unwrap_or_else(|| {
                Path::new(path)
                    .file_name()
                    .and_then(|name| name.to_str())
                    .unwrap_or(path)
            })
            .to_owned()
    });
    let mut tags = string_or_list(&frontmatter, "tags");
    let aliases = string_or_list(&frontmatter, "aliases");
    let asn = parse_asn(path, frontmatter.get("asn"))?;
    let derived_from =
        string_value(&frontmatter, &yaml_timestamps, "derived_from").unwrap_or_default();
    let mut derived = frontmatter
        .get("derived")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    if !derived_from.is_empty() {
        derived = true;
    }
    let body = if is_excalidraw_file(path) {
        String::new()
    } else {
        String::from_utf8_lossy(&body_bytes).into_owned()
    };
    let links = extract_wikilinks(&body);
    for tag in extract_inline_tags(&body) {
        if !tags
            .iter()
            .any(|existing| existing.eq_ignore_ascii_case(&tag))
        {
            tags.push(tag);
        }
    }

    Ok(Document {
        path: path.to_owned(),
        sha256,
        title,
        created: string_value(&frontmatter, &yaml_timestamps, "created").unwrap_or_default(),
        tags,
        aliases,
        document_date: string_value(&frontmatter, &yaml_timestamps, "document_date")
            .unwrap_or_default(),
        person: string_value(&frontmatter, &yaml_timestamps, "person").unwrap_or_default(),
        status: string_value(&frontmatter, &yaml_timestamps, "status").unwrap_or_default(),
        due_date: string_value(&frontmatter, &yaml_timestamps, "due_date").unwrap_or_default(),
        confidence: integer_value(frontmatter.get("confidence")),
        ocr_json_path: string_value(&frontmatter, &yaml_timestamps, "ocr_json_path")
            .unwrap_or_default(),
        simhash: string_value(&frontmatter, &yaml_timestamps, "simhash").unwrap_or_default(),
        asn,
        document_type: infer_type(&frontmatter),
        derived_from,
        derived,
        yaml_timestamps,
        frontmatter,
        body,
        links,
        size: i64::try_from(input.len()).unwrap_or(i64::MAX),
    })
}

#[must_use]
pub fn is_excalidraw_file(path: &str) -> bool {
    path.to_ascii_lowercase().ends_with(".excalidraw.md")
}

/// Returns a lowercase SHA-256 digest for a byte string.
#[must_use]
pub fn sha256_hex(input: &[u8]) -> String {
    sha256::digest(input)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

pub(crate) fn split_frontmatter(input: &[u8]) -> (Vec<u8>, Vec<u8>, bool) {
    let mut frontmatter = Vec::new();
    let mut body = Vec::new();
    let mut in_frontmatter = false;
    let mut found = false;
    let mut start = 0;
    loop {
        let end = input[start..]
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or(input.len(), |offset| start + offset + 1);
        let line = &input[start..end];
        let trimmed = line.strip_suffix(b"\n").unwrap_or(line);
        let trimmed = trimmed.strip_suffix(b"\r").unwrap_or(trimmed);
        if trimmed == b"---" {
            if !in_frontmatter && !found && frontmatter.is_empty() {
                in_frontmatter = true;
            } else if in_frontmatter {
                in_frontmatter = false;
                found = true;
            } else {
                body.extend_from_slice(line);
            }
        } else if in_frontmatter {
            frontmatter.extend_from_slice(line);
        } else {
            body.extend_from_slice(line);
        }
        if end == input.len() {
            break;
        }
        start = end;
    }
    (frontmatter, body, found)
}

fn string_value(
    values: &BTreeMap<String, Value>,
    timestamps: &BTreeMap<String, String>,
    key: &str,
) -> Option<String> {
    if timestamps.contains_key(key) {
        return None;
    }
    values.get(key)?.as_str().map(str::to_owned)
}

fn top_level_yaml_timestamps(input: &[u8]) -> BTreeMap<String, String> {
    let text = String::from_utf8_lossy(input);
    let mut result = BTreeMap::new();
    for line in text.lines() {
        if line.starts_with([' ', '\t']) {
            continue;
        }
        let Some((key, raw)) = line.split_once(':') else {
            continue;
        };
        let value = raw.trim();
        if value.starts_with(['"', '\'']) {
            continue;
        }
        if let Some(stamp) = yaml_v3_timestamp(value) {
            result.insert(key.trim().to_owned(), stamp);
        }
    }
    result
}

/// yaml.v3 `parseTimestamp` (`2006-1-2T15:4:5.999999999Z07:00`, the
/// lowercase-`t` form, `2006-1-2 15:4:5.999999999` and `2006-1-2`), as the
/// RFC3339Nano text Go formats a decoded `time.Time` with.
fn yaml_v3_timestamp(value: &str) -> Option<String> {
    let bytes = value.as_bytes();
    let number = |at: &mut usize, max: usize| -> Option<u32> {
        let start = *at;
        while *at < bytes.len() && *at - start < max && bytes[*at].is_ascii_digit() {
            *at += 1;
        }
        if *at == start {
            return None;
        }
        value[start..*at].parse().ok()
    };
    if bytes.len() < 8 || !bytes[0..4].iter().all(u8::is_ascii_digit) || bytes[4] != b'-' {
        return None;
    }
    let year: u32 = value[0..4].parse().ok()?;
    let mut at = 5;
    let month = number(&mut at, 2)?;
    if bytes.get(at) != Some(&b'-') || !(1..=12).contains(&month) {
        return None;
    }
    at += 1;
    let day = number(&mut at, 2)?;
    let leap = (year.is_multiple_of(4) && !year.is_multiple_of(100)) || year.is_multiple_of(400);
    let days = match month {
        2 if leap => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    };
    if day == 0 || day > days {
        return None;
    }
    let date = format!("{year:04}-{month:02}-{day:02}");
    if at == bytes.len() {
        return Some(format!("{date}T00:00:00Z"));
    }
    let zoned = match bytes[at] {
        b'T' | b't' => true,
        b' ' => false,
        _ => return None,
    };
    at += 1;
    let hour = number(&mut at, 2)?;
    if bytes.get(at) != Some(&b':') || hour > 23 {
        return None;
    }
    at += 1;
    let minute = number(&mut at, 2)?;
    if bytes.get(at) != Some(&b':') || minute > 59 {
        return None;
    }
    at += 1;
    let second = number(&mut at, 2)?;
    if second > 59 {
        return None;
    }
    let mut fraction = String::new();
    if matches!(bytes.get(at), Some(b'.' | b',')) {
        let start = at + 1;
        let mut end = start;
        while end < bytes.len() && bytes[end].is_ascii_digit() {
            end += 1;
        }
        if end == start {
            return None;
        }
        let digits = value[start..end].trim_end_matches('0');
        if !digits.is_empty() {
            fraction = format!(".{digits}");
        }
        at = end;
    }
    let clock = format!("{date}T{hour:02}:{minute:02}:{second:02}{fraction}");
    if !zoned {
        return (at == bytes.len()).then(|| format!("{clock}Z"));
    }
    if bytes.get(at) == Some(&b'Z') {
        return (at + 1 == bytes.len()).then(|| format!("{clock}Z"));
    }
    if at + 6 != bytes.len() || !matches!(bytes[at], b'+' | b'-') || bytes[at + 3] != b':' {
        return None;
    }
    let zone_hours: u32 = value[at + 1..at + 3].parse().ok()?;
    let zone_minutes: u32 = value[at + 4..at + 6].parse().ok()?;
    if zone_hours == 0 && zone_minutes == 0 {
        return Some(format!("{clock}Z"));
    }
    Some(format!("{clock}{}", &value[at..]))
}

/// Returns every repeated top-level key, in document order, with its 1-based
/// line numbers in the frontmatter block: first occurrence, then the repeat.
fn duplicate_top_level_keys(input: &[u8]) -> Vec<(String, usize, usize)> {
    let text = String::from_utf8_lossy(input);
    let mut seen = std::collections::BTreeMap::new();
    let mut duplicates = Vec::new();
    for (index, line) in text.lines().enumerate() {
        if line.starts_with([' ', '\t']) || line.trim_start().starts_with('#') {
            continue;
        }
        let Some((key, _)) = line.split_once(':') else {
            continue;
        };
        let key = key.trim();
        if key.is_empty() {
            continue;
        }
        if let Some(first) = seen.get(key) {
            duplicates.push((key.to_owned(), *first, index + 1));
            continue;
        }
        seen.insert(key.to_owned(), index + 1);
    }
    duplicates
}

fn string_or_list(values: &BTreeMap<String, Value>, key: &str) -> Vec<String> {
    match values.get(key) {
        Some(Value::String(value)) => vec![value.clone()],
        Some(Value::Sequence(values)) => values
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect(),
        _ => Vec::new(),
    }
}

fn integer_value(value: Option<&Value>) -> i64 {
    value.map_or(0, |value| {
        value
            .as_i64()
            .or_else(|| value.as_u64().and_then(|number| i64::try_from(number).ok()))
            .or_else(|| value.as_f64().map(|number| number as i64))
            .unwrap_or(0)
    })
}

fn parse_asn(path: &str, value: Option<&Value>) -> Result<Option<i64>, VaultError> {
    let Some(value) = value else {
        return Ok(None);
    };
    let Some(number) = value
        .as_i64()
        .or_else(|| value.as_u64().and_then(|number| i64::try_from(number).ok()))
    else {
        return Err(VaultError::Asn {
            path: path.to_owned(),
            reason: "must be a positive integer",
        });
    };
    if number < 1 {
        return Err(VaultError::Asn {
            path: path.to_owned(),
            reason: "must be a positive integer",
        });
    }
    Ok(Some(number))
}

fn infer_type(frontmatter: &BTreeMap<String, Value>) -> String {
    if let Some(value) = frontmatter.get("type").and_then(Value::as_str)
        && matches!(
            value,
            "note" | "document" | "meeting" | "notebook" | "base" | "dataset"
        )
    {
        return value.to_owned();
    }
    if ["source_path", "mime", "sha256", "document_date", "asn"]
        .iter()
        .any(|key| frontmatter.contains_key(*key))
    {
        "document".to_owned()
    } else if frontmatter.contains_key("meeting_id") {
        "meeting".to_owned()
    } else if frontmatter.contains_key("base_id") {
        "base".to_owned()
    } else {
        "note".to_owned()
    }
}
