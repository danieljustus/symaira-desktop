//! `symdesk ingest <file>` and `ingest jobs|retry|reocr|split|merge|rotate`
//! (Go `cmd/symdesk/ingest.go`, `internal/ingest/api.go`,
//! `internal/ingest/ingest.go`, `internal/service/service.go`).
use std::{
    path::PathBuf,
    process::ExitCode,
    sync::{Arc, Mutex},
};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_ingest::{
    Store,
    ocr::{self, Runner},
    pipeline::{Engine, Pipeline, PipelineError},
    vlm::VlmRunner,
    writer::NoteWriter,
};
use symdesk_render::pdfops::{self, Context, Tools};
use symdesk_vault::{activity_journal::append_activity, go_quote};

use crate::{
    emit_error,
    ingest_common::{
        Resolved, SCHEMA_VERSION, configured_vault, go_abs, go_clean, open_store, resolve,
    },
    write_go_json, write_stdout,
};

fn int_arg(name: &'static str, default: &'static str) -> Arg {
    Arg::new(name)
        .long(name)
        .num_args(1)
        .allow_hyphen_values(true)
        .default_value(default)
        .overrides_with(name)
        .value_parser(clap::value_parser!(i64))
}

pub fn cli() -> Command {
    Command::new("ingest")
        .subcommand_precedence_over_arg(true)
        .arg(Arg::new("args").num_args(0..).action(ArgAction::Append))
        .subcommand(
            Command::new("jobs")
                .arg(int_arg("limit", "100"))
                .arg(int_arg("offset", "0"))
                .arg(Arg::new("extra").num_args(0..).hide(true)),
        )
        .subcommand(Command::new("retry").arg(Arg::new("args").num_args(0..)))
        .subcommand(
            Command::new("reocr")
                .arg(int_arg("document-id", "0"))
                .arg(Arg::new("args").num_args(0..)),
        )
        .subcommand(
            Command::new("split")
                .arg(Arg::new("at").long("at").num_args(1).overrides_with("at"))
                .arg(
                    Arg::new("output-dir")
                        .long("output-dir")
                        .num_args(1)
                        .overrides_with("output-dir"),
                )
                .arg(Arg::new("args").num_args(0..)),
        )
        .subcommand(
            Command::new("merge")
                .arg(
                    Arg::new("local-output")
                        .long("local-output")
                        .num_args(1)
                        .overrides_with("local-output"),
                )
                .arg(Arg::new("args").num_args(0..)),
        )
        .subcommand(
            Command::new("rotate")
                .arg(
                    Arg::new("local-output")
                        .long("local-output")
                        .num_args(1)
                        .overrides_with("local-output"),
                )
                .arg(int_arg("degrees", "0"))
                .arg(
                    Arg::new("pages")
                        .long("pages")
                        .num_args(1)
                        .overrides_with("pages"),
                )
                .arg(Arg::new("args").num_args(0..)),
        )
}

/// Cobra's command-local `--output` of `ingest merge|rotate` shadows the
/// persistent one; Clap names it apart from the global flag.
pub fn rewrite_output_flag(args: &mut [std::ffi::OsString]) {
    let Some(at) = args.iter().position(|arg| arg == "ingest") else {
        return;
    };
    if !args
        .get(at + 1)
        .is_some_and(|arg| arg == "merge" || arg == "rotate")
    {
        return;
    }
    for argument in args.iter_mut().skip(at + 2) {
        if argument == "--output" {
            *argument = std::ffi::OsString::from("--local-output");
        } else if let Some(value) = argument.to_str().and_then(|v| v.strip_prefix("--output=")) {
            *argument = std::ffi::OsString::from(format!("--local-output={value}"));
        }
    }
}

fn positional(matches: &ArgMatches) -> Vec<String> {
    matches
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .cloned()
        .collect()
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let result = match command.subcommand() {
        None => ingest_file(command, vault, json),
        Some(("jobs", sub)) => jobs(sub, vault, json),
        Some(("retry", sub)) => retry(sub, vault, json),
        Some(("reocr", sub)) => return reocr(sub, vault, json),
        Some(("split", sub)) => split(sub, json),
        Some(("merge", sub)) => merge(sub, json),
        Some(("rotate", sub)) => rotate(sub, json),
        Some(_) => Ok(crate::process_exit(CoreExitCode::Ok)),
    };
    result.unwrap_or_else(|error| emit_error(error, json))
}

