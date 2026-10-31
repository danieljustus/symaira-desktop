//! Paperless import orchestration (`internal/ingest/internal/paperlessimport`
//! `Run`, `importOne`, `selectDocuments`, `loadLookups`, the migration report).
//! Planning/mapping live in [`crate::paperless_import`]; the HTTP client in
//! [`crate::paperless`]; the one-shot pipeline in [`crate::pipeline`].

use crate::paperless::{Client, Document};
use crate::paperless_import::{
    AuditReport, DocumentResult, Lookups, bounded_error_string, build_audit_report,
    download_extension, normalize_content_type, paperless_created, planned_document_result,
    resolve_document_meta, source_uri, stage_from_error,
};
use crate::paperless_state::PaperlessStateWrite;
use crate::pipeline::{IngestOptions, Pipeline, PipelineError, hash_file};
use crate::writer::{NoteLayout, PaperlessMeta, sanitize_storage_path};
use serde::Serialize;
use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;
use time::OffsetDateTime;

/// A line sink standing in for Go's `os.Stderr` / `log` / progress writers.
pub type Sink = Arc<dyn Fn(&str) + Send + Sync>;

/// `ReportSchemaVersion`.
pub const REPORT_SCHEMA_VERSION: i64 = 1;
const CANCELED: &str = "context canceled";

/// `Options`.
#[derive(Clone)]
pub struct Options {
    pub base_url: String,
    pub token: String,
    /// `YYYY-MM-DD` lower bound (`Since`), empty for none.
    pub since: String,
    pub dry_run: bool,
    pub plan: bool,
    pub resume: bool,
    pub deep_verify: bool,
    pub retry_failed: bool,
    pub concurrency: usize,
    pub checkpoint_every: usize,
    pub target_vault: String,
    pub target_archive: String,
    /// Progress writer; `None` writes to `stderr`.
    pub progress: Option<Sink>,
    pub limit: usize,
    pub ids: Vec<i64>,
    pub preserve_storage_paths: bool,
    /// Destination of `os.Stderr` output.
    pub stderr: Sink,
    /// Destination of `log.Printf` output (one call per line, no newline).
    pub log: Sink,
    /// Cooperative cancellation (Go `context`).
    pub cancel: Option<Arc<AtomicBool>>,
}

impl Default for Options {
    fn default() -> Self {
        Self {
            base_url: String::new(),
            token: String::new(),
            since: String::new(),
            dry_run: false,
            plan: false,
            resume: false,
            deep_verify: false,
            retry_failed: false,
            concurrency: 0,
            checkpoint_every: 0,
            target_vault: String::new(),
            target_archive: String::new(),
            progress: None,
            limit: 0,
            ids: Vec::new(),
            preserve_storage_paths: false,
            stderr: Arc::new(|text| eprint!("{text}")),
            log: Arc::new(|line| eprintln!("{line}")),
            cancel: None,
        }
    }
}

impl Options {
    fn canceled(&self) -> bool {
        self.cancel
            .as_ref()
            .is_some_and(|flag| flag.load(Ordering::SeqCst))
    }

    fn progress_sink(&self) -> Sink {
        self.progress.clone().unwrap_or_else(|| self.stderr.clone())
    }
}

/// `Stats`.
#[derive(Clone, Debug, Default)]
pub struct Stats {
    pub imported: usize,
    pub skipped: usize,
    pub failed: usize,
    pub total: usize,
    pub warnings: Vec<String>,
    pub run_id: String,
    pub source: String,
    pub source_url: String,
    pub mode: String,
    pub started_at: Option<OffsetDateTime>,
    pub finished_at: Option<OffsetDateTime>,
    pub selected_ids: Vec<i64>,
    pub results: Vec<DocumentResult>,
    pub audit: Option<AuditReport>,
}

/// What `Run` returns: Go yields stats and an error independently.
#[derive(Clone, Debug, Default)]
pub struct RunOutcome {
    pub stats: Option<Stats>,
    pub error: Option<String>,
}

