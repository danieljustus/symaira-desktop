//! Ingest-store tools: classification rules (`desk_rules_*`), ingestion
//! (`desk_ingest`, `desk_ingest_jobs`, `desk_ingest_retry`, `desk_ingest_reocr`)
//! and the legacy aliases (`list_rules`, `add_rule`, `delete_rule`,
//! `ingest_file`, `list_jobs`, `retry_job`, `reocr`). Go registers each alias
//! as the same handler as its canonical tool.
use serde_json::{Value, json};

use super::{
    ServerConfig,
    args::{Kind, decode, field},
    read::encoded,
};
use crate::ingest_common::{SCHEMA_VERSION, configured_vault, open_store, resolve};

pub(super) fn canonical(name: &str) -> Option<&'static str> {
    Some(match name {
        "desk_rules_list" | "list_rules" => "desk_rules_list",
        "desk_rules_add" | "add_rule" => "desk_rules_add",
        "desk_rules_update" => "desk_rules_update",
        "desk_rules_delete" | "delete_rule" => "desk_rules_delete",
        "desk_ingest" | "ingest_file" => "desk_ingest",
        "desk_ingest_jobs" | "list_jobs" => "desk_ingest_jobs",
        "desk_ingest_retry" | "retry_job" => "desk_ingest_retry",
        "desk_ingest_reocr" | "reocr" => "desk_ingest_reocr",
        _ => return None,
    })
}

/// Tools that mutate the store: only registered with `--allow-write`.
pub(super) fn is_write(canonical: &str) -> bool {
    !matches!(canonical, "desk_rules_list" | "desk_ingest_jobs")
}

fn store(config: &ServerConfig) -> Result<symdesk_ingest::Store, String> {
    // Go `ingestOptions(cfg)`: the root configuration with the vault flag applied.
    let vault = configured_vault(config.vault.as_deref())?;
    let resolved = resolve(&vault)?;
    open_store(&resolved)
}

pub(super) fn call(name: &str, raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    match canonical(name).unwrap_or_default() {
        "desk_rules_list" => {
            let store = store(config)?;
            let rules = store.list_rules().map_err(|error| error.to_string())?;
            let rules: Vec<Value> = rules
                .unwrap_or_default()
                .iter()
                .map(crate::rules_cli::rule_value)
                .collect();
            encoded(&json!({"rules": rules, "schema_version": SCHEMA_VERSION}))
        }
        "desk_rules_add" => {
            let args = decode(
                raw,
                &[
                    field("pattern", "Pattern", Kind::Str),
                    field("kind", "Kind", Kind::Str),
                    field("value", "Value", Kind::Str),
                ],
            )?;
            let (pattern, kind, value) = (args.str("pattern"), args.str("kind"), args.str("value"));
            if pattern.is_empty() || kind.is_empty() || value.is_empty() {
                return Err("pattern, kind and value are required".into());
            }
            let store = store(config)?;
            let rule = store.add_rule(&pattern, &kind, &value)?;
            encoded(
                &json!({"rule": crate::rules_cli::rule_value(&rule), "schema_version": SCHEMA_VERSION}),
            )
        }
        "desk_rules_update" => {
            let args = decode(
                raw,
                &[
                    field("id", "ID", Kind::I64),
                    field("pattern", "Pattern", Kind::Str),
                    field("kind", "Kind", Kind::Str),
                    field("value", "Value", Kind::Str),
                ],
            )?;
            let (id, pattern, kind, value) = (
                args.i64("id"),
                args.str("pattern"),
                args.str("kind"),
                args.str("value"),
            );
            if id == 0 || pattern.is_empty() || kind.is_empty() || value.is_empty() {
                return Err("id, pattern, kind and value are required".into());
            }
            let store = store(config)?;
            let rule = store.update_rule(id, &pattern, &kind, &value)?;
            encoded(
                &json!({"rule": crate::rules_cli::rule_value(&rule), "schema_version": SCHEMA_VERSION}),
            )
        }
        "desk_rules_delete" => {
            let args = decode(raw, &[field("id", "ID", Kind::I64)])?;
            let id = args.i64("id");
            if id == 0 {
                return Err("id is required".into());
            }
            let store = store(config)?;
            store.delete_rule(id)?;
            encoded(&json!({"deleted": true, "id": id, "schema_version": SCHEMA_VERSION}))
        }
        "desk_ingest" => {
            let args = decode(raw, &[field("source_path", "SourcePath", Kind::Str)])?;
            let source = args.str("source_path");
            if source.is_empty() {
                return Err("source_path is required".into());
            }
            let (root, mut sidecar) = super::open_sidecar(config)?;
            let outcome = crate::ingest_cli::service_ingest(&root, &mut sidecar, &source);
            crate::ingest_cli::close_sidecar(sidecar);
            encoded(&json!({"path": outcome?}))
        }
        "desk_ingest_jobs" => {
            // Go decodes no arguments for this tool; the service is opened first.
            let (root, sidecar) = super::open_sidecar(config)?;
            crate::ingest_cli::close_sidecar(sidecar);
            encoded(&jobs(&root)?)
        }
        "desk_ingest_retry" => {
            let args = decode(raw, &[field("id", "ID", Kind::Str)])?;
            let (_, sidecar) = super::open_sidecar(config)?;
            crate::ingest_cli::close_sidecar(sidecar);
            retry(&args.str("id"))?;
            encoded(&json!({"status": "ok"}))
        }
        "desk_ingest_reocr" => reocr(raw, config),
        other => Err(format!("Unknown tool: {other}")),
    }
}