/// Go `Go filepath.Rel` for two cleaned paths of the same kind.
fn go_rel(base: &str, target: &str) -> Option<String> {
    let (base, target) = (go_clean(base), go_clean(target));
    if base == target {
        return Some(".".to_owned());
    }
    if base.starts_with('/') != target.starts_with('/') {
        return None;
    }
    let parts = |path: &str| -> Vec<String> {
        path.split('/')
            .filter(|part| !part.is_empty() && *part != ".")
            .map(str::to_owned)
            .collect()
    };
    let (base, target) = (parts(&base), parts(&target));
    let common = base.iter().zip(&target).take_while(|(a, b)| a == b).count();
    if base[common..].iter().any(|part| part == "..") {
        return None;
    }
    let mut out: Vec<String> = base[common..].iter().map(|_| "..".to_owned()).collect();
    out.extend(target[common..].iter().cloned());
    Some(if out.is_empty() {
        ".".into()
    } else {
        out.join("/")
    })
}

/// Go `initServiceDeps`: the resolved vault root and its sidecar database.
pub fn init_service_deps(vault: Option<&str>) -> Result<(PathBuf, symdesk_index::Sidecar), String> {
    let root = crate::resolve_vault(vault).map_err(|e| {
        if e.starts_with("vault path not configured") {
            "vault path not configured (use flag or SYMDESK_VAULT env)".into()
        } else {
            crate::index_cli::index_vault_error(vault, &e)
        }
    })?;
    let sidecar = symdesk_index::open_for_vault(&root).map_err(|e| e.to_string())?;
    Ok((root, sidecar))
}

pub(crate) fn close_sidecar(mut sidecar: symdesk_index::Sidecar) {
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
}

fn engine_for(resolved: &Resolved) -> Arc<dyn Engine> {
    if resolved.ollama_model.is_empty() {
        Arc::new(Runner::default_runner(&resolved.ocr_lang))
    } else {
        Arc::new(VlmRunner::new(
            &resolved.ollama_base_url,
            &resolved.ollama_model,
            &resolved.ocr_lang,
            None,
        ))
    }
}

fn pipeline_for(resolved: &Resolved, store: Store) -> Pipeline {
    let mut pipeline = Pipeline::new(
        Arc::new(Mutex::new(store)),
        NoteWriter::new(&resolved.vault),
    );
    pipeline.engine = Some(engine_for(resolved));
    pipeline.archive_dir.clone_from(&resolved.archive);
    pipeline.vault_root.clone_from(&resolved.vault);
    pipeline
}

/// Go `service.Ingest`: run the pipeline, index the new note and journal it.
/// Returns the note's vault-relative path.
pub fn service_ingest(
    root: &std::path::Path,
    sidecar: &mut symdesk_index::Sidecar,
    source: &str,
) -> Result<String, String> {
    let root_text = root.to_string_lossy().into_owned();
    let resolved = resolve(&root_text)?;
    if resolved.vault.is_empty() {
        return Err("no vault configured".to_owned());
    }
    let absolute = go_abs(source)
        .map_err(|error| format!("invalid source path {}: {error}", go_quote(source)))?;
    let store = open_store(&resolved)?;
    let pipeline = pipeline_for(&resolved, store);
    let result = pipeline
        .ingest(&absolute, None)
        .map_err(|error: PipelineError| error.to_string())?;
    let rel = if result.vault_path.starts_with('/') {
        go_rel(&root_text, &result.vault_path).unwrap_or_else(|| result.vault_path.clone())
    } else {
        result.vault_path.clone()
    };
    // Index the new note (best effort) and journal the ingest.
    if let Ok(path) = symdesk_vault::secure_path(root, &rel) {
        let _ = crate::mutate_cli::reindex(sidecar, &path);
    }
    let _ = append_activity(root, "file_added", &rel, "", "ingested document");
    Ok(rel)
}

