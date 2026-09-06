#![deny(unsafe_code)]

//! Read-only contract-v1–v6 Markdown vault parsing.

mod health_links;
mod links;
mod metadata;
mod paths;
mod resolver;
mod sha256;
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

pub use health_links::{HealthLinkResolver, LinkInventory, normalize_health_link_target};
pub use links::extract_wikilinks;
pub use metadata::{
    SearchMetadata, SearchMetadataField, format_search_metadata, metadata_matches,
    search_metadata_from_document, strip_search_metadata,
};
pub use paths::{SecurePathError, secure_path};
pub use resolver::{ResolveDocument, ResolvedEdge, ResolvedNode, Resolver, resolve_graph};
pub use tags::{TagSpan, extract_inline_tags, find_inline_tag_spans};
pub use typed::{
    Base, ComputedColumn, Coverage, DatasetHandle, Filter, FilterGroup, Notebook, PropertyConfig,
    Provenance, Sort, Template, TypedVaultError, View, parse_base, parse_dataset_handle,
    parse_notebook,
};
pub use walk::{WalkEntry, WalkEntryType, walk_all, walk_markdown, walk_markdown_with};

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
        if let Some(key) = duplicate_top_level_key(&frontmatter_bytes) {
            return Err(VaultError::Frontmatter {
                path: path.to_owned(),
                detail: format!("mapping key {key:?} already defined"),
            });
        }
        noyalib::from_slice::<BTreeMap<String, Value>>(&frontmatter_bytes).map_err(|error| {
            VaultError::Frontmatter {
                path: path.to_owned(),
                detail: error.to_string(),
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
        if is_iso_date(value) {
            result.insert(key.trim().to_owned(), format!("{value}T00:00:00Z"));
        }
    }
    result
}

fn duplicate_top_level_key(input: &[u8]) -> Option<String> {
    let text = String::from_utf8_lossy(input);
    let mut seen = std::collections::BTreeSet::new();
    for line in text.lines() {
        if line.starts_with([' ', '\t']) || line.trim_start().starts_with('#') {
            continue;
        }
        let Some((key, _)) = line.split_once(':') else {
            continue;
        };
        let key = key.trim();
        if !key.is_empty() && !seen.insert(key.to_owned()) {
            return Some(key.to_owned());
        }
    }
    None
}

fn is_iso_date(value: &str) -> bool {
    let bytes = value.as_bytes();
    bytes.len() == 10
        && bytes[4] == b'-'
        && bytes[7] == b'-'
        && bytes
            .iter()
            .enumerate()
            .all(|(index, byte)| matches!(index, 4 | 7) || byte.is_ascii_digit())
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