/// `loadLookups`.
pub fn load_lookups(client: &Client) -> Result<Lookups, String> {
    let mut lookups = Lookups::default();
    type Fetch = fn(&Client) -> Result<Option<Vec<crate::paperless::Entity>>, String>;
    let steps: [(&str, Fetch); 4] = [
        ("list tags", Client::list_tags),
        ("list correspondents", Client::list_correspondents),
        ("list document types", Client::list_document_types),
        ("list storage paths", Client::list_storage_paths),
    ];
    let mut tables: Vec<BTreeMap<i64, String>> = Vec::new();
    for (label, fetch) in steps {
        let entities = fetch(client).map_err(|e| format!("{label}: {e}"))?;
        tables.push(
            entities
                .unwrap_or_default()
                .into_iter()
                .map(|e| (e.id, e.name))
                .collect(),
        );
    }
    lookups.storage_paths = tables.pop().unwrap_or_default();
    lookups.document_types = tables.pop().unwrap_or_default();
    lookups.correspondents = tables.pop().unwrap_or_default();
    lookups.tags = tables.pop().unwrap_or_default();
    Ok(lookups)
}

/// `selectDocuments`.
pub fn select_documents(client: &Client, opts: &Options) -> Result<Vec<Document>, String> {
    if !opts.ids.is_empty() {
        let mut docs = Vec::with_capacity(opts.ids.len());
        for id in &opts.ids {
            docs.push(
                client
                    .get_document(*id)
                    .map_err(|e| format!("get document {id}: {e}"))?,
            );
        }
        return Ok(docs);
    }
    let mut docs = client
        .list_documents(&opts.since, opts.limit, &[])
        .map_err(|e| format!("list documents: {e}"))?
        .unwrap_or_default();
    if opts.limit > 0 && docs.len() > opts.limit {
        docs.truncate(opts.limit);
    }
    Ok(docs)
}

/// An error carrying the pipeline stage it happened in (`stagedError`).
#[derive(Clone, Debug)]
struct Staged {
    stage: String,
    message: String,
}

fn staged(stage: &str, message: String) -> Staged {
    Staged {
        stage: stage.to_owned(),
        message,
    }
}

/// `newRunID`: `paperless-20060102T150405.000000000Z` in UTC.
fn new_run_id(t: OffsetDateTime) -> String {
    format!(
        "paperless-{:04}{:02}{:02}T{:02}{:02}{:02}.{:09}Z",
        t.year(),
        u8::from(t.month()),
        t.day(),
        t.hour(),
        t.minute(),
        t.second(),
        t.nanosecond()
    )
}

/// Go `time.Duration.String()` for a whole number of seconds.
fn go_duration(seconds: u64) -> String {
    let (h, m, s) = (seconds / 3600, seconds % 3600 / 60, seconds % 60);
    if h > 0 {
        format!("{h}h{m}m{s}s")
    } else if m > 0 {
        format!("{m}m{s}s")
    } else {
        format!("{s}s")
    }
}

fn format_eta(start: Instant, processed: usize, total: usize) -> String {
    if processed == 0 || total <= processed {
        return "0s".to_owned();
    }
    let elapsed = start.elapsed().as_secs_f64();
    if elapsed <= 0.0 {
        return "unknown".to_owned();
    }
    let eta = elapsed / processed as f64 * (total - processed) as f64;
    go_duration(eta.round() as u64)
}

fn print_checkpoint(sink: &Sink, stats: &Stats, start: Instant) {
    let processed = stats.imported + stats.skipped + stats.failed;
    let minutes = start.elapsed().as_secs_f64() / 60.0;
    let rate = if minutes > 0.0 {
        processed as f64 / minutes
    } else {
        0.0
    };
    sink(&format!(
        "checkpoint: processed={processed}/{} imported={} skipped={} failed={} rate={rate:.1}/min eta={}\n",
        stats.total,
        stats.imported,
        stats.skipped,
        stats.failed,
        format_eta(start, processed, stats.total)
    ));
}

