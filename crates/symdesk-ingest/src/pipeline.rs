//! The one-shot ingestion pipeline (`internal/ingest/internal/ingest`):
//! detect -> hash -> record -> queue -> extract (kind routing) -> archive ->
//! classify -> write note -> annotate -> store paths, plus reprocessing.

use crate::annotate;
use crate::classification::{Presets, classify};
use crate::extract::{self, Extraction};
use crate::goos;
use crate::ocr::{Context, Runner};
use crate::store_state::Extraction as StoredExtraction;
use crate::writer::{self, Note, NoteInput, NoteLayout, NoteWriter, PaperlessMeta};
use crate::{DocumentPaths, Job, Store};
use sha2::{Digest, Sha256};
use std::fmt;
use std::io::Read;
use std::sync::{Arc, Mutex};
use symdesk_vault::go_quote;
use time::OffsetDateTime;

/// Stable queue kind used by the `reocr` command.
pub const REPROCESS_JOB_KIND: &str = "reocr";

/// Extraction engine for kinds the pipeline cannot read itself
/// (`extract.Engine`).
pub trait Engine: Send + Sync {
    fn extract(&self, ctx: &Context, path: &str, kind: &str) -> Result<Extraction, String>;
    /// Copy using another OCR language (`WithLanguage`); `None` keeps the engine.
    fn with_language(&self, _lang: &str) -> Option<Arc<dyn Engine>> {
        None
    }
}

impl Engine for Runner {
    fn extract(&self, ctx: &Context, path: &str, kind: &str) -> Result<Extraction, String> {
        Runner::extract(self, ctx, path, kind).map(|r| Extraction {
            text: r.text,
            mime: r.mime,
            engine: r.engine,
            language: r.language,
        })
    }

    fn with_language(&self, lang: &str) -> Option<Arc<dyn Engine>> {
        Some(Arc::new(Runner::with_language(self, lang)))
    }
}

#[cfg(unix)]
impl Engine for crate::vlm::VlmRunner {
    fn extract(&self, ctx: &Context, path: &str, kind: &str) -> Result<Extraction, String> {
        crate::vlm::VlmRunner::extract(self, ctx, path, kind).map(|r| Extraction {
            text: r.text,
            mime: r.mime,
            engine: r.engine,
            language: r.language,
        })
    }
}

/// Preset metadata and import traceability (`IngestOptions`).
#[derive(Clone, Debug, Default)]
pub struct IngestOptions {
    pub preset_category: String,
    pub preset_tags: Vec<String>,
    pub preset_correspondent: String,
    pub preset_document_type: String,
    pub source_path_override: String,
    pub imported_from: String,
    pub import_run_id: String,
    pub source_uri: String,
    pub download_uri: String,
    pub paperless: Option<PaperlessMeta>,
    pub layout: Option<NoteLayout>,
    pub allow_duplicate_content: bool,
    pub existing_vault_path: String,
    pub archive_path_override: String,
    pub ocr_language: String,
}

/// Outcome of a one-shot ingest (`Result`).
#[derive(Clone, Debug, Default)]
pub struct IngestResult {
    pub source_path: String,
    pub sha256: String,
    pub kind: String,
    pub extract: Option<Extraction>,
    pub vault_path: String,
    pub archive_path: String,
    pub category: String,
    pub tags: Option<Vec<String>>,
    pub correspondent: String,
    pub document_type: String,
    pub sidecar_path: String,
    pub extraction_count: usize,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DuplicateError {
    pub source_path: String,
    pub vault_path: String,
    pub archive_path: String,
    pub vault_root: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PipelineError {
    /// `ErrDuplicate` via `*DuplicateError`.
    Duplicate(DuplicateError),
    /// `ErrNoArchivedOriginal` wrapped with its message.
    NoArchivedOriginal(String),
    Other(String),
}

impl fmt::Display for PipelineError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            PipelineError::Duplicate(e) => {
                let vault = if e.vault_root.is_empty() {
                    &e.vault_path
                } else {
                    &e.vault_root
                };
                write!(
                    f,
                    "source already ingested: {} (vault: {vault}, note: {}, archive: {})",
                    e.source_path, e.vault_path, e.archive_path
                )
            }
            PipelineError::NoArchivedOriginal(m) | PipelineError::Other(m) => f.write_str(m),
        }
    }
}

