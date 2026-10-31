//! Paperless import verification (`paperlessimport.Verify`): compares every
//! selected Paperless document against the durable import state, the vault
//! notes that carry its identity and the archived original.

use crate::Store;
use crate::paperless::{Client, Document, FlexDate};
use crate::paperless_import::Lookups;
use crate::paperless_import::resolve_document_meta;
use crate::paperless_run::{
    Options, REPORT_SCHEMA_VERSION, TOOL_VERSION, load_lookups, select_documents,
};
use crate::report_json::{GoFloat, go_seconds, go_time};
use crate::writer::{Note, PaperlessMeta};
use serde::Serialize;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::io::Write;
use std::path::Path;
use time::OffsetDateTime;

/// `VerifyMismatch`.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct VerifyMismatch {
    pub document_id: i64,
    pub field: String,
    pub expected: String,
    pub got: String,
}

/// `VerifyReport`.
#[derive(Clone, Debug, Serialize)]
pub struct VerifyReport {
    pub schema_version: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub run_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub tool_version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source_url: String,
    pub started_at: String,
    pub finished_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub duration_seconds: Option<GoFloat>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mode: String,
    pub source_documents: usize,
    pub vault_notes: usize,
    pub verified: usize,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub deep_verify: bool,
    #[serde(skip_serializing_if = "is_zero")]
    pub deep_verified: usize,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub missing: Vec<i64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub duplicate: Vec<i64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub duplicate_content: Vec<i64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub missing_archive: Vec<i64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub hash_mismatch: Vec<i64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub source_hash_mismatch: Vec<i64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub mismatches: Vec<VerifyMismatch>,
}

#[allow(clippy::trivially_copy_pass_by_ref)]
fn is_zero(n: &usize) -> bool {
    *n == 0
}

impl VerifyReport {
    /// `Complete`: no missing, duplicate-note, archive-less, hash-mismatched
    /// or metadata-mismatched documents (`duplicate_content` is informational).
    #[must_use]
    pub fn complete(&self) -> bool {
        self.missing.is_empty()
            && self.duplicate.is_empty()
            && self.missing_archive.is_empty()
            && self.hash_mismatch.is_empty()
            && self.source_hash_mismatch.is_empty()
            && self.mismatches.is_empty()
    }
}

fn file_exists(path: &str) -> bool {
    std::fs::metadata(path).is_ok_and(|m| !m.is_dir())
}

fn file_sha256(path: &str) -> Result<String, String> {
    crate::pipeline::hash_file(path)
}

struct ImportState {
    status: String,
    vault_path: String,
    archive_path: String,
    sha256: String,
}

fn import_state(
    store: Option<&Store>,
    base_url: &str,
    vault: &str,
    archive: &str,
    id: i64,
) -> Result<Option<ImportState>, String> {
    let Some(store) = store else {
        return Ok(None);
    };
    let state = if vault.is_empty() && archive.is_empty() {
        store.paperless_import_state_by_id(base_url, id)
    } else {
        store.paperless_import_state_for_target(base_url, vault, archive, id)
    };
    match state {
        Ok(s) => Ok(Some(ImportState {
            status: s.status,
            vault_path: s.vault_path,
            archive_path: s.archive_path,
            sha256: s.sha256,
        })),
        Err(e) if e == "sql: no rows in result set" => Ok(None),
        Err(e) => Err(e),
    }
}

fn scalar_string(value: &serde_json::Value) -> Option<String> {
    match value {
        serde_json::Value::Null => Some(String::new()),
        serde_json::Value::String(s) => Some(s.clone()),
        serde_json::Value::Number(n) => Some(n.to_string()),
        serde_json::Value::Bool(b) => Some(b.to_string()),
        _ => None,
    }
}

fn parse_time(value: &serde_json::Value) -> Option<Option<OffsetDateTime>> {
    match value {
        serde_json::Value::Null => Some(None),
        serde_json::Value::String(s) => {
            if let Ok(t) =
                OffsetDateTime::parse(s, &time::format_description::well_known::Rfc3339)
            {
                return Some(Some(t));
            }
            let day = time::macros::format_description!("[year]-[month]-[day]");
            time::Date::parse(s, day)
                .ok()
                .map(|d| Some(d.midnight().assume_utc()))
        }
        _ => None,
    }
}