/// Go `%v` of `map[string]int`.
fn map_v(map: &BTreeMap<String, usize>) -> String {
    let body: Vec<String> = map.iter().map(|(k, v)| format!("{k}:{v}")).collect();
    format!("map[{}]", body.join(" "))
}

/// Go `%v` of `[]int`.
fn ints_v<'a>(ids: impl IntoIterator<Item = &'a i64>) -> String {
    let body: Vec<String> = ids.into_iter().map(ToString::to_string).collect();
    format!("[{}]", body.join(" "))
}

fn print_audit_report(sink: &Sink, r: &AuditReport) {
    sink("\n--- migration audit summary ---\n");
    sink(&format!("total documents: {}\n", r.total_documents));
    sink(&format!("by MIME type: {}\n", map_v(&r.by_mime)));
    sink(&format!(
        "tags: {} distinct, correspondents: {} distinct, document types: {} distinct, storage paths: {} distinct\n",
        r.tag_counts.len(),
        r.correspondent_counts.len(),
        r.document_type_counts.len(),
        r.storage_path_counts.len()
    ));
    if !r.unresolved_tag_ids.is_empty() {
        sink(&format!(
            "unresolved tag IDs: {}\n",
            ints_v(&r.unresolved_tag_ids)
        ));
    }
    if !r.unresolved_correspondent_ids.is_empty() {
        sink(&format!(
            "unresolved correspondent IDs: {}\n",
            ints_v(&r.unresolved_correspondent_ids)
        ));
    }
    if !r.unresolved_document_type_ids.is_empty() {
        sink(&format!(
            "unresolved document type IDs: {}\n",
            ints_v(&r.unresolved_document_type_ids)
        ));
    }
    if !r.unresolved_storage_path_ids.is_empty() {
        sink(&format!(
            "unresolved storage path IDs: {}\n",
            ints_v(&r.unresolved_storage_path_ids)
        ));
    }
    if !r.unsupported_file_types.is_empty() {
        sink(&format!(
            "unsupported file types: {}\n",
            map_v(&r.unsupported_file_types)
        ));
    }
}

fn note_base_name(doc: &Document) -> String {
    if !doc.original_file_name.is_empty() {
        let base = crate::goos::base(&doc.original_file_name);
        let ext = crate::goos::ext(&doc.original_file_name);
        let stem = base.strip_suffix(ext).unwrap_or(&base);
        if !stem.is_empty() {
            return stem.to_owned();
        }
    }
    if !doc.title.is_empty() {
        return doc.title.clone();
    }
    format!("document-{}", doc.id)
}

fn parse_flex(value: &crate::paperless::FlexDate) -> Option<OffsetDateTime> {
    value.0.as_deref().and_then(|s| {
        OffsetDateTime::parse(s, &time::format_description::well_known::Rfc3339).ok()
    })
}

fn temp_path(extension: &str) -> Result<(String, std::fs::File), String> {
    use std::os::unix::fs::OpenOptionsExt;
    static NONCE: AtomicUsize = AtomicUsize::new(0);
    for _ in 0..10_000 {
        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .subsec_nanos() as usize
            ^ NONCE.fetch_add(1, Ordering::Relaxed).wrapping_mul(2_654_435_761);
        let path = std::env::temp_dir().join(format!("symingest-import-{stamp}{extension}"));
        match std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
        {
            Ok(file) => return Ok((path.to_string_lossy().into_owned(), file)),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {}
            Err(e) => return Err(crate::goos::path_error("open", &path.to_string_lossy(), &e)),
        }
    }
    Err("could not allocate a temporary file".to_owned())
}

struct Removal<'a> {
    path: String,
    log: &'a Sink,
}

impl Drop for Removal<'_> {
    fn drop(&mut self) {
        if let Err(e) = std::fs::remove_file(&self.path)
            && e.kind() != std::io::ErrorKind::NotFound
        {
            (self.log)(&format!(
                "remove temporary import file {}: {}",
                go_quote(&self.path),
                crate::goos::io_text(&e)
            ));
        }
    }
}