fn ingest_file(command: &ArgMatches, vault: Option<&str>, json: bool) -> Result<ExitCode, String> {
    let args = positional(command);
    let Some(source) = args.first() else {
        // Go prints the command's help for a bare `ingest`.
        return Ok(crate::help_cli::render("ingest"));
    };
    let (root, mut sidecar) = init_service_deps(vault)?;
    let outcome = service_ingest(&root, &mut sidecar, source);
    close_sidecar(sidecar);
    let rel = outcome?;
    if json {
        return Ok(write_go_json(&serde_json::json!({ "path": rel })));
    }
    Ok(write_stdout(format!("map[path:{rel}]\n")))
}

#[derive(Serialize)]
struct JobOut<'a> {
    id: i64,
    document_id: i64,
    status: &'a str,
    attempts: i64,
    kind: &'a str,
    source_path: &'a str,
    #[serde(skip_serializing_if = "String::is_empty")]
    last_error: String,
    created_at: &'a str,
    updated_at: &'a str,
}

fn jobs(sub: &ArgMatches, vault: Option<&str>, json: bool) -> Result<ExitCode, String> {
    let mut limit = *sub.get_one::<i64>("limit").unwrap_or(&100);
    let mut offset = *sub.get_one::<i64>("offset").unwrap_or(&0);
    let (root, sidecar) = init_service_deps(vault)?;
    close_sidecar(sidecar);
    let root_text = root.to_string_lossy().into_owned();
    if limit <= 0 {
        limit = 100;
    }
    if offset < 0 {
        offset = 0;
    }
    let resolved = resolve(&root_text)?;
    let store = open_store(&resolved)?;
    let vault_root = match go_abs(&root_text) {
        Ok(absolute) => go_clean(&absolute),
        Err(_) => root_text,
    };
    let (listed, total) = store
        .list_jobs_page(&vault_root, limit, offset)
        .map_err(|error| format!("list jobs: {error}"))?;
    let listed = listed.unwrap_or_default();
    let out: Vec<JobOut<'_>> = listed
        .iter()
        .map(|job| JobOut {
            id: job.id,
            document_id: job.document_id,
            status: &job.status,
            attempts: job.attempts,
            kind: &job.kind,
            source_path: &job.source_path,
            last_error: job.last_error.clone().unwrap_or_default(),
            created_at: &job.created_at,
            updated_at: &job.updated_at,
        })
        .collect();
    if json {
        let page = serde_json::json!({
            "jobs": out, "total": total, "limit": limit, "offset": offset,
        });
        let text = serde_json::to_string_pretty(&page).map_err(|error| error.to_string())?;
        return Ok(write_stdout(format!("{}\n", crate::go_escape_json(text))));
    }
    let mut text = format!(
        "Jobs {}-{} of {total}\n",
        offset + 1,
        offset + i64::try_from(out.len()).unwrap_or(0)
    );
    for job in &out {
        text.push_str(&format!(
            "Job ID: {} | Status: {} | Source: {}\n",
            job.id, job.status, job.source_path
        ));
    }
    Ok(write_stdout(text))
}

fn retry(sub: &ArgMatches, vault: Option<&str>, json: bool) -> Result<ExitCode, String> {
    let args = positional(sub);
    let job = args.first().cloned().unwrap_or_default();
    let (_, sidecar) = init_service_deps(vault)?;
    close_sidecar(sidecar);
    let trimmed = job.trim_matches(|c: char| c.is_whitespace());
    let id: i64 = trimmed.parse().map_err(|error: std::num::ParseIntError| {
        let reason = match error.kind() {
            std::num::IntErrorKind::PosOverflow | std::num::IntErrorKind::NegOverflow => {
                "value out of range"
            }
            _ => "invalid syntax",
        };
        format!(
            "invalid job ID {}: strconv.ParseInt: parsing {}: {reason}",
            go_quote(&job),
            go_quote(trimmed)
        )
    })?;
    let resolved = resolve("")?;
    let mut store = open_store(&resolved)?;
    store
        .retry_job(id)
        .map_err(|error| format!("retry job {id}: {error}"))?;
    if json {
        return Ok(write_go_json(
            &serde_json::json!({ "job_id": job, "status": "retried" }),
        ));
    }
    Ok(write_stdout(format!("map[job_id:{job} status:retried]\n")))
}

#[derive(Serialize)]
struct ReocrError<'a> {
    code: &'a str,
    message: String,
}

#[derive(Serialize)]
struct ReocrResponse<'a> {
    schema_version: u32,
    document_id: i64,
    job_id: i64,
    status: &'a str,
    output_path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<ReocrError<'a>>,
}

