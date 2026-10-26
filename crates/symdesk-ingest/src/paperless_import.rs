//! Pure API-import planning, reference mapping and dry-run audit. The
//! download/archive/write orchestration and verifier remain separate contracts.
use crate::paperless::{Document, DownloadMetadata, Ref};
use serde::{Deserialize, Serialize};
use std::{
    collections::{BTreeMap, BTreeSet},
    path::Path,
};
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct Lookups {
    pub tags: BTreeMap<i64, String>,
    pub correspondents: BTreeMap<i64, String>,
    pub document_types: BTreeMap<i64, String>,
    pub storage_paths: BTreeMap<i64, String>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct ResolvedMeta {
    pub tags: Vec<String>,
    pub correspondent: String,
    pub document_type: String,
    pub storage_path: String,
    pub warnings: Option<Vec<String>>,
}
pub fn resolve_ref<'a>(reference: &'a Ref, table: &'a BTreeMap<i64, String>) -> Option<&'a str> {
    if !reference.name.is_empty() {
        Some(&reference.name)
    } else if reference.id == 0 {
        Some("")
    } else {
        table.get(&reference.id).map(String::as_str)
    }
}
pub fn resolve_document_meta(doc: &Document, lookup: &Lookups) -> ResolvedMeta {
    let mut m = ResolvedMeta::default();
    let mut resolve = |r: &Ref, table: &BTreeMap<i64, String>, kind: &str, fallback: bool| {
        if let Some(name) = resolve_ref(r, table) {
            name.to_owned()
        } else {
            m.warnings.get_or_insert_default().push(format!(
                "document {} ({}): unresolved {kind} ID {}",
                doc.id, doc.title, r.id
            ));
            if fallback {
                format!("id:{}", r.id)
            } else {
                String::new()
            }
        }
    };
    for r in doc.tags.iter().flatten() {
        let name = resolve(r, &lookup.tags, "tag", true);
        if !name.is_empty() {
            m.tags.push(name);
        }
    }
    if let Some(r) = &doc.correspondent {
        m.correspondent = resolve(r, &lookup.correspondents, "correspondent", true);
    }
    if let Some(r) = &doc.document_type {
        m.document_type = resolve(r, &lookup.document_types, "document type", true);
    }
    if let Some(r) = &doc.storage_path {
        m.storage_path = resolve(r, &lookup.storage_paths, "storage path", false);
    }
    m
}
pub fn paperless_created(doc: &Document) -> &str {
    doc.created
        .0
        .as_deref()
        .or(doc.created_date.0.as_deref())
        .unwrap_or("0001-01-01T00:00:00Z")
}
pub fn normalize_content_type(candidate: &str) -> String {
    candidate
        .split(';')
        .next()
        .unwrap_or_default()
        .trim()
        .to_lowercase()
}
pub fn normalize_extension(candidate: &str) -> String {
    let c = candidate.trim();
    if c.is_empty() {
        return String::new();
    }
    if c.starts_with('.') && !c[1..].contains(['/', '\\']) {
        return c.to_lowercase();
    }
    if !c.contains(['/', '\\', '.']) {
        return format!(".{}", c.to_lowercase());
    }
    let file = Path::new(c)
        .file_name()
        .unwrap_or_default()
        .to_string_lossy();
    file.rfind('.')
        .map(|i| file[i..].to_lowercase())
        .unwrap_or_default()
}
fn mime_extension(mime: &str) -> &'static str {
    match normalize_content_type(mime).as_str() {
        "application/pdf" => ".pdf",
        "image/png" => ".png",
        "image/jpeg" => ".jpg",
        "image/tiff" => ".tiff",
        "image/webp" => ".webp",
        "image/heic" => ".heic",
        "image/heif" => ".heif",
        "text/plain" => ".txt",
        "text/csv" => ".csv",
        "text/markdown" => ".md",
        "text/html" => ".html",
        "application/rtf" | "text/rtf" => ".rtf",
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document" => ".docx",
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" => ".xlsx",
        "application/vnd.oasis.opendocument.text" => ".odt",
        "message/rfc822" => ".eml",
        _ => "",
    }
}
pub fn download_extension(doc: &Document, meta: &DownloadMetadata) -> String {
    for candidate in [
        &meta.filename,
        &doc.archived_file_name,
        &doc.original_file_name,
        &doc.file_type,
    ] {
        let ext = normalize_extension(candidate);
        if !ext.is_empty() {
            return ext;
        }
    }
    for mime in [&meta.content_type, &doc.mime_type] {
        let ext = mime_extension(mime);
        if !ext.is_empty() {
            return ext.into();
        }
    }
    String::new()
}
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct DocumentResult {
    pub id: i64,
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub stage: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub vault_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub archive_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sha256: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub mime: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub expected_extension: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub actual_archive_extension: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub import_run_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source_uri: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub download_uri: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
}
pub fn source_uri(id: i64) -> String {
    format!("paperless://documents/{id}")
}
pub fn planned_document_result(doc: &Document, run: &str, status: &str) -> DocumentResult {
    let ext = download_extension(doc, &DownloadMetadata::default());
    let source = source_uri(doc.id);
    DocumentResult {
        id: doc.id,
        status: status.into(),
        mime: doc.mime_type.clone(),
        expected_extension: if ext.is_empty() { ".bin".into() } else { ext },
        import_run_id: run.into(),
        download_uri: format!("{source}/download"),
        source_uri: source,
        ..DocumentResult::default()
    }
}
pub fn sanitize_storage_path(path: &str) -> String {
    let parts = path
        .split(['/', '\\'])
        .map(|p| {
            p.trim()
                .chars()
                .map(|c| {
                    if c < '\u{20}' || "<>:\"|?*/\\".contains(c) {
                        '_'
                    } else {
                        c
                    }
                })
                .collect::<String>()
        })
        .map(|p| p.trim().trim_matches('.').to_owned())
        .filter(|p| !p.is_empty())
        .collect::<Vec<_>>();
    parts.join(std::path::MAIN_SEPARATOR_STR)
}
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct AuditReport {
    pub total_documents: usize,
    pub by_mime: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub tag_counts: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub correspondent_counts: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub document_type_counts: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub storage_path_counts: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "BTreeSet::is_empty")]
    pub unresolved_tag_ids: BTreeSet<i64>,
    #[serde(default, skip_serializing_if = "BTreeSet::is_empty")]
    pub unresolved_correspondent_ids: BTreeSet<i64>,
    #[serde(default, skip_serializing_if = "BTreeSet::is_empty")]
    pub unresolved_document_type_ids: BTreeSet<i64>,
    #[serde(default, skip_serializing_if = "BTreeSet::is_empty")]
    pub unresolved_storage_path_ids: BTreeSet<i64>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub unsupported_file_types: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub by_expected_extension: BTreeMap<String, usize>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub required_tools: Vec<String>,
}
fn count_ref(
    r: &Ref,
    table: &BTreeMap<i64, String>,
    counts: &mut BTreeMap<String, usize>,
    missing: &mut BTreeSet<i64>,
) {
    match resolve_ref(r, table) {
        None => {
            missing.insert(r.id);
        }
        Some("") => {}
        Some(name) => *counts.entry(name.into()).or_default() += 1,
    }
}
pub fn build_audit_report(docs: &[Document], lookup: &Lookups) -> AuditReport {
    let mut out = AuditReport {
        total_documents: docs.len(),
        ..AuditReport::default()
    };
    for doc in docs {
        let mime = if doc.mime_type.is_empty() {
            "unknown"
        } else {
            &doc.mime_type
        };
        *out.by_mime.entry(mime.into()).or_default() += 1;
        for r in doc.tags.iter().flatten() {
            count_ref(
                r,
                &lookup.tags,
                &mut out.tag_counts,
                &mut out.unresolved_tag_ids,
            );
        }
        if let Some(r) = &doc.correspondent {
            count_ref(
                r,
                &lookup.correspondents,
                &mut out.correspondent_counts,
                &mut out.unresolved_correspondent_ids,
            );
        }
        if let Some(r) = &doc.document_type {
            count_ref(
                r,
                &lookup.document_types,
                &mut out.document_type_counts,
                &mut out.unresolved_document_type_ids,
            );
        }
        if let Some(r) = &doc.storage_path {
            count_ref(
                r,
                &lookup.storage_paths,
                &mut out.storage_path_counts,
                &mut out.unresolved_storage_path_ids,
            );
        }
        let ext = download_extension(doc, &DownloadMetadata::default());
        let ext = ext.strip_prefix('.').unwrap_or(&ext);
        let ext = if ext.is_empty() { "unknown" } else { ext };
        *out.by_expected_extension.entry(ext.into()).or_default() += 1;
        let tools: &[&str] = match ext {
            "pdf" => &["pdftoppm", "tesseract"],
            "png" | "jpg" | "jpeg" | "tiff" | "tif" | "webp" | "heic" | "heif" => &["tesseract"],
            _ => &[],
        };
        for tool in tools {
            if !out.required_tools.iter().any(|t| t == tool) {
                out.required_tools.push((*tool).into());
            }
        }
        if ![
            "pdf", "png", "jpg", "jpeg", "tiff", "tif", "webp", "heic", "heif", "txt", "text",
            "csv", "md", "markdown", "html", "htm", "rtf", "docx", "xlsx", "odt", "eml",
        ]
        .contains(&ext)
            && mime_extension(&doc.mime_type).is_empty()
        {
            *out.unsupported_file_types.entry(ext.into()).or_default() += 1;
        }
    }
    out
}
pub fn bounded_error_string(error: &str) -> String {
    let s = error.split_whitespace().collect::<Vec<_>>().join(" ");
    if s.len() <= 1024 {
        s
    } else {
        format!("{}…", String::from_utf8_lossy(&s.as_bytes()[..1024]))
    }
}
pub fn stage_from_error(error: &str, explicit: Option<&str>) -> String {
    if let Some(stage) = explicit.filter(|s| !s.is_empty()) {
        return stage.into();
    }
    for (needle, stage) in [
        ("download", "download"),
        ("detect source type", "detect"),
        ("extract text", "extract"),
        ("archive file", "archive"),
        ("write note", "write"),
        ("record document", "metadata"),
        ("set vault path", "metadata"),
        ("complete job", "metadata"),
    ] {
        if error.contains(needle) {
            return stage.into();
        }
    }
    "import".into()
}