use symdesk_vault::go_quote;

struct OneOutcome {
    result: DocumentResult,
    warnings: Vec<String>,
    error: Option<Staged>,
}

/// `importOne`.
fn import_one(
    client: &Client,
    doc: &Document,
    lookups: &Lookups,
    pipeline: &Pipeline,
    preserve_storage_paths: bool,
    run_id: &str,
    log: &Sink,
) -> OneOutcome {
    let mut result = planned_document_result(doc, run_id, "");
    let mut warnings: Vec<String> = Vec::new();
    macro_rules! fail {
        ($stage:expr, $msg:expr) => {
            return OneOutcome {
                result,
                warnings,
                error: Some(staged($stage, $msg)),
            }
        };
    }

    let (tmp_name, mut tmp_file) = match temp_path(".tmp") {
        Ok(v) => v,
        Err(e) => fail!("download", format!("create temp file: {e}")),
    };
    let tmp_guard = Removal {
        path: tmp_name.clone(),
        log,
    };
    let metadata = match client.download_document(doc.id, &mut tmp_file) {
        Ok(m) => m,
        Err(e) => {
            drop(tmp_file);
            fail!("download", format!("download document: {e}"))
        }
    };
    if let Err(e) = std::io::Write::flush(&mut tmp_file) {
        fail!(
            "download",
            format!("close temp file: {}", crate::goos::io_text(&e))
        );
    }
    drop(tmp_file);
    let content_type = normalize_content_type(&metadata.content_type);
    if !content_type.is_empty() {
        result.mime = content_type;
    }

    let mut ext = download_extension(doc, &metadata);
    if ext.is_empty() {
        ext = ".bin".to_owned();
    }
    result.expected_extension.clone_from(&ext);
    let final_path = format!("{tmp_name}{ext}");
    if let Err(e) = std::fs::rename(&tmp_name, &final_path) {
        fail!(
            "download",
            format!(
                "rename temp file: rename {tmp_name} {final_path}: {}",
                crate::goos::io_text(&e)
            )
        );
    }
    std::mem::forget(tmp_guard);
    let _final_guard = Removal {
        path: final_path.clone(),
        log,
    };

    result.sha256 = match hash_file(&final_path) {
        Ok(h) => h,
        Err(e) => fail!("download", format!("hash downloaded file: {e}")),
    };

    let meta = resolve_document_meta(doc, lookups);
    let meta_warnings = meta.warnings.clone().unwrap_or_default();
    warnings.extend(meta_warnings.iter().cloned());
    result.warnings.extend(meta_warnings);

    let mut base_name = note_base_name(doc);
    if !preserve_storage_paths {
        base_name = format!("paperless-{}-{base_name}", doc.id);
    }
    let mut layout = NoteLayout {
        subdir: String::new(),
        base_name,
    };
    if preserve_storage_paths {
        layout.subdir = sanitize_storage_path(&meta.storage_path);
    }
    let options = IngestOptions {
        preset_category: meta.document_type.clone(),
        preset_tags: meta.tags.clone(),
        preset_correspondent: meta.correspondent.clone(),
        preset_document_type: meta.document_type.clone(),
        layout: Some(layout),
        source_path_override: result.download_uri.clone(),
        imported_from: "paperless".to_owned(),
        import_run_id: run_id.to_owned(),
        source_uri: result.source_uri.clone(),
        download_uri: result.download_uri.clone(),
        allow_duplicate_content: true,
        paperless: Some(PaperlessMeta {
            document_id: doc.id,
            title: doc.title.clone(),
            created: parse_flex(&doc.created).or_else(|| parse_flex(&doc.created_date)),
            added: parse_flex(&doc.added),
            modified: parse_flex(&doc.modified),
            storage_path: meta.storage_path.clone(),
            original_file_name: doc.original_file_name.clone(),
            archived_file_name: doc.archived_file_name.clone(),
            page_count: doc.page_count,
            url: client.document_url(doc.id),
        }),
        ..IngestOptions::default()
    };
    let _ = paperless_created(doc);

    match pipeline.ingest(&final_path, Some(&options)) {
        Err(PipelineError::Duplicate(dup)) => {
            log(&format!("  skipped (duplicate): {}", doc.title));
            result.status = "skipped".to_owned();
            result.reason = "duplicate content; note already present in the vault".to_owned();
            result.vault_path.clone_from(&dup.vault_path);
            result.archive_path.clone_from(&dup.archive_path);
            result.actual_archive_extension = crate::goos::ext(&dup.archive_path).to_owned();
            OneOutcome {
                result,
                warnings,
                error: None,
            }
        }
        Err(e) => {
            let message = e.to_string();
            let stage = stage_from_error(&message, None);
            fail!(&stage, format!("ingest: {message}"))
        }
        Ok(res) => {
            log(&format!("  imported: {} \u{2192} vault", doc.title));
            result.status = "imported".to_owned();
            result.vault_path.clone_from(&res.vault_path);
            result.archive_path.clone_from(&res.archive_path);
            result.sha256.clone_from(&res.sha256);
            if result.mime.is_empty()
                && let Some(extract) = &res.extract
            {
                result.mime.clone_from(&extract.mime);
            }
            result.actual_archive_extension = crate::goos::ext(&res.archive_path).to_owned();
            OneOutcome {
                result,
                warnings,
                error: None,
            }
        }
    }
}