pub(crate) enum ReocrFailure {
    Plain(String),
    NotFound(String),
    NoArchivedOriginal(String),
    Other(String),
}

impl ReocrFailure {
    pub(crate) fn message(&self) -> &str {
        match self {
            Self::Plain(m) | Self::NotFound(m) | Self::NoArchivedOriginal(m) | Self::Other(m) => m,
        }
    }
}

fn reocr(sub: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let args = positional(sub);
    let document_id = *sub.get_one::<i64>("document-id").unwrap_or(&0);
    let archive_path = args.first().cloned().unwrap_or_default();
    let validation = if archive_path.is_empty() && document_id == 0 {
        Some("provide an archive path or --document-id")
    } else if !archive_path.is_empty() && document_id != 0 {
        Some("provide either an archive path or --document-id, not both")
    } else {
        None
    };
    if let Some(message) = validation {
        return emit_error(message.to_owned(), json);
    }
    let outcome = configured_vault(vault)
        .map_err(ReocrFailure::Plain)
        .and_then(|configured| reprocess(&configured, document_id, &archive_path));
    match outcome {
        Ok((document, job_id, status, output)) => {
            if json {
                return write_go_json(&ReocrResponse {
                    schema_version: SCHEMA_VERSION,
                    document_id: document,
                    job_id,
                    status,
                    output_path: output,
                    error: None,
                });
            }
            let mut text = format!("document {document}: {status} (job {job_id})\n");
            if !output.is_empty() {
                text.push_str(&format!("  output: {output}\n"));
            }
            write_stdout(text)
        }
        Err(failure) => {
            if !json {
                return emit_error(failure.message().to_owned(), false);
            }
            let (code, exit) = match &failure {
                ReocrFailure::NotFound(_) => ("not_found", CoreExitCode::NotFound),
                ReocrFailure::NoArchivedOriginal(_) => ("no_archived_original", CoreExitCode::Data),
                _ => ("internal", CoreExitCode::Generic),
            };
            let written = write_go_json(&ReocrResponse {
                schema_version: SCHEMA_VERSION,
                document_id,
                job_id: 0,
                status: "failed",
                output_path: String::new(),
                error: Some(ReocrError {
                    code,
                    message: failure.message().to_owned(),
                }),
            });
            if written == crate::process_exit(CoreExitCode::Ok) {
                crate::process_exit(exit)
            } else {
                written
            }
        }
    }
}

fn archive_candidates(resolved: &Resolved, input: &str) -> Result<Vec<String>, String> {
    if input.is_empty() {
        return Err("archive path is empty".to_owned());
    }
    let mut candidates: Vec<String> = Vec::new();
    let mut push = |candidate: String| {
        if !candidates.contains(&candidate) {
            candidates.push(candidate);
        }
    };
    if input.starts_with('/') {
        let absolute = go_clean(input);
        if !resolved.vault.is_empty() {
            let vault_root = go_abs(&resolved.vault)
                .map_err(|e| format!("invalid vault path {}: {e}", go_quote(&resolved.vault)))?;
            if let Some(rel) = go_rel(&vault_root, &absolute)
                && rel != ".."
                && !rel.starts_with("../")
                && !rel.starts_with('/')
            {
                push(rel);
            }
        }
        push(absolute);
        return Ok(candidates);
    }
    let rel = go_clean(input);
    push(rel.clone());
    let physical = if resolved.vault.is_empty() {
        go_abs(&rel)
    } else {
        go_abs(&format!("{}/{rel}", resolved.vault))
    }
    .map_err(|e| format!("invalid archive path {}: {e}", go_quote(input)))?;
    push(go_clean(&physical));
    Ok(candidates)
}