/// Go `Jobs(limit 0)`: the first page of the vault's jobs, marshalled through
/// a generic value, so keys come out sorted and an empty queue is `[]`.
fn jobs(root: &std::path::Path) -> Result<Value, String> {
    let root_text = root.to_string_lossy().into_owned();
    let resolved = resolve(&root_text)?;
    let store = open_store(&resolved)?;
    let vault_root = match crate::ingest_common::go_abs(&root_text) {
        Ok(absolute) => crate::ingest_common::go_clean(&absolute),
        Err(_) => root_text,
    };
    let (listed, _total) = store
        .list_jobs_page(&vault_root, 100, 0)
        .map_err(|error| format!("list jobs: {error}"))?;
    let mut out = Vec::new();
    for job in listed.unwrap_or_default() {
        let mut object = serde_json::Map::new();
        object.insert("attempts".into(), json!(job.attempts));
        object.insert("created_at".into(), json!(job.created_at));
        object.insert("document_id".into(), json!(job.document_id));
        object.insert("id".into(), json!(job.id));
        object.insert("kind".into(), json!(job.kind));
        if let Some(error) = job.last_error.filter(|error| !error.is_empty()) {
            object.insert("last_error".into(), json!(error));
        }
        object.insert("source_path".into(), json!(job.source_path));
        object.insert("status".into(), json!(job.status));
        object.insert("updated_at".into(), json!(job.updated_at));
        out.push(Value::Object(object));
    }
    Ok(Value::Array(out))
}

/// Go `ingest.IngestRetry`.
fn retry(job: &str) -> Result<(), String> {
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
            symdesk_vault::go_quote(job),
            symdesk_vault::go_quote(trimmed)
        )
    })?;
    let resolved = resolve("")?;
    let mut store = open_store(&resolved)?;
    store
        .retry_job(id)
        .map_err(|error| format!("retry job {id}: {error}"))
}

fn reocr(raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    use crate::ingest_cli::ReocrFailure;
    let args = decode(
        raw,
        &[
            field("document_id", "DocumentID", Kind::I64),
            field("archive_path", "ArchivePath", Kind::Str),
            field("source", "Source", Kind::Str),
        ],
    )?;
    let document_id = args.i64("document_id");
    let mut archive_path = args.str("archive_path");
    if !args.str("source").is_empty() && archive_path.is_empty() {
        archive_path = args.str("source");
    }
    if archive_path.is_empty() && document_id == 0 {
        return Err("document_id or archive_path is required".into());
    }
    if !archive_path.is_empty() && document_id != 0 {
        return Err("provide either document_id or archive_path, not both".into());
    }
    let vault = configured_vault(config.vault.as_deref())?;
    match crate::ingest_cli::reprocess(&vault, document_id, &archive_path) {
        Ok((document, job_id, status, output)) => encoded(&json!({
            "document_id": document,
            "job_id": job_id,
            "output_path": output,
            "schema_version": SCHEMA_VERSION,
            "status": status,
        })),
        Err(failure) => {
            // The legacy envelope reports not-found and missing-original
            // failures; every other failure is a bare tool error.
            let code = match &failure {
                ReocrFailure::NotFound(_) => "not_found",
                ReocrFailure::NoArchivedOriginal(_) => "no_archived_original",
                ReocrFailure::Plain(_) | ReocrFailure::Other(_) => {
                    return Err(failure.message().to_owned());
                }
            };
            encoded(&json!({
                "document_id": document_id,
                "error": {"code": code, "message": failure.message()},
                "schema_version": SCHEMA_VERSION,
                "status": "failed",
            }))
        }
    }
}