fn import_targets(opts: &Options, pipeline: &Pipeline) -> (String, String) {
    let vault = if opts.target_vault.is_empty() {
        pipeline.writer.vault.clone()
    } else {
        opts.target_vault.clone()
    };
    let archive = if opts.target_archive.is_empty() {
        pipeline.archive_dir.clone()
    } else {
        opts.target_archive.clone()
    };
    (vault, archive)
}

fn created_day(doc: &Document) -> String {
    doc.created_date
        .0
        .as_deref()
        .map_or_else(|| "0001-01-01".to_owned(), |s| s[..10.min(s.len())].to_owned())
}

struct Shared {
    stats: Stats,
    results: Vec<DocumentResult>,
    recorded: Vec<bool>,
}

/// `Run`.
pub fn run(opts: &Options, pipeline: &Pipeline) -> RunOutcome {
    let client = Client::new(opts.base_url.clone(), opts.token.clone());
    let started = OffsetDateTime::now_utc();
    let started_instant = Instant::now();

    let docs = match select_documents(&client, opts) {
        Ok(d) => d,
        Err(e) => {
            return RunOutcome {
                stats: None,
                error: Some(e),
            };
        }
    };
    let lookups = match load_lookups(&client) {
        Ok(l) => l,
        Err(e) => {
            return RunOutcome {
                stats: None,
                error: Some(format!("load lookup maps: {e}")),
            };
        }
    };

    let mode = if opts.plan {
        "plan"
    } else if opts.dry_run {
        "dry-run"
    } else if opts.retry_failed {
        "retry-failed"
    } else if opts.resume {
        "resume"
    } else {
        "import"
    };
    let mut stats = Stats {
        total: docs.len(),
        run_id: new_run_id(started),
        source: "paperless".to_owned(),
        source_url: opts.base_url.clone(),
        mode: mode.to_owned(),
        started_at: Some(started),
        selected_ids: docs.iter().map(|d| d.id).collect(),
        ..Stats::default()
    };

    if opts.plan || opts.dry_run {
        let (status, verb) = if opts.plan {
            ("planned", "planned")
        } else {
            ("would-import", "would import")
        };
        for (i, doc) in docs.iter().enumerate() {
            if opts.canceled() {
                stats.finished_at = Some(OffsetDateTime::now_utc());
                return RunOutcome {
                    stats: Some(stats),
                    error: Some(CANCELED.to_owned()),
                };
            }
            (opts.stderr)(&format!(
                "[{}/{}] {verb}: {} (created: {})\n",
                i + 1,
                stats.total,
                doc.title,
                created_day(doc)
            ));
            stats.skipped += 1;
            stats
                .results
                .push(planned_document_result(doc, &stats.run_id, status));
        }
        let audit = build_audit_report(&docs, &lookups);
        print_audit_report(&opts.stderr, &audit);
        stats.audit = Some(audit);
        stats.finished_at = Some(OffsetDateTime::now_utc());
        return RunOutcome {
            stats: Some(stats),
            error: None,
        };
    }

    let progress = opts.progress_sink();
    let (target_vault, target_archive) = import_targets(opts, pipeline);
    let concurrency = opts.concurrency.max(1);
    let run_id = stats.run_id.clone();
    let shared = Mutex::new(Shared {
        results: vec![DocumentResult::default(); docs.len()],
        recorded: vec![false; docs.len()],
        stats,
    });
    let progress_lock = Mutex::new(());
    let first_error: Mutex<Option<String>> = Mutex::new(None);
    let next = AtomicUsize::new(0);

    let record = |i: usize, result: DocumentResult, warnings: Vec<String>, outcome: &str| {
        let mut guard = shared.lock().unwrap_or_else(|e| e.into_inner());
        guard.stats.warnings.extend(warnings);
        match outcome {
            "imported" => guard.stats.imported += 1,
            "skipped" => guard.stats.skipped += 1,
            "failed" => guard.stats.failed += 1,
            _ => {}
        }
        guard.results[i] = result;
        guard.recorded[i] = true;
        let processed = guard.stats.imported + guard.stats.skipped + guard.stats.failed;
        if opts.checkpoint_every > 0 && processed % opts.checkpoint_every == 0 {
            let _p = progress_lock.lock().unwrap_or_else(|e| e.into_inner());
            print_checkpoint(&progress, &guard.stats, started_instant);
        }
    };
    let say = |text: String| {
        let _p = progress_lock.lock().unwrap_or_else(|e| e.into_inner());
        progress(&text);
    };
    let add_warning = |text: String| {
        shared
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .stats
            .warnings
            .push(text);
    };
    let total = docs.len();

    // `processDoc`: Err stops the run (cancellation).
    let process = |i: usize| -> Result<(), String> {
        if opts.canceled() {
            return Err(CANCELED.to_owned());
        }
        let doc = &docs[i];
        say(format!("[{}/{}] {}\n", i + 1, total, doc.title));

        let state_result = pipeline
            .store
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .paperless_import_state_for_target(
                &opts.base_url,
                &target_vault,
                &target_archive,
                doc.id,
            );
        let (state, state_error) = match state_result {
            Ok(state) => (Some(state), None),
            Err(e) if e == "sql: no rows in result set" => (None, None),
            Err(e) => (None, Some(e)),
        };
        let found = state.is_some();
        let status = state.as_ref().map_or("", |s| s.status.as_str()).to_owned();
        if let Some(e) = &state_error {
            add_warning(format!("document {}: read import state: {e}", doc.id));
        }
        if state_error.is_none() && found && status == "imported" {
            say("  skipped (already imported in a previous run)\n".to_owned());
            let mut result = planned_document_result(doc, &run_id, "skipped");
            "already imported in a previous run".clone_into(&mut result.reason);
            if let Some(state) = &state {
                result.vault_path.clone_from(&state.vault_path);
                result.archive_path.clone_from(&state.archive_path);
                result.sha256.clone_from(&state.sha256);
                result.actual_archive_extension = crate::goos::ext(&state.archive_path).to_owned();
            }
            record(i, result, Vec::new(), "skipped");
            return if opts.canceled() {
                Err(CANCELED.to_owned())
            } else {
                Ok(())
            };
        }
        if opts.retry_failed && (!found || status != "failed") {
            let mut result = planned_document_result(doc, &run_id, "skipped");
            "not recorded as failed for this target".clone_into(&mut result.reason);
            record(i, result, Vec::new(), "skipped");
            return if opts.canceled() {
                Err(CANCELED.to_owned())
            } else {
                Ok(())
            };
        }

        let OneOutcome {
            mut result,
            mut warnings,
            error,
        } = import_one(
            &client,
            doc,
            &lookups,
            pipeline,
            opts.preserve_storage_paths,
            &run_id,
            &opts.log,
        );
        let write = |status: &str, last_error: &str, result: &DocumentResult| {
            pipeline
                .store
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .upsert_paperless_import_state_for_target(&PaperlessStateWrite {
                    base_url: &opts.base_url,
                    target_vault: &target_vault,
                    target_archive: &target_archive,
                    document_id: doc.id,
                    status,
                    last_error,
                    vault_path: &result.vault_path,
                    archive_path: &result.archive_path,
                    sha256: &result.sha256,
                })
        };
        if let Some(error) = error {
            if opts.canceled() {
                return Err(CANCELED.to_owned());
            }
            say(format!("  failed: {}\n", error.message));
            let reason = bounded_error_string(&error.message);
            "failed".clone_into(&mut result.status);
            result.error.clone_from(&reason);
            if result.stage.is_empty() {
                result.stage = if error.stage.is_empty() {
                    stage_from_error(&error.message, None)
                } else {
                    error.stage.clone()
                };
            }
            if let Err(e) = write("failed", &reason, &result) {
                warnings.push(format!("document {}: record import state: {e}", doc.id));
            }
            record(i, result, warnings, "failed");
            return Ok(());
        }
        if let Err(e) = write("imported", "", &result) {
            warnings.push(format!("document {}: record import state: {e}", doc.id));
        }
        let outcome = result.status.clone();
        record(i, result, warnings, &outcome);
        if opts.canceled() {
            Err(CANCELED.to_owned())
        } else {
            Ok(())
        }
    };

    let mut failure: Option<String> = None;
    if concurrency == 1 || docs.len() <= 1 {
        for i in 0..docs.len() {
            if let Err(e) = process(i) {
                failure = Some(e);
                break;
            }
        }
    } else {
        let workers = concurrency.min(docs.len());
        std::thread::scope(|scope| {
            for _ in 0..workers {
                scope.spawn(|| {
                    loop {
                        let i = next.fetch_add(1, Ordering::SeqCst);
                        if i >= docs.len() {
                            return;
                        }
                        let outcome = if opts.canceled() {
                            Err(CANCELED.to_owned())
                        } else {
                            process(i)
                        };
                        if let Err(e) = outcome {
                            first_error
                                .lock()
                                .unwrap_or_else(|e| e.into_inner())
                                .get_or_insert(e);
                        }
                    }
                });
            }
        });
        failure = first_error.into_inner().unwrap_or_else(|e| e.into_inner());
    }

    let mut shared = shared.into_inner().unwrap_or_else(|e| e.into_inner());
    if failure.is_some() {
        shared.stats.finished_at = Some(OffsetDateTime::now_utc());
        return RunOutcome {
            stats: Some(shared.stats),
            error: failure,
        };
    }
    for i in 0..docs.len() {
        if shared.recorded[i] {
            let result = std::mem::take(&mut shared.results[i]);
            shared.stats.results.push(result);
        }
    }
    if opts.checkpoint_every > 0 && shared.stats.total > 0 {
        print_checkpoint(&progress, &shared.stats, started_instant);
    }
    shared.stats.finished_at = Some(OffsetDateTime::now_utc());
    RunOutcome {
        stats: Some(shared.stats),
        error: None,
    }
}