/// `parseNoteFrontmatter`: the leading YAML block as a [`Note`], `None` for a
/// file without a well-formed block.
#[must_use]
pub fn parse_note_frontmatter(data: &[u8]) -> Option<Note> {
    let text = String::from_utf8_lossy(data);
    let rest = text.strip_prefix("---\n")?;
    let end = rest.find("\n---")?;
    let value: serde_json::Value = noyalib::from_str(&rest[..end]).ok()?;
    let map = match &value {
        serde_json::Value::Object(map) => map,
        serde_json::Value::Null => return Some(Note::default()),
        _ => return None,
    };
    let mut note = Note::default();
    let string = |key: &str| -> Option<String> { map.get(key).map_or(Some(String::new()), scalar_string) };
    note.source_path = string("source_path")?;
    note.sha256 = string("sha256")?;
    note.archive_path = string("archive_path")?;
    note.correspondent = string("correspondent")?;
    note.document_type = string("document_type")?;
    note.category = string("category")?;
    if let Some(tags) = map.get("tags") {
        match tags {
            serde_json::Value::Null => {}
            serde_json::Value::Array(list) => {
                for tag in list {
                    note.tags.push(scalar_string(tag)?);
                }
            }
            _ => return None,
        }
    }
    if let Some(paperless) = map.get("paperless") {
        match paperless {
            serde_json::Value::Null => {}
            serde_json::Value::Object(p) => {
                let pstring = |key: &str| -> Option<String> {
                    p.get(key).map_or(Some(String::new()), scalar_string)
                };
                let id = match p.get("document_id") {
                    None | Some(serde_json::Value::Null) => 0,
                    Some(serde_json::Value::Number(n)) => n.as_i64()?,
                    Some(serde_json::Value::String(s)) => s.parse::<i64>().ok()?,
                    _ => return None,
                };
                let when = |key: &str| -> Option<Option<OffsetDateTime>> {
                    p.get(key).map_or(Some(None), parse_time)
                };
                note.paperless = Some(PaperlessMeta {
                    document_id: id,
                    title: pstring("title")?,
                    created: when("created")?,
                    added: when("added")?,
                    modified: when("modified")?,
                    storage_path: pstring("storage_path")?,
                    original_file_name: pstring("original_file_name")?,
                    archived_file_name: pstring("archived_file_name")?,
                    page_count: match p.get("page_count") {
                        None | Some(serde_json::Value::Null) => 0,
                        Some(serde_json::Value::Number(n)) => n.as_i64()?,
                        _ => return None,
                    },
                    url: pstring("url")?,
                });
            }
            _ => return None,
        }
    }
    Some(note)
}

type Notes = BTreeMap<i64, Vec<Note>>;

/// `scanVaultNotes`.
fn scan_vault_notes(vault: &str) -> Result<Notes, String> {
    let mut notes = Notes::new();
    if vault.is_empty() {
        return Ok(notes);
    }
    match std::fs::metadata(vault) {
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(notes),
        Err(e) => return Err(crate::goos::path_error("stat", vault, &e)),
        Ok(m) if !m.is_dir() => return Ok(notes),
        Ok(_) => {}
    }
    fn walk(dir: &Path, notes: &mut Notes) -> Result<(), String> {
        let mut entries: Vec<_> = std::fs::read_dir(dir)
            .map_err(|e| crate::goos::path_error("open", &dir.to_string_lossy(), &e))?
            .map(|e| e.map(|e| e.path()))
            .collect::<Result<_, _>>()
            .map_err(|e| crate::goos::io_text(&e))?;
        entries.sort_by(|a, b| a.file_name().cmp(&b.file_name()));
        for path in entries {
            let meta = std::fs::symlink_metadata(&path)
                .map_err(|e| crate::goos::path_error("lstat", &path.to_string_lossy(), &e))?;
            if meta.is_dir() {
                walk(&path, notes)?;
                continue;
            }
            let name = path
                .file_name()
                .map(|n| n.to_string_lossy().to_lowercase())
                .unwrap_or_default();
            if !name.ends_with(".md") {
                continue;
            }
            let data = std::fs::read(&path)
                .map_err(|e| crate::goos::path_error("open", &path.to_string_lossy(), &e))?;
            let Some(note) = parse_note_frontmatter(&data) else {
                continue;
            };
            let Some(id) = note
                .paperless
                .as_ref()
                .map(|p| p.document_id)
                .filter(|id| *id != 0)
            else {
                continue;
            };
            notes.entry(id).or_default().push(note);
        }
        Ok(())
    }
    walk(Path::new(vault), &mut notes)?;
    Ok(notes)
}

fn created_time(doc: &Document) -> Option<OffsetDateTime> {
    let parse = |d: &FlexDate| {
        d.0.as_deref().and_then(|s| {
            OffsetDateTime::parse(s, &time::format_description::well_known::Rfc3339).ok()
        })
    };
    parse(&doc.created).or_else(|| parse(&doc.created_date))
}