pub(crate) fn reprocess(
    configured_vault: &str,
    document_id: i64,
    archive_path: &str,
) -> Result<(i64, i64, &'static str, String), ReocrFailure> {
    let resolved = resolve(configured_vault).map_err(ReocrFailure::Other)?;
    let candidates = if archive_path.is_empty() {
        Vec::new()
    } else {
        archive_candidates(&resolved, archive_path).map_err(ReocrFailure::Other)?
    };
    let store = open_store(&resolved).map_err(ReocrFailure::Other)?;
    let mut target = document_id;
    if !archive_path.is_empty() {
        let mut found = None;
        for candidate in &candidates {
            match store.by_archive_path(candidate) {
                Ok(document) => {
                    found = Some(document.id);
                    break;
                }
                Err(error) if error.contains("sql: no rows in result set") => {}
                Err(error) => {
                    return Err(ReocrFailure::Other(format!(
                        "look up document by archive path: {error}"
                    )));
                }
            }
        }
        let Some(id) = found else {
            return Err(ReocrFailure::NotFound(format!(
                "document not found: archive path {}",
                go_quote(archive_path)
            )));
        };
        target = id;
    }
    let pipeline = pipeline_for(&resolved, store);
    match pipeline.reprocess(target, "", None) {
        Ok(result) => {
            let status = if result.already_running {
                "already_running"
            } else {
                "completed"
            };
            let output = if result.already_running {
                String::new()
            } else {
                result.result.map(|r| r.vault_path).unwrap_or_default()
            };
            Ok((target, result.job.id, status, output))
        }
        Err(PipelineError::NoArchivedOriginal(message)) => {
            Err(ReocrFailure::NoArchivedOriginal(message))
        }
        Err(error) => {
            let message = error.to_string();
            if archive_path.is_empty() && message.contains("sql: no rows in result set") {
                Err(ReocrFailure::NotFound(format!(
                    "document not found: id {document_id}"
                )))
            } else {
                Err(ReocrFailure::Other(message))
            }
        }
    }
}

fn on_path(tool: &str) -> bool {
    ocr::look_path(tool).is_ok()
}

fn split(sub: &ArgMatches, json: bool) -> Result<ExitCode, String> {
    let args = positional(sub);
    let at = sub.get_one::<String>("at").cloned().unwrap_or_default();
    let output_dir = sub
        .get_one::<String>("output-dir")
        .cloned()
        .unwrap_or_default();
    if at.is_empty() {
        return Err("--at is required (comma-separated pages, e.g. 2,4 or 2-3,6)".to_owned());
    }
    if output_dir.is_empty() {
        return Err("--output-dir is required".to_owned());
    }
    if !(on_path("pdfinfo") && on_path("qpdf")) {
        return Err(
            "PDF splitting requires pdfinfo and qpdf on PATH (brew install poppler qpdf)"
                .to_owned(),
        );
    }
    let parts = pdfops::split_pdf_at_spec(
        &Context::default(),
        &Tools::default(),
        &args[0],
        &at,
        &output_dir,
    )?;
    if json {
        return Ok(write_go_json(&serde_json::json!({
            "schema_version": SCHEMA_VERSION,
            "parts": parts,
        })));
    }
    Ok(write_stdout(
        parts
            .iter()
            .map(|part| format!("{part}\n"))
            .collect::<String>(),
    ))
}

fn merge(sub: &ArgMatches, json: bool) -> Result<ExitCode, String> {
    let args = positional(sub);
    let output = sub
        .get_one::<String>("local-output")
        .cloned()
        .unwrap_or_default();
    if output.is_empty() {
        return Err("--output is required".to_owned());
    }
    pdfops::merge_pdfs(&Context::default(), &Tools::default(), &args, &output)?;
    if json {
        return Ok(write_go_json(&serde_json::json!({
            "schema_version": SCHEMA_VERSION,
            "output": output,
        })));
    }
    Ok(write_stdout(format!("{output}\n")))
}

fn rotate(sub: &ArgMatches, json: bool) -> Result<ExitCode, String> {
    let args = positional(sub);
    let output = sub
        .get_one::<String>("local-output")
        .cloned()
        .unwrap_or_default();
    let degrees = *sub.get_one::<i64>("degrees").unwrap_or(&0);
    let pages = sub.get_one::<String>("pages").cloned().unwrap_or_default();
    if output.is_empty() {
        return Err("--output is required".to_owned());
    }
    if degrees == 0 {
        return Err("--degrees is required (one of -270, -180, -90, 90, 180, 270)".to_owned());
    }
    pdfops::rotate_pdf(
        &Context::default(),
        &Tools::default(),
        &args[0],
        &output,
        degrees,
        &pages,
    )?;
    if json {
        return Ok(write_go_json(&serde_json::json!({
            "schema_version": SCHEMA_VERSION,
            "output": output,
        })));
    }
    Ok(write_stdout(format!("{output}\n")))
}