impl From<String> for PipelineError {
    fn from(m: String) -> Self {
        PipelineError::Other(m)
    }
}

/// Outcome of [`Pipeline::reprocess`].
#[derive(Clone, Debug)]
pub struct ReprocessResult {
    pub result: Option<IngestResult>,
    pub job: Job,
    pub already_running: bool,
}

type IndexHook = Box<dyn Fn(&str) -> Result<(), String> + Send + Sync>;
type ExtractHook =
    Box<dyn Fn(&IngestResult, &[StoredExtraction]) -> Result<(), String> + Send + Sync>;

/// Orchestrates extraction, persistence and Markdown output.
pub struct Pipeline {
    pub engine: Option<Arc<dyn Engine>>,
    pub store: Arc<Mutex<Store>>,
    pub writer: NoteWriter,
    pub archive_dir: String,
    pub vault_root: String,
    pub processed_dir: String,
    pub failed_dir: String,
    pub post_index: Option<IndexHook>,
    pub post_extract: Option<ExtractHook>,
    pub extraction_profile: String,
    /// Destination of Go's `log.Printf` diagnostics.
    pub log: Box<dyn Fn(&str) + Send + Sync>,
}

struct Extracted {
    res: Extraction,
    /// Raw bytes of a text source when they are not valid UTF-8.
    raw: Option<Vec<u8>>,
}

impl Pipeline {
    #[must_use]
    pub fn new(store: Arc<Mutex<Store>>, writer: NoteWriter) -> Self {
        Self {
            engine: None,
            store,
            writer,
            archive_dir: String::new(),
            vault_root: String::new(),
            processed_dir: String::new(),
            failed_dir: String::new(),
            post_index: None,
            post_extract: None,
            extraction_profile: String::new(),
            log: Box::new(|line| eprintln!("{line}")),
        }
    }

