//! Filesystem Paperless-ngx manifest importer (`internal/paperless`). This is
//! distinct from the API-backed ingest pipeline and does not pretend to update
//! its import-state database. Optional search indexing belongs to the caller.
use crate::Result;
use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};
use symdesk_vault::{go_path_error, go_quote, go_read, parse_bytes, walk_markdown};
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct ManifestNote {
    pub note: String,
    pub created: Option<String>,
    pub user: Option<i64>,
}
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct ManifestEntry {
    pub id: i64,
    pub correspondent: Option<String>,
    pub document_type: Option<String>,
    pub title: String,
    pub content: String,
    pub tags: Option<Vec<String>>,
    pub created: Option<String>,
    pub added: Option<String>,
    pub modified: Option<String>,
    pub file_size: Option<i64>,
    pub checksum: Option<String>,
    pub archive_serial_number: Option<i64>,
    pub original_file_name: Option<String>,
    pub archived_file_name: Option<String>,
    pub notes: Option<Vec<ManifestNote>>,
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}
#[derive(Clone, Debug)]
pub struct ImportOptions {
    pub vault_root: PathBuf,
    pub export_dir: PathBuf,
    pub dry_run: bool,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct ImportResult {
    pub paperless_id: i64,
    pub title: String,
    pub action: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub note_path: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub asn: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
}
fn is_zero(v: &i64) -> bool {
    *v == 0
}
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct ImportSummary {
    pub total: usize,
    pub created: usize,
    pub updated: usize,
    pub skipped: usize,
    pub errors: usize,
    pub results: Option<Vec<ImportResult>>,
}
fn text(path: impl AsRef<Path>) -> String {
    path.as_ref().to_string_lossy().into_owned()
}
pub fn parse_manifest(path: &Path) -> Result<Option<Vec<ManifestEntry>>> {
    let data = go_read(path)
        .map_err(|(op, e)| format!("read manifest file: {}", go_path_error(op, path, &e)))?;
    serde_json::from_slice(&data).map_err(|e| format!("parse manifest: {e}"))
}
pub fn source_identifier(entry: &ManifestEntry) -> String {
    format!(
        "paperless:{}:{}",
        entry.id,
        entry.checksum.as_deref().unwrap_or_default()
    )
}
pub fn sanitize_file_name(value: &str) -> String {
    let mut out = String::new();
    for c in value.chars() {
        let c = if "/\\:*?\"<>|\n\r\t".contains(c) {
            '_'
        } else {
            c
        };
        if c == '_' && out.ends_with('_') {
            continue;
        }
        out.push(c);
    }
    let out = out.trim_end_matches(['.', ' ']);
    let mut end = out.len().min(120);
    while !out.is_char_boundary(end) {
        end -= 1;
    }
    out[..end].into()
}
pub fn note_file_name(entry: &ManifestEntry) -> String {
    let title = sanitize_file_name(&entry.title);
    let title = if title.is_empty() {
        format!("doc_{}", entry.id)
    } else {
        title
    };
    format!("Paperless_{}_{title}.md", entry.id)
}
pub fn archive_file_name(entry: &ManifestEntry) -> String {
    entry
        .archived_file_name
        .as_ref()
        .filter(|s| !s.is_empty())
        .or_else(|| entry.original_file_name.as_ref().filter(|s| !s.is_empty()))
        .cloned()
        .unwrap_or_else(|| format!("paperless_{}", entry.id))
}
pub fn build_note(entry: &ManifestEntry, archive: &str, source: &str, now: &str) -> String {
    let mut out = format!(
        "---\ntitle: {}\ncreated: {}\n",
        go_quote(&entry.title),
        go_quote(now)
    );
    if let Some(tags) = entry.tags.as_ref().filter(|t| !t.is_empty()) {
        out.push_str("tags:\n");
        for tag in tags {
            out.push_str(&format!("  - {}\n", go_quote(tag)));
        }
    } else {
        out.push_str("tags: []\n");
    }
    out.push_str(&format!(
        "status: \"open\"\nsource_identifier: {}\nimported_from: \"paperless\"\n",
        go_quote(source)
    ));
    for (k, v) in [
        ("correspondent", &entry.correspondent),
        ("document_type", &entry.document_type),
    ] {
        if let Some(v) = v.as_ref().filter(|s| !s.is_empty()) {
            out.push_str(&format!("{k}: {}\n", go_quote(v)));
        }
    }
    if let Some(date) = entry.created.as_ref().filter(|s| s.len() >= 10) {
        out.push_str(&format!(
            "document_date: {}\n",
            go_quote(&String::from_utf8_lossy(&date.as_bytes()[..10]))
        ));
    }
    if let Some(asn) = entry.archive_serial_number.filter(|n| *n > 0) {
        out.push_str(&format!("asn: {asn}\n"));
    }
    out.push_str(&format!(
        "archive_path: {}\npaperless:\n  id: {}\n",
        go_quote(archive),
        entry.id
    ));
    for (k, v) in [
        ("checksum", &entry.checksum),
        ("added", &entry.added),
        ("modified", &entry.modified),
        ("original_file_name", &entry.original_file_name),
    ] {
        if let Some(v) = v {
            out.push_str(&format!("  {k}: {}\n", go_quote(v)));
        }
    }
    out.push_str("confidence: 100\n");
    if let Some(name) = &entry.archived_file_name {
        let ext = Path::new(name)
            .extension()
            .unwrap_or_default()
            .to_string_lossy()
            .to_ascii_lowercase();
        let mime = match ext.as_str() {
            "pdf" => "application/pdf",
            "png" => "image/png",
            "jpg" | "jpeg" => "image/jpeg",
            "tiff" | "tif" => "image/tiff",
            "gif" => "image/gif",
            "txt" => "text/plain",
            "docx" => "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            "odt" => "application/vnd.oasis.opendocument.text",
            _ => "",
        };
        if !mime.is_empty() {
            out.push_str(&format!("mime: {}\n", go_quote(mime)));
        }
    }
    out.push_str("---\n\n");
    if !entry.content.is_empty() {
        out.push_str(&entry.content);
        out.push_str("\n\n");
    }
    out.push_str(&format!("[[{archive}]]\n"));
    if let Some(notes) = entry.notes.as_ref().filter(|n| !n.is_empty()) {
        out.push_str("\n## Paperless Notes\n\n");
        for note in notes {
            out.push_str(&format!("- {}\n", note.note));
            if let Some(created) = &note.created {
                out.push_str(&format!("  (added: {created})\n"));
            }
        }
    }
    out
}
/// Import with an explicit UTC RFC3339 clock for byte-exact oracle replay.
pub fn import_at(opts: &ImportOptions, now: &str) -> Result<ImportSummary> {
    let entries = parse_manifest(&opts.export_dir.join("manifest.json"))
        .map_err(|e| format!("parse manifest: {e}"))?
        .unwrap_or_default();
    let archive_dir = opts.vault_root.join("archive/paperless");
    if !opts.dry_run {
        fs::create_dir_all(&archive_dir).map_err(|e| {
            format!(
                "create archive dir: {}",
                go_path_error("mkdir", &archive_dir, &e)
            )
        })?;
    }
    let paths = walk_markdown(&opts.vault_root).map_err(|e| format!("scan vault ASNs: {e}"))?;
    let mut asns: BTreeMap<i64, Vec<String>> = BTreeMap::new();
    let mut sources = BTreeMap::new();
    for relative_path in paths {
        let path = opts.vault_root.join(&relative_path);
        let Ok(bytes) = go_read(&path) else { continue };
        let Ok(doc) = parse_bytes(&text(&path), &bytes) else {
            continue;
        };
        if let Some(asn) = doc.asn {
            asns.entry(asn).or_default().push(text(&relative_path));
        }
        if let Some(source) = doc
            .frontmatter
            .get("source_identifier")
            .and_then(noyalib::Value::as_str)
        {
            sources.insert(source.to_owned(), path);
        }
    }
    let mut summary = ImportSummary {
        total: entries.len(),
        ..ImportSummary::default()
    };
    for entry in entries {
        let source = source_identifier(&entry);
        let relative = Path::new("paperless").join(note_file_name(&entry));
        let mut note_path = opts.vault_root.join(&relative);
        let archive_name = archive_file_name(&entry);
        let archive_relative = Path::new("archive/paperless").join(&archive_name);
        let archive_path = opts.vault_root.join(&archive_relative);
        let mut r = ImportResult {
            paperless_id: entry.id,
            title: entry.title.clone(),
            ..ImportResult::default()
        };
        let result = (|| -> Result<()> {
            if let Some(asn) = entry.archive_serial_number.filter(|n| *n > 0) {
                if asns
                    .get(&asn)
                    .is_some_and(|paths| paths.iter().any(|p| p != &text(&relative)))
                {
                    return Err(format!("ASN {asn} is already assigned to another note"));
                }
                r.asn = asn;
            }
            if let Some(path) = sources.get(&source) {
                r.action = "updated".into();
                r.note_path = text(path);
                note_path = path.clone();
            } else {
                r.action = "created".into();
                r.note_path = text(&relative);
                if !opts.dry_run {
                    fs::create_dir_all(note_path.parent().unwrap())
                        .map_err(|e| format!("create note dir: {e}"))?;
                }
            }
            if opts.dry_run {
                return Ok(());
            }
            let src = opts.export_dir.join(&archive_name);
            match fs::File::open(&src) {
                Ok(mut reader) => {
                    let mut target = fs::File::create(&archive_path).map_err(|e| {
                        format!(
                            "copy archive file: {}",
                            go_path_error("open", &archive_path, &e)
                        )
                    })?;
                    std::io::copy(&mut reader, &mut target)
                        .map_err(|e| format!("copy archive file: {e}"))?;
                    target
                        .sync_all()
                        .map_err(|e| format!("copy archive file: {e}"))?;
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => {
                    return Err(format!(
                        "copy archive file: {}",
                        go_path_error("open", &src, &e)
                    ));
                }
            }
            fs::write(
                &note_path,
                build_note(&entry, &text(&archive_relative), &source, now),
            )
            .map_err(|e| format!("write note: {}", go_path_error("open", &note_path, &e)))?;
            sources.insert(source, note_path);
            Ok(())
        })();
        match result {
            Err(e) => {
                r.action = "error".into();
                r.error = e;
                summary.errors += 1
            }
            Ok(()) if r.action == "created" => summary.created += 1,
            Ok(()) if r.action == "updated" => summary.updated += 1,
            _ => {}
        }
        summary.results.get_or_insert_default().push(r);
    }
    Ok(summary)
}
pub fn import(opts: &ImportOptions) -> Result<ImportSummary> {
    let now = time::OffsetDateTime::now_utc()
        .replace_nanosecond(0)
        .map_err(|e| e.to_string())?
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|e| e.to_string())?;
    import_at(opts, &now)
}
