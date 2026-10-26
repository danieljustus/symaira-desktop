//! Reviewed meeting-note reads from authoritative Markdown, never the index.
use std::path::Path;

use cap_std::{ambient_authority, fs::Dir};
use serde::Serialize;
use symdesk_vault::{Document, go_path_error, parse_bytes, secure_path, walk_markdown};

pub(super) fn read(root: &Path, requested: &str) -> Result<Document, String> {
    let path = secure_path(root, requested).map_err(|e| e.to_string())?;
    let relative = path.strip_prefix(root).map_err(|e| e.to_string())?;
    let dir = Dir::open_ambient_dir(root, ambient_authority()).map_err(|e| e.to_string())?;
    let file = dir.open(relative).map_err(|e| {
        format!(
            "open vault file {}: {}",
            path.display(),
            go_path_error("openat", relative, &e)
        )
    })?;
    let meta = file.metadata().map_err(|e| e.to_string())?;
    if !meta.is_file() {
        return Err(format!(
            "vault path is not a regular file: {}",
            path.display()
        ));
    }
    if meta.len() > 64 * 1024 * 1024 {
        return Err(format!(
            "vault file exceeds 67108864 byte read limit: {}",
            path.display()
        ));
    }
    use std::io::Read;
    let mut bytes = Vec::new();
    file.take(64 * 1024 * 1024 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    if bytes.len() > 64 * 1024 * 1024 {
        return Err(format!(
            "vault file exceeds 67108864 byte read limit: {}",
            path.display()
        ));
    }
    parse_bytes(&path.to_string_lossy(), &bytes).map_err(|e| e.to_string())
}

#[derive(Serialize)]
struct Summary {
    path: String,
    title: String,
    meeting_id: String,
    started_at: String,
    duration_ms: i64,
    language: String,
    review_state: String,
}

pub(super) fn list(root: &Path) -> Result<serde_json::Value, String> {
    let mut summaries = Vec::new();
    for path in walk_markdown(root).map_err(|e| e.to_string())? {
        let relative = super::backlink_relative_path(root, &path);
        let Ok(doc) = read(root, &relative) else {
            continue;
        };
        let text = |key: &str| {
            if doc.yaml_timestamps.contains_key(key) {
                return String::new();
            }
            doc.frontmatter
                .get(key)
                .and_then(noyalib::Value::as_str)
                .unwrap_or_default()
                .to_owned()
        };
        if text("type") != "meeting" {
            continue;
        }
        let duration_ms = doc
            .frontmatter
            .get("duration_ms")
            .and_then(|value| value.as_i64().or_else(|| value.as_f64().map(|n| n as i64)))
            .unwrap_or_default();
        let review_state = doc
            .frontmatter
            .get("symmeet_source")
            .and_then(|v| v.get("review_state"))
            .and_then(noyalib::Value::as_str)
            .unwrap_or_default()
            .to_owned();
        summaries.push(Summary {
            path: relative,
            title: doc.title.clone(),
            meeting_id: text("meeting_id"),
            started_at: text("started_at"),
            duration_ms,
            language: text("language"),
            review_state,
        });
    }
    super::encoded(&summaries)
}

pub(super) fn show(root: &Path, requested: &str) -> Result<serde_json::Value, String> {
    let doc = read(root, requested)?;
    if doc.frontmatter.get("type").and_then(noyalib::Value::as_str) != Some("meeting") {
        return Err(format!(
            "{requested} is not a meeting note (frontmatter type != \"meeting\")"
        ));
    }
    let modified = std::fs::metadata(&doc.path)
        .and_then(|meta| meta.modified())
        .map_err(|e| e.to_string())?;
    let modified = time::OffsetDateTime::from(modified)
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|e| e.to_string())?;
    // Go's exported Document has no JSON tags: declaration order and nil slices
    // are observable, unlike the lowercase map-shaped meeting-list entries.
    #[derive(Serialize)]
    #[serde(rename_all = "PascalCase")]
    struct WireDocument<'a> {
        path: &'a str,
        #[serde(rename = "SHA256")]
        sha256: &'a str,
        title: &'a str,
        created: &'a str,
        tags: Option<&'a Vec<String>>,
        aliases: Option<&'a Vec<String>>,
        frontmatter: serde_json::Value,
        body: &'a str,
        links: Option<&'a Vec<String>>,
        size: i64,
        mod_time: String,
        document_date: &'a str,
        person: &'a str,
        status: &'a str,
        due_date: &'a str,
        confidence: i64,
        #[serde(rename = "OcrJSONPath")]
        ocr_json_path: &'a str,
        simhash: &'a str,
        #[serde(rename = "ASN")]
        asn: Option<i64>,
        #[serde(rename = "Type")]
        kind: &'a str,
        derived_from: &'a str,
        derived: bool,
    }
    let mut frontmatter = serde_json::to_value(&doc.frontmatter).map_err(|e| e.to_string())?;
    for (key, stamp) in &doc.yaml_timestamps {
        let parsed =
            time::OffsetDateTime::parse(stamp, &time::format_description::well_known::Rfc3339)
                .map_err(|e| e.to_string())?;
        frontmatter[key] = serde_json::Value::String(
            parsed
                .format(&time::format_description::well_known::Rfc3339)
                .map_err(|e| e.to_string())?,
        );
    }
    super::encoded(&WireDocument {
        path: &doc.path,
        sha256: &doc.sha256,
        title: &doc.title,
        created: &doc.created,
        tags: (!doc.tags.is_empty()).then_some(&doc.tags),
        aliases: (!doc.aliases.is_empty()).then_some(&doc.aliases),
        frontmatter,
        body: &doc.body,
        links: (!doc.links.is_empty()).then_some(&doc.links),
        size: doc.size,
        mod_time: modified,
        document_date: &doc.document_date,
        person: &doc.person,
        status: &doc.status,
        due_date: &doc.due_date,
        confidence: doc.confidence,
        ocr_json_path: &doc.ocr_json_path,
        simhash: &doc.simhash,
        asn: doc.asn,
        kind: &doc.document_type,
        derived_from: &doc.derived_from,
        derived: doc.derived,
    })
}