    fn logf(&self, line: String) {
        (self.log)(&line);
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, Store> {
        self.store.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// Archive path recorded for a physical archive: vault-relative when the
    /// archive lives inside the vault, otherwise absolute.
    #[must_use]
    pub fn vault_relative_archive(&self, physical: &str) -> String {
        if self.vault_root.is_empty() {
            return physical.to_owned();
        }
        let Ok(root) = goos::abs(&self.vault_root) else {
            return physical.to_owned();
        };
        let Ok(abs_archive) = goos::abs(physical) else {
            return physical.to_owned();
        };
        let Some(rel) = goos::rel(&root, &abs_archive) else {
            return abs_archive;
        };
        if rel == ".." || rel.starts_with("../") || rel.starts_with('/') {
            return abs_archive;
        }
        rel
    }

    /// The stored archive path back to a path that can be opened.
    #[must_use]
    pub fn resolve_archive_path(&self, stored: &str) -> String {
        if stored.is_empty() || stored.starts_with('/') || self.vault_root.is_empty() {
            return stored.to_owned();
        }
        goos::join(&[&self.vault_root, stored])
    }

    /// Stable identity used for deduplication.
    #[must_use]
    pub fn destination_vault_root(&self) -> String {
        let root = if self.vault_root.is_empty() {
            self.writer.vault.clone()
        } else {
            self.vault_root.clone()
        };
        if root.is_empty() {
            return String::new();
        }
        goos::abs(&root).unwrap_or_else(|_| crate::ocr::go_clean(&root))
    }

    /// Validates an archived original, queues one reprocessing job and runs it.
    pub fn reprocess(
        &self,
        document_id: i64,
        source: &str,
        opts: Option<&IngestOptions>,
    ) -> Result<ReprocessResult, PipelineError> {
        let doc = self
            .lock()
            .by_id(document_id)
            .map_err(|e| format!("get document: {e}"))?;
        let archive = doc.archive_path.clone().unwrap_or_default();
        if archive.is_empty() {
            return Err(PipelineError::NoArchivedOriginal(format!(
                "archived original not recorded: document {document_id}"
            )));
        }
        let vault_path = doc.vault_path.clone().unwrap_or_default();
        if vault_path.is_empty() {
            return Err(format!("output note is not recorded for document {document_id}").into());
        }
        let source = if source.is_empty() {
            self.resolve_archive_path(&archive)
        } else {
            source.to_owned()
        };
        let meta = std::fs::metadata(&source).map_err(|e| {
            format!(
                "stat archived source: {}",
                goos::path_error("stat", &source, &e)
            )
        })?;
        if meta.is_dir() {
            return Err(format!("archived source is a directory: {source}").into());
        }
        let hash = hash_file(&source).map_err(|e| format!("hash archived source: {e}"))?;
        if hash != doc.sha256 {
            return Err(format!("archived source hash mismatch for document {document_id}").into());
        }
        extract::detect_file(&source).map_err(|e| format!("detect archived source type: {e}"))?;

        let (job, created) = self
            .lock()
            .enqueue_reprocess_job(document_id)
            .map_err(|e| format!("enqueue reprocess job: {e}"))?;
        if !created {
            return Ok(ReprocessResult {
                result: None,
                job,
                already_running: true,
            });
        }
        let claimed = self
            .lock()
            .claim_job_by_id(job.id)
            .map_err(|e| format!("claim reprocess job: {e}"))?
            .ok_or_else(|| format!("reprocess job {} is no longer available", job.id))?;

        let mut opts = opts.cloned().unwrap_or_default();
        opts.source_path_override.clone_from(&doc.source_path);
        opts.existing_vault_path.clone_from(&vault_path);
        opts.archive_path_override.clone_from(&archive);
        let lang = writer::read_ocr_language(&self.resolve_archive_path(&vault_path));
        if !lang.is_empty() {
            opts.ocr_language = lang;
        }
        let res = match self.process_job(&claimed, Some(&opts)) {
            Ok(r) => r,
            Err(e) => {
                if let Err(fail) = self.lock().fail_job(claimed.id, &e.to_string()) {
                    return Err(format!(
                        "reprocess failed: {e} (failed to mark job as failed: {fail})"
                    )
                    .into());
                }
                return Err(e);
            }
        };
        self.lock()
            .set_vault_and_archive_path(
                document_id,
                &DocumentPaths {
                    vault_path: &res.vault_path,
                    archive_path: &res.archive_path,
                    category: &res.category,
                    tags: res.tags.as_deref().unwrap_or(&[]),
                    correspondent: &res.correspondent,
                    document_type: &res.document_type,
                },
            )
            .map_err(|e| format!("set reprocessed document paths: {e}"))?;
        self.lock()
            .complete_job(claimed.id)
            .map_err(|e| format!("complete reprocess job: {e}"))?;
        Ok(ReprocessResult {
            result: Some(res),
            job: claimed,
            already_running: false,
        })
    }

    /// Processes one source through the whole pipeline.
    pub fn ingest(
        &self,
        source: &str,
        opts: Option<&IngestOptions>,
    ) -> Result<IngestResult, PipelineError> {
        let meta = std::fs::metadata(source)
            .map_err(|e| format!("stat source: {}", goos::path_error("stat", source, &e)))?;
        if meta.is_dir() {
            return Err(format!("source is a directory: {source}").into());
        }
        let kind = extract::detect_file(source).map_err(|e| format!("detect source type: {e}"))?;
        let hash = hash_file(source).map_err(|e| format!("hash source: {e}"))?;
        let (doc, created) = self
            .lock()
            .create_or_get(source, &hash, &kind, &self.destination_vault_root())
            .map_err(|e| format!("record document: {e}"))?;
        if !created {
            if opts.is_some_and(|o| o.allow_duplicate_content) {
                return self
                    .process_source(source, &hash, &kind, opts)
                    .map_err(Into::into);
            }
            self.lock()
                .enqueue_skipped_job(doc.id, &kind, "duplicate")
                .map_err(|e| format!("enqueue skipped job: {e}"))?;
            return Err(PipelineError::Duplicate(DuplicateError {
                source_path: source.to_owned(),
                vault_path: doc.vault_path.unwrap_or_default(),
                archive_path: doc.archive_path.unwrap_or_default(),
                vault_root: doc.vault_root,
            }));
        }
        let enqueued = self
            .lock()
            .enqueue_job(doc.id, &kind)
            .map_err(|e| format!("enqueue job: {e}"))?;
        let claimed = self
            .lock()
            .claim_job_by_id(enqueued.id)
            .map_err(|e| format!("claim job: {e}"))?
            .ok_or_else(|| "failed to claim enqueued job immediately".to_owned())?;
        let res = match self.process_job(&claimed, opts) {
            Ok(r) => r,
            Err(e) => {
                if let Err(fail) = self.lock().fail_job(claimed.id, &e.to_string()) {
                    return Err(format!(
                        "process job failed: {e} (failed to mark job as failed: {fail})"
                    )
                    .into());
                }
                return Err(e);
            }
        };
        self.lock()
            .set_vault_and_archive_path(
                doc.id,
                &DocumentPaths {
                    vault_path: &res.vault_path,
                    archive_path: &res.archive_path,
                    category: &res.category,
                    tags: res.tags.as_deref().unwrap_or(&[]),
                    correspondent: &res.correspondent,
                    document_type: &res.document_type,
                },
            )
            .map_err(|e| format!("set vault path: {e}"))?;
        self.lock()
            .complete_job(claimed.id)
            .map_err(|e| format!("complete job: {e}"))?;
        Ok(res)
    }

    /// Text extraction and note writing for a claimed job.
    pub fn process_job(
        &self,
        job: &Job,
        opts: Option<&IngestOptions>,
    ) -> Result<IngestResult, PipelineError> {
        let doc = self
            .lock()
            .by_id(job.document_id)
            .map_err(|e| format!("get document: {e}"))?;
        if job.kind == REPROCESS_JOB_KIND {
            let archive = doc.archive_path.clone().unwrap_or_default();
            if archive.is_empty() {
                return Err(
                    format!("archived source is not recorded for document {}", doc.id).into(),
                );
            }
            let archive_source = self.resolve_archive_path(&archive);
            let kind = extract::detect_file(&archive_source)
                .map_err(|e| format!("detect archived source type: {e}"))?;
            let mut o = opts.cloned().unwrap_or_default();
            o.source_path_override.clone_from(&doc.source_path);
            o.archive_path_override = archive;
            if let Some(v) = &doc.vault_path {
                o.existing_vault_path.clone_from(v);
            }
            return self
                .process_source(&archive_source, &doc.sha256, &kind, Some(&o))
                .map_err(Into::into);
        }
        self.process_source(&doc.source_path, &doc.sha256, &job.kind, opts)
            .map_err(Into::into)
    }

    fn engine_for(&self, opts: Option<&IngestOptions>) -> Option<Arc<dyn Engine>> {
        let engine = self.engine.as_ref()?;
        match opts {
            Some(o) if !o.ocr_language.is_empty() => engine
                .with_language(&o.ocr_language)
                .or_else(|| Some(Arc::clone(engine))),
            _ => Some(Arc::clone(engine)),
        }
    }

    fn extract_text(
        &self,
        source: &str,
        kind: &str,
        engine: Option<Arc<dyn Engine>>,
    ) -> Result<Extracted, String> {
        if symdesk_core::document_format::UNSUPPORTED_FORMATS
            .iter()
            .any(|f| f.format.kind == kind)
        {
            return Err(symdesk_core::document_format::unsupported_format_error(
                kind,
            ));
        }
        let wrap = |e: String| format!("extract text: {e}");
        match kind {
            "text/plain" | "text/markdown" | "text/csv" => {
                let data = std::fs::read(source).map_err(|e| {
                    wrap(format!(
                        "read text file: {}",
                        goos::path_error("open", source, &e)
                    ))
                })?;
                let text = symdesk_core::gofmt::lossy(&data);
                let raw = (text.as_bytes() != data.as_slice()).then_some(data);
                Ok(Extracted {
                    res: Extraction {
                        text,
                        mime: kind.to_owned(),
                        engine: "text".into(),
                        language: String::new(),
                    },
                    raw,
                })
            }
            "text/html"
            | "application/rtf"
            | "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
            | "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            | "application/vnd.openxmlformats-officedocument.presentationml.presentation"
            | "application/vnd.oasis.opendocument.text"
            | "application/vnd.oasis.opendocument.spreadsheet"
            | "application/vnd.oasis.opendocument.presentation"
            | "application/epub+zip"
            | "message/rfc822" => {
                let data = std::fs::read(source).map_err(|e| {
                    wrap(format!(
                        "read file: {}",
                        goos::path_error("open", source, &e)
                    ))
                })?;
                let res = extract::extract_bytes(&data, kind, usize::MAX).map_err(wrap)?;
                Ok(Extracted { res, raw: None })
            }
            _ => {
                let engine = engine.ok_or_else(|| {
                    format!("no extraction engine available for {}", go_quote(kind))
                })?;
                let res = engine
                    .extract(&Context::background(), source, kind)
                    .map_err(wrap)?;
                Ok(Extracted { res, raw: None })
            }
        }
    }

    fn process_source(
        &self,
        source: &str,
        hash: &str,
        kind: &str,
        opts: Option<&IngestOptions>,
    ) -> Result<IngestResult, String> {
        let extracted = match kind {
            "text/plain" | "text/markdown" | "text/csv" => self.extract_text(source, kind, None)?,
            _ => {
                if self.engine.is_none() {
                    return Err(format!(
                        "no extraction engine available for {}",
                        go_quote(kind)
                    ));
                }
                self.extract_text(source, kind, self.engine_for(opts))?
            }
        };
        let res = &extracted.res;

        let mut archive_path = String::new();
        if let Some(o) = opts.filter(|o| !o.archive_path_override.is_empty()) {
            archive_path.clone_from(&o.archive_path_override);
        } else if !self.archive_dir.is_empty() {
            let physical =
                goos::join(&[&self.archive_dir, &format!("{hash}{}", goos::ext(source))]);
            atomic_copy(source, &physical).map_err(|e| format!("archive file: {e}"))?;
            archive_path = self.vault_relative_archive(&physical);
        }

        // Classification failures do not block basic OCR/text ingestion.
        let rules = match self.lock().list_rules() {
            Ok(r) => r.unwrap_or_default(),
            Err(e) => {
                self.logf(format!("Failed to load classification rules: {e}"));
                Vec::new()
            }
        };
        let no_tags: Vec<String> = Vec::new();
        let presets = Presets {
            category: opts.map_or("", |o| &o.preset_category),
            tags: opts.map_or(&no_tags[..], |o| &o.preset_tags),
            correspondent: opts.map_or("", |o| &o.preset_correspondent),
            document_type: opts.map_or("", |o| &o.preset_document_type),
        };
        let class = classify(&rules, &res.text, &presets);

        let note_source = opts
            .filter(|o| !o.source_path_override.is_empty())
            .map_or(source, |o| &o.source_path_override);
        let now = OffsetDateTime::now_utc();
        let body: &[u8] = extracted.raw.as_deref().unwrap_or(res.text.as_bytes());
        let tags = class.tags.clone().unwrap_or_default();
        let vault_path = if let Some(o) = opts.filter(|o| !o.existing_vault_path.is_empty()) {
            let note = Note {
                source_path: note_source.to_owned(),
                ingested_at: Some(now),
                sha256: hash.to_owned(),
                mime: res.mime.clone(),
                tags: tags.clone(),
                category: class.category.clone(),
                correspondent: class.correspondent.clone(),
                document_type: class.document_type.clone(),
                ocr_engine: res.engine.clone(),
                ocr_language: res.language.clone(),
                archive_path: archive_path.clone(),
                ..Note::default()
            };
            self.writer
                .update_note(&o.existing_vault_path, &note, body)
                .map_err(|e| format!("write note: {e}"))?;
            o.existing_vault_path.clone()
        } else {
            let input = NoteInput {
                source_path: note_source,
                sha256: hash,
                mime: &res.mime,
                ocr_engine: &res.engine,
                ocr_language: &res.language,
                archive_path: &archive_path,
                ingested_at: Some(now),
                category: &class.category,
                tags: &tags,
                correspondent: &class.correspondent,
                document_type: &class.document_type,
                imported_from: opts.map_or("", |o| &o.imported_from),
                import_run_id: opts.map_or("", |o| &o.import_run_id),
                source_uri: opts.map_or("", |o| &o.source_uri),
                download_uri: opts.map_or("", |o| &o.download_uri),
                paperless: opts.and_then(|o| o.paperless.as_ref()),
                layout: opts.and_then(|o| o.layout.as_ref()),
            };
            self.writer
                .write_note(&input, body)
                .map_err(|e| format!("write note: {e}"))?
        };
        if let Some(hook) = &self.post_index
            && let Err(e) = hook(&vault_path)
        {
            self.logf(format!(
                "symseek post-ingest index failed for {vault_path}: {e}"
            ));
        }

        let mut result = IngestResult {
            source_path: note_source.to_owned(),
            sha256: hash.to_owned(),
            kind: kind.to_owned(),
            extract: Some(res.clone()),
            vault_path: vault_path.clone(),
            archive_path,
            category: class.category,
            tags: class.tags,
            correspondent: class.correspondent,
            document_type: class.document_type,
            sidecar_path: String::new(),
            extraction_count: 0,
        };

        if !self.extraction_profile.is_empty() && !res.text.trim().is_empty() {
            match annotate::get_profile(&self.extraction_profile) {
                Err(e) => self.logf(format!(
                    "annotate: invalid profile {}: {e}",
                    go_quote(&self.extraction_profile)
                )),
                Ok(profile) => {
                    let extractions = annotate::extract(profile, &res.text);
                    if !extractions.is_empty() {
                        let sidecar = annotate::sidecar_path(&self.writer.vault, hash);
                        match annotate::write_sidecar(&self.writer.vault, hash, &extractions) {
                            Err(e) => self.logf(format!("annotate: write sidecar failed: {e}")),
                            Ok(()) => {
                                if let Err(e) = self.writer.update_note_sidecar(
                                    &vault_path,
                                    &sidecar,
                                    extractions.len(),
                                ) {
                                    self.logf(format!(
                                        "annotate: update note frontmatter failed: {e}"
                                    ));
                                }
                                result.sidecar_path = sidecar;
                                result.extraction_count = extractions.len();
                            }
                        }
                        let doc = self
                            .lock()
                            .by_hash_in_vault(hash, &self.destination_vault_root());
                        if let Ok(doc) = doc
                            && let Err(e) = self.lock().record_extractions(
                                doc.id,
                                &self.extraction_profile,
                                &extractions,
                            )
                        {
                            self.logf(format!("annotate: record extractions to store failed: {e}"));
                        }
                    }
                    if let Some(hook) = &self.post_extract
                        && let Err(e) = hook(&result, &extractions)
                    {
                        self.logf(format!("annotate: PostExtract hook failed: {e}"));
                    }
                }
            }
        }
        Ok(result)
    }
}

/// SHA-256 of a file as lowercase hex.
pub fn hash_file(path: &str) -> Result<String, String> {
    let mut file = std::fs::File::open(path).map_err(|e| goos::path_error("open", path, &e))?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0_u8; 64 * 1024];
    loop {
        let n = file
            .read(&mut buf)
            .map_err(|e| goos::path_error("read", path, &e))?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

/// Copies `src` to `dst` through a temporary file; an existing `dst` is kept.
fn atomic_copy(src: &str, dst: &str) -> Result<(), String> {
    use std::os::unix::fs::OpenOptionsExt;
    if std::fs::metadata(dst).is_ok() {
        return Ok(());
    }
    let mut input = std::fs::File::open(src).map_err(|e| goos::path_error("open", src, &e))?;
    let dir = goos::dir(dst);
    goos::mkdir_all(&dir, 0o700)?;
    let pid = u64::from(std::process::id());
    let mut seed = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos() as u64)
        ^ pid.rotate_left(32);
    let mut tries = 0;
    let (mut out, tmp) = loop {
        seed = seed
            .wrapping_mul(6_364_136_223_846_793_005)
            .wrapping_add(1_442_695_040_888_963_407);
        let name = goos::join(&[
            &dir,
            &format!("symingest-archive-tmp-{}", (seed >> 33) as u32),
        ]);
        match std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&name)
        {
            Ok(f) => break (f, name),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists && tries < 10_000 => tries += 1,
            Err(e) => return Err(goos::path_error("open", &name, &e)),
        }
    };
    let copy = (|| -> Result<(), String> {
        std::io::copy(&mut input, &mut out).map_err(|e| goos::io_text(&e))?;
        out.sync_all().map_err(|e| goos::io_text(&e))?;
        Ok(())
    })();
    drop(out);
    let finish = copy.and_then(|()| {
        std::fs::rename(&tmp, dst).map_err(|e| format!("rename {tmp} {dst}: {}", goos::io_text(&e)))
    });
    if finish.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    finish
}