/// Go `t.Format(time.RFC3339)` (no fraction); empty for the zero time.
fn format_date(t: Option<OffsetDateTime>) -> String {
    let Some(t) = t else {
        return String::new();
    };
    let offset = t.offset();
    let zone = if offset.is_utc() {
        "Z".to_owned()
    } else {
        let seconds = offset.whole_seconds();
        let sign = if seconds < 0 { '-' } else { '+' };
        let abs = seconds.abs();
        format!("{sign}{:02}:{:02}", abs / 3600, abs % 3600 / 60)
    };
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}{zone}",
        t.year(),
        u8::from(t.month()),
        t.day(),
        t.hour(),
        t.minute(),
        t.second()
    )
}

fn compare_metadata(doc: &Document, note: &Note, lookups: &Lookups) -> Vec<VerifyMismatch> {
    let mut out = Vec::new();
    let meta = resolve_document_meta(doc, lookups);
    let mismatch = |field: &str, expected: String, got: String| VerifyMismatch {
        document_id: doc.id,
        field: field.to_owned(),
        expected,
        got,
    };
    let mut want = meta.tags.clone();
    let mut got = note.tags.clone();
    want.sort();
    got.sort();
    if want.join(",") != got.join(",") {
        out.push(mismatch("tags", want.join(","), got.join(",")));
    }
    if meta.correspondent != note.correspondent {
        out.push(mismatch(
            "correspondent",
            meta.correspondent.clone(),
            note.correspondent.clone(),
        ));
    }
    if meta.document_type != note.document_type {
        out.push(mismatch(
            "document_type",
            meta.document_type.clone(),
            note.document_type.clone(),
        ));
    }
    let (got_storage, got_created) = note
        .paperless
        .as_ref()
        .map_or((String::new(), None), |p| (p.storage_path.clone(), p.created));
    if meta.storage_path != got_storage {
        out.push(mismatch("storage_path", meta.storage_path.clone(), got_storage));
    }
    let want_created = created_time(doc);
    if want_created != got_created {
        out.push(mismatch(
            "created",
            format_date(want_created),
            format_date(got_created),
        ));
    }
    out
}