/// `MigrationReport`.
#[derive(Clone, Debug, Serialize)]
pub struct MigrationReport {
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
    pub duration_seconds: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mode: String,
    pub dry_run: bool,
    pub total: usize,
    pub imported: usize,
    pub skipped: usize,
    pub failed: usize,
    pub documents: Vec<DocumentResult>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub unsupported_file_types: BTreeMap<String, usize>,
    #[serde(skip_serializing_if = "std::collections::BTreeSet::is_empty")]
    pub unresolved_tag_ids: std::collections::BTreeSet<i64>,
    #[serde(skip_serializing_if = "std::collections::BTreeSet::is_empty")]
    pub unresolved_correspondent_ids: std::collections::BTreeSet<i64>,
    #[serde(skip_serializing_if = "std::collections::BTreeSet::is_empty")]
    pub unresolved_document_type_ids: std::collections::BTreeSet<i64>,
    #[serde(skip_serializing_if = "std::collections::BTreeSet::is_empty")]
    pub unresolved_storage_path_ids: std::collections::BTreeSet<i64>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub by_expected_extension: BTreeMap<String, usize>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub required_tools: Vec<String>,
}

fn rfc3339_nano(t: Option<OffsetDateTime>) -> String {
    let Some(t) = t else {
        return "0001-01-01T00:00:00Z".to_owned();
    };
    let base = format!(
        "{:04}-{:02}-{:02}T{:02}{}{:02}{}{:02}",
        t.year(),
        u8::from(t.month()),
        t.day(),
        t.hour(),
        ":",
        t.minute(),
        ":",
        t.second()
    );
    let nanos = format!("{:09}", t.nanosecond());
    let nanos = nanos.trim_end_matches('0');
    if nanos.is_empty() {
        format!("{base}Z")
    } else {
        format!("{base}.{nanos}Z")
    }
}