struct HashWriter(Sha256);
impl Write for HashWriter {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.0.update(buf);
        Ok(buf.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

fn download_sha256(client: &Client, id: i64) -> Result<String, String> {
    let mut hasher = HashWriter(Sha256::new());
    client.download_document(id, &mut hasher)?;
    Ok(hasher.0.finalize().iter().map(|b| format!("{b:02x}")).collect())
}

fn find_duplicate_content_ids(
    docs: &[Document],
    notes: &Notes,
    store: Option<&Store>,
    opts: &Options,
) -> Vec<i64> {
    let mut seen: BTreeMap<String, Vec<i64>> = BTreeMap::new();
    for doc in docs {
        let mut sha = import_state(store, &opts.base_url, &opts.target_vault, &opts.target_archive, doc.id)
            .ok()
            .flatten()
            .map(|s| s.sha256)
            .unwrap_or_default();
        if sha.is_empty()
            && let Some(first) = notes.get(&doc.id).and_then(|n| n.first())
        {
            sha.clone_from(&first.sha256);
        }
        if !sha.is_empty() {
            seen.entry(sha).or_default().push(doc.id);
        }
    }
    seen.into_values()
        .filter(|ids| ids.len() > 1)
        .flat_map(|ids| ids.into_iter().skip(1))
        .collect()
}

/// `Verify`.
pub fn verify(opts: &Options, vault: &str, store: Option<&Store>) -> Result<VerifyReport, String> {
    let started = OffsetDateTime::now_utc();
    let client = Client::new(opts.base_url.clone(), opts.token.clone());
    let docs = select_documents(&client, opts)?;
    let lookups = load_lookups(&client).map_err(|e| format!("load lookup maps: {e}"))?;
    let notes = scan_vault_notes(vault).map_err(|e| format!("scan vault notes: {e}"))?;

    let mut report = VerifyReport {
        schema_version: REPORT_SCHEMA_VERSION,
        run_id: crate::paperless_run::run_id_at(started),
        tool_version: TOOL_VERSION.to_owned(),
        source: "paperless".to_owned(),
        source_url: opts.base_url.clone(),
        started_at: String::new(),
        finished_at: String::new(),
        duration_seconds: None,
        mode: "verify".to_owned(),
        source_documents: docs.len(),
        vault_notes: notes.values().map(Vec::len).sum(),
        verified: 0,
        deep_verify: opts.deep_verify,
        deep_verified: 0,
        missing: Vec::new(),
        duplicate: Vec::new(),
        duplicate_content: Vec::new(),
        missing_archive: Vec::new(),
        hash_mismatch: Vec::new(),
        source_hash_mismatch: Vec::new(),
        mismatches: Vec::new(),
    };

    for doc in &docs {
        let mut matches: Vec<Note> = notes.get(&doc.id).cloned().unwrap_or_default();
        let mut doc_ok = true;
        match import_state(store, &opts.base_url, &opts.target_vault, &opts.target_archive, doc.id) {
            Err(e) => {
                report.mismatches.push(VerifyMismatch {
                    document_id: doc.id,
                    field: "import_state".to_owned(),
                    expected: "readable".to_owned(),
                    got: e,
                });
                doc_ok = false;
            }
            Ok(Some(state)) => {
                if state.status == "imported" {
                    if matches.is_empty() && file_exists(&state.vault_path) {
                        matches = vec![Note {
                            source_path: state.vault_path.clone(),
                            archive_path: state.archive_path.clone(),
                            sha256: state.sha256.clone(),
                            ..Note::default()
                        }];
                    }
                    if !file_exists(&state.vault_path) {
                        report.missing.push(doc.id);
                        doc_ok = false;
                    }
                    if !state.archive_path.is_empty() && !file_exists(&state.archive_path) {
                        report.missing_archive.push(doc.id);
                        doc_ok = false;
                    }
                    if !state.archive_path.is_empty() && !state.sha256.is_empty() {
                        match file_sha256(&state.archive_path) {
                            Ok(got) if got.eq_ignore_ascii_case(&state.sha256) => {}
                            _ => {
                                report.hash_mismatch.push(doc.id);
                                doc_ok = false;
                            }
                        }
                    }
                } else {
                    report.missing.push(doc.id);
                    doc_ok = false;
                }
            }
            Ok(None) => {}
        }

        if matches.is_empty() {
            if doc_ok {
                report.missing.push(doc.id);
            }
            continue;
        }
        if matches.len() > 1 {
            report.duplicate.push(doc.id);
            doc_ok = false;
        }
        let note = &matches[0];

        if note.archive_path.is_empty() || !file_exists(&note.archive_path) {
            report.missing_archive.push(doc.id);
            doc_ok = false;
        }
        if !note.sha256.is_empty() && !note.archive_path.is_empty() {
            match file_sha256(&note.archive_path) {
                Ok(got) if got.eq_ignore_ascii_case(&note.sha256) => {}
                _ => {
                    report.hash_mismatch.push(doc.id);
                    doc_ok = false;
                }
            }
        }
        if opts.deep_verify && !note.archive_path.is_empty() && file_exists(&note.archive_path) {
            match file_sha256(&note.archive_path) {
                Err(_) => {
                    report.hash_mismatch.push(doc.id);
                    doc_ok = false;
                }
                Ok(local) => match download_sha256(&client, doc.id) {
                    Err(e) => {
                        report.mismatches.push(VerifyMismatch {
                            document_id: doc.id,
                            field: "source_download".to_owned(),
                            expected: "readable".to_owned(),
                            got: e,
                        });
                        doc_ok = false;
                    }
                    Ok(remote) if !local.eq_ignore_ascii_case(&remote) => {
                        report.source_hash_mismatch.push(doc.id);
                        doc_ok = false;
                    }
                    Ok(_) => report.deep_verified += 1,
                },
            }
        }
        let mismatches = compare_metadata(doc, note, &lookups);
        if !mismatches.is_empty() {
            report.mismatches.extend(mismatches);
            doc_ok = false;
        }
        if doc_ok {
            report.verified += 1;
        }
    }

    report.duplicate_content = find_duplicate_content_ids(&docs, &notes, store, opts);
    report.missing.sort_unstable();
    report.duplicate.sort_unstable();
    report.duplicate_content.sort_unstable();
    report.missing_archive.sort_unstable();
    report.hash_mismatch.sort_unstable();
    report.source_hash_mismatch.sort_unstable();
    report.mismatches.sort_by(|a, b| {
        a.document_id
            .cmp(&b.document_id)
            .then_with(|| a.field.cmp(&b.field))
    });

    let finished = OffsetDateTime::now_utc();
    let duration = go_seconds(Some(started), Some(finished));
    report.started_at = go_time(Some(started));
    report.finished_at = go_time(Some(finished));
    report.duration_seconds = (duration != 0.0).then_some(GoFloat(duration));
    Ok(report)
}