impl Stats {
    /// `BuildMigrationReport`.
    #[must_use]
    pub fn build_migration_report(&self, dry_run: bool) -> MigrationReport {
        let duration = match (self.started_at, self.finished_at) {
            (Some(a), Some(b)) => (b - a).as_seconds_f64(),
            _ => 0.0,
        };
        let mut report = MigrationReport {
            schema_version: REPORT_SCHEMA_VERSION,
            run_id: self.run_id.clone(),
            tool_version: TOOL_VERSION.to_owned(),
            source: self.source.clone(),
            source_url: self.source_url.clone(),
            started_at: rfc3339_nano(self.started_at),
            finished_at: rfc3339_nano(self.finished_at),
            duration_seconds: duration,
            mode: self.mode.clone(),
            dry_run,
            total: self.total,
            imported: self.imported,
            skipped: self.skipped,
            failed: self.failed,
            documents: self.results.clone(),
            warnings: self.warnings.clone(),
            unsupported_file_types: BTreeMap::new(),
            unresolved_tag_ids: Default::default(),
            unresolved_correspondent_ids: Default::default(),
            unresolved_document_type_ids: Default::default(),
            unresolved_storage_path_ids: Default::default(),
            by_expected_extension: BTreeMap::new(),
            required_tools: Vec::new(),
        };
        if let Some(audit) = &self.audit {
            report.unsupported_file_types = audit.unsupported_file_types.clone();
            report.unresolved_tag_ids = audit.unresolved_tag_ids.clone();
            report.unresolved_correspondent_ids = audit.unresolved_correspondent_ids.clone();
            report.unresolved_document_type_ids = audit.unresolved_document_type_ids.clone();
            report.unresolved_storage_path_ids = audit.unresolved_storage_path_ids.clone();
            report.by_expected_extension = audit.by_expected_extension.clone();
            report.required_tools = audit.required_tools.clone();
        }
        report
    }
}

/// Version string Go embeds (`version.Version`).
pub const TOOL_VERSION: &str = "0.12.0";

/// `WriteMigrationReport`: indented JSON plus a trailing newline, mode 0600.
pub fn write_migration_report(path: &str, report: &MigrationReport) -> Result<(), String> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut data = serde_json::to_vec_pretty(report)
        .map_err(|e| format!("marshal migration report: {e}"))?;
    data.push(b'\n');
    let write = || -> std::io::Result<()> {
        use std::io::Write;
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(path)?;
        file.write_all(&data)
    };
    write().map_err(|e| {
        format!(
            "write migration report: {}",
            crate::goos::path_error("open", path, &e)
        )
    })
}

/// Source URI helper re-exported for callers building their own results.
#[must_use]
pub fn document_source_uri(id: i64) -> String {
    source_uri(id)
}
