use std::{
    collections::BTreeMap,
    fs,
    io::Read,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgMatches, Command};
use rusqlite::Connection;
use serde_json::json;
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_index::{
    MAX_RETRIEVAL_SOURCE_BYTES, RetrievalDb, RetrievalDocument, RetrievalEmbeddingConfig,
    StoredRetrievalChunk, backup_database, index_location_for_vault, materialize_chunks,
    parse_markdown_retrieval_sections, parse_text_retrieval_sections, relocate_index_for_vault,
    restore_database, retrieval_embedding_config,
};
use symdesk_protocol::{LocalEmbeddingError, embed_local_ollama};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::time::Duration;

#[path = "index_status.rs"]
mod status;
#[path = "index_status_process.rs"]
mod status_process;

pub fn cli() -> Command {
    Command::new("index")
        .arg(Arg::new("path").value_name("PATH").num_args(0..=1))
        .arg(
            Arg::new("prune")
                .long("prune")
                .action(clap::ArgAction::SetTrue),
        )
        .arg(
            Arg::new("re-embed")
                .long("re-embed")
                .action(clap::ArgAction::SetTrue),
        )
        .subcommand(
            Command::new("status")
                .about("Show retrieval and document indexing status")
                .arg(
                    Arg::new("documents")
                        .long("documents")
                        .action(clap::ArgAction::SetTrue),
                )
                .arg(
                    Arg::new("state")
                        .long("state")
                        .num_args(1)
                        .value_name("STATE"),
                )
                .arg(
                    Arg::new("timeout")
                        .long("timeout")
                        .num_args(1)
                        .allow_hyphen_values(true)
                        .default_value("10s")
                        .value_name("DURATION"),
                )
                .arg(
                    Arg::new("worker")
                        .long("worker")
                        .hide(true)
                        .action(clap::ArgAction::SetTrue),
                ),
        )
        .subcommand(
            Command::new("maintenance")
                .subcommand(Command::new("location"))
                .subcommand(
                    Command::new("backup").arg(
                        Arg::new("destination")
                            .long("index-output")
                            .hide(true)
                            .num_args(1)
                            .value_name("FILE"),
                    ),
                )
                .subcommand(
                    Command::new("restore").arg(
                        Arg::new("source")
                            .long("input")
                            .num_args(1)
                            .value_name("FILE"),
                    ),
                )
                .subcommand(
                    Command::new("relocate").arg(
                        Arg::new("destination")
                            .long("index-output")
                            .hide(true)
                            .num_args(1)
                            .value_name("FILE"),
                    ),
                ),
        )
}

pub fn run(
    command: &ArgMatches,
    vault: Option<&str>,
    json_output: bool,
    json_flag: bool,
) -> ExitCode {
    if let Some(("status", status)) = command.subcommand() {
        return status::run(status, vault, json_output);
    }
    let Some(("maintenance", maintenance)) = command.subcommand() else {
        return run_build(command, vault, json_output);
    };
    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let cwd = match std::env::current_dir() {
        Ok(path) => path,
        Err(error) => {
            return super::emit_error(
                format!("failed to get current directory: {error}"),
                json_output,
            );
        }
    };
    let temp_root = std::env::temp_dir();
    let vault_root = vault.unwrap_or("");

    match maintenance.subcommand() {
        Some(("location", _)) => {
            let path = match index_location_for_vault(vault_root, &environment, &cwd, &temp_root) {
                Ok(path) => path,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            emit_result(
                json!({"index_location": path.to_string_lossy()}),
                json_output,
            )
        }
        Some(("backup", args)) => {
            let Some(destination) = args.get_one::<String>("destination") else {
                return super::emit_error("--output is required".to_owned(), json_output);
            };
            let json_output = json_flag;
            let source = match resolve_location(vault_root, &environment, &cwd, &temp_root) {
                Ok(path) => path,
                Err(error) => return super::emit_error(error, json_output),
            };
            if let Err(error) = backup_index(&source, Path::new(destination)) {
                return super::emit_error(error, json_output);
            }
            emit_result(json!({"status": "ok", "backup": destination}), json_output)
        }
        Some(("restore", args)) => {
            let Some(source) = args.get_one::<String>("source") else {
                return super::emit_error("--input is required".to_owned(), json_output);
            };
            let destination = match resolve_location(vault_root, &environment, &cwd, &temp_root) {
                Ok(path) => path,
                Err(error) => return super::emit_error(error, json_output),
            };
            if let Err(error) = restore_database(Path::new(source), &destination) {
                return super::emit_error(error.to_string(), json_output);
            }
            emit_result(
                json!({"status": "ok", "restored_from": source}),
                json_output,
            )
        }
        Some(("relocate", args)) => {
            let Some(destination) = args.get_one::<String>("destination") else {
                return super::emit_error("--output is required".to_owned(), json_output);
            };
            let json_output = json_flag;
            if let Err(error) = relocate_index_for_vault(
                vault_root,
                Path::new(destination),
                &environment,
                &cwd,
                &temp_root,
            ) {
                return super::emit_error(error.to_string(), json_output);
            }
            let location =
                match index_location_for_vault(vault_root, &environment, &cwd, &temp_root) {
                    Ok(path) => path,
                    Err(error) => return super::emit_error(error.to_string(), json_output),
                };
            emit_result(
                json!({"status": "ok", "index_location": location.to_string_lossy()}),
                json_output,
            )
        }
        Some((other, _)) => super::emit_error(
            format!("unknown index maintenance subcommand: {other}"),
            json_output,
        ),
        None => super::process_exit(CoreExitCode::Ok),
    }
}

fn run_build(command: &ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let requested = command
        .get_one::<String>("path")
        .map(String::as_str)
        .or(vault);
    let root = match super::resolve_vault(requested) {
        Ok(path) => path,
        Err(error) => {
            return super::emit_error(index_vault_error(requested, &error), json_output);
        }
    };
    let mut sidecar = match symdesk_index::open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(error) => return super::emit_error(error.to_string(), json_output),
    };

    let reembed = command.get_flag("re-embed");
    let reembed_report = if reembed {
        let reembedded = match reembed_pending_documents() {
            Ok(report) => report,
            Err(error) => {
                return super::emit_error(format!("re-embed failed: {error}"), json_output);
            }
        };
        if !json_output {
            // Go reports its processed-document count even when every provider
            // request failed and fallback chunks remain pending. Preserve its
            // established success/no-pending bytes, but label that failure
            // state as incomplete so this command does not claim resolution.
            let message = if reembedded.pending_documents == 0 {
                format!(
                    "Re-embedded {} document(s) with pending chunks.\n",
                    reembedded.resolved_documents
                )
            } else {
                format!(
                    "Re-embed incomplete: {} document(s) still have pending chunks.\n",
                    reembedded.pending_documents
                )
            };
            let code = super::write_stdout(message);
            if code != super::process_exit(CoreExitCode::Ok) {
                return code;
            }
        }
        Some(reembedded)
    } else {
        None
    };

    let before = match indexed_paths(&root) {
        Ok(paths) => paths,
        Err(error) => return super::emit_error(error, json_output),
    };
    let discovered = match symdesk_vault::walk_markdown(&root) {
        Ok(paths) => paths,
        Err(error) => return super::emit_error(error.to_string(), json_output),
    };
    let mut indexed = 0usize;
    let mut skipped = 0usize;
    for relative in discovered {
        let path = root.join(&relative);
        let key = path.to_string_lossy().into_owned();
        let bytes = match fs::read(&path) {
            Ok(bytes) => bytes,
            Err(error) => return super::emit_error(format!("{error}"), json_output),
        };
        let document = match symdesk_vault::parse_bytes(&key, &bytes) {
            Ok(document) => document,
            Err(error) => return super::emit_error(error.to_string(), json_output),
        };
        if document.derived {
            continue;
        }
        if !reembed && before.get(&key).is_some_and(|sha| sha == &document.sha256) {
            skipped += 1;
        } else {
            indexed += 1;
        }
    }

    if let Err(error) = sidecar.refresh_index_for_cli(&root) {
        return super::emit_error(error.to_string(), json_output);
    }
    let pruned = if command.get_flag("prune") {
        match sidecar.prune(&root) {
            Ok(count) => Some(count),
            Err(error) => {
                return super::emit_error(format!("prune failed: {error}"), json_output);
            }
        }
    } else {
        None
    };
    let status = if reembed_report.is_some_and(|report| report.pending_documents > 0) {
        "incomplete"
    } else {
        "ok"
    };
    let mut result = BTreeMap::from([
        ("indexed", json!(indexed)),
        ("skipped", json!(skipped)),
        ("status", json!(status)),
    ]);
    if let Some(report) = reembed_report {
        result.insert("reembedded_documents", json!(report.resolved_documents));
        result.insert("reembed_pending_documents", json!(report.pending_documents));
    }
    if let Some(count) = pruned {
        result.insert("pruned", json!(count));
    }
    if json_output {
        let mut rendered = serde_json::to_string(&result).unwrap_or_default();
        rendered.push('\n');
        super::write_stdout(rendered)
    } else {
        let summary = format!("Index complete. {indexed} new/updated files, {skipped} skipped.\n");
        let summary = if let Some(count) = pruned {
            format!("{summary}Prune complete. {count} stale entries removed.\n")
        } else {
            summary
        };
        super::write_stdout(summary)
    }
}

#[derive(Clone, Copy)]
struct ReembedReport {
    resolved_documents: usize,
    pending_documents: usize,
}

fn reembed_pending_documents() -> Result<ReembedReport, String> {
    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let cwd = std::env::current_dir().map_err(|error| error.to_string())?;
    let temp_root = std::env::temp_dir();
    let path = index_location_for_vault("", &environment, &cwd, &temp_root)
        .map_err(|error| error.to_string())?;
    if !path.exists() {
        return Ok(ReembedReport {
            resolved_documents: 0,
            pending_documents: 0,
        });
    }
    // Keep the provider-free no-pending path read-only. Legacy probe states
    // may contain only the chunks table; migrate only when repair work exists.
    let connection = Connection::open_with_flags(
        &path,
        rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY | rusqlite::OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )
    .map_err(|error| error.to_string())?;
    let has_chunks: bool = connection
        .query_row(
            "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='chunks')",
            [],
            |row| row.get(0),
        )
        .map_err(|error| error.to_string())?;
    if !has_chunks {
        return Ok(ReembedReport {
            resolved_documents: 0,
            pending_documents: 0,
        });
    }
    let pending_documents: i64 = connection
        .query_row(
            "SELECT COUNT(DISTINCT document_path) FROM chunks WHERE embedding_pending=1",
            [],
            |row| row.get(0),
        )
        .map_err(|error| error.to_string())?;
    drop(connection);
    if pending_documents == 0 {
        return Ok(ReembedReport {
            resolved_documents: 0,
            pending_documents: 0,
        });
    }
    let config =
        retrieval_embedding_config(&environment, &cwd).map_err(|error| error.to_string())?;
    let database = RetrievalDb::open_at(&path).map_err(|error| error.to_string())?;
    let documents = database
        .list_pending_documents()
        .map_err(|error| error.to_string())?;
    if documents.is_empty() {
        return Ok(ReembedReport {
            resolved_documents: 0,
            pending_documents: 0,
        });
    }
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|error| format!("create local embedding runtime: {error}"))?;
    let mut resolved_documents = 0;
    let mut expected_dimension = config.embedding_dim;
    for document in documents {
        let extension = Path::new(&document.path)
            .extension()
            .and_then(|value| value.to_str())
            .unwrap_or_default();
        let markdown =
            extension.eq_ignore_ascii_case("md") || extension.eq_ignore_ascii_case("markdown");
        let plain_text = extension.eq_ignore_ascii_case("txt");
        if !markdown && !plain_text {
            eprintln!(
                "Warning: failed to re-parse {} for re-embed: Rust re-embed currently supports Markdown and UTF-8 plain text only",
                document.path
            );
            continue;
        }
        let bytes = match if plain_text {
            read_limited_plain_text(&document.path)
        } else {
            fs::read(&document.path).map_err(|error| error.to_string())
        } {
            Ok(bytes) => bytes,
            Err(error) => {
                eprintln!(
                    "Warning: failed to re-parse {} for re-embed: {error}",
                    document.path
                );
                continue;
            }
        };
        let sections = match if plain_text {
            parse_text_retrieval_sections(&document.path, &bytes)
        } else {
            parse_markdown_retrieval_sections(&document.path, &bytes)
        } {
            Ok(sections) => sections,
            Err(error) => {
                eprintln!(
                    "Warning: failed to re-parse {} for re-embed: {error}",
                    document.path
                );
                continue;
            }
        };
        let chunks = materialize_chunks(&document.path, &sections);
        let texts = chunks
            .iter()
            .map(|chunk| chunk.content.clone())
            .collect::<Vec<_>>();
        let embeddings = runtime.block_on(embed_chunks(&config, &texts, &mut expected_dimension));
        let stored = chunks
            .into_iter()
            .zip(embeddings)
            .map(|(chunk, embedding)| {
                let (embedding, model, pending) = match embedding {
                    Some(vector) => (vector, config.model.clone(), false),
                    _ => (Vec::new(), "local-hash".to_owned(), true),
                };
                let dim = i64::try_from(embedding.len()).unwrap_or(0);
                let norm = embedding
                    .iter()
                    .map(|value| f64::from(*value) * f64::from(*value))
                    .sum::<f64>()
                    .sqrt() as f32;
                StoredRetrievalChunk {
                    id: 0,
                    uuid: chunk.uuid,
                    document_path: document.path.clone(),
                    chunk_index: i64::try_from(chunk.chunk_index).unwrap_or(i64::MAX),
                    content: chunk.content,
                    embedding,
                    hash: chunk.hash,
                    norm,
                    dim,
                    model,
                    char_start: chunk.char_start.and_then(|value| i64::try_from(value).ok()),
                    char_end: chunk.char_end.and_then(|value| i64::try_from(value).ok()),
                    anchor_kind: chunk.anchor_kind,
                    anchor_value: chunk.anchor_value,
                    embedding_pending: pending,
                }
            })
            .collect::<Vec<_>>();
        let document = RetrievalDocument {
            path: document.path,
            hash: symdesk_vault::sha256_hex(&bytes),
            updated_at: OffsetDateTime::now_utc()
                .format(&Rfc3339)
                .unwrap_or_default(),
        };
        if let Err(error) = database.replace_document_chunks(&document, &stored) {
            eprintln!("Warning: failed to re-embed {}: {error}", document.path);
            continue;
        }
        if stored.iter().all(|chunk| !chunk.embedding_pending) {
            resolved_documents += 1;
        }
    }
    let pending_documents = database
        .list_pending_documents()
        .map_err(|error| error.to_string())?
        .len();
    Ok(ReembedReport {
        resolved_documents,
        pending_documents,
    })
}

fn read_limited_plain_text(path: &str) -> Result<Vec<u8>, String> {
    let file = fs::File::open(path).map_err(|error| error.to_string())?;
    let mut bytes = Vec::new();
    file.take((MAX_RETRIEVAL_SOURCE_BYTES + 1) as u64)
        .read_to_end(&mut bytes)
        .map_err(|error| error.to_string())?;
    if bytes.len() > MAX_RETRIEVAL_SOURCE_BYTES {
        return Err(format!(
            "file {path} exceeds {MAX_RETRIEVAL_SOURCE_BYTES} byte limit ({} bytes)",
            bytes.len()
        ));
    }
    Ok(bytes)
}

async fn embed_chunks(
    config: &RetrievalEmbeddingConfig,
    texts: &[String],
    expected_dimension: &mut Option<usize>,
) -> Vec<Option<Vec<f32>>> {
    if texts.is_empty() {
        return Vec::new();
    }
    match embed_with_retries(config, texts).await {
        Ok(vectors) if vectors.len() == texts.len() => {
            if expected_dimension.is_none()
                && let Some(first) = vectors.first()
            {
                *expected_dimension = Some(if first.is_empty() { 768 } else { first.len() });
            }
            let expected = (*expected_dimension).unwrap_or(768);
            vectors
                .into_iter()
                .map(|vector| (vector.len() == expected).then_some(vector))
                .collect()
        }
        batch_result => {
            if let Err(error) = &batch_result {
                eprintln!(
                    "Warning: batch embedding failed ({error}); falling back to per-text requests"
                );
            }
            let mut vectors = Vec::with_capacity(texts.len());
            for text in texts {
                let one = std::slice::from_ref(text);
                let vector = match embed_with_retries(config, one).await {
                    Ok(mut result) if result.len() == 1 => {
                        let vector = result.pop().unwrap_or_default();
                        if expected_dimension.is_none() {
                            *expected_dimension =
                                Some(if vector.is_empty() { 768 } else { vector.len() });
                        }
                        (vector.len() == (*expected_dimension).unwrap_or(768)).then_some(vector)
                    }
                    Ok(_) => None,
                    Err(error) => {
                        eprintln!("Warning: embedding request failed: {error}");
                        None
                    }
                };
                vectors.push(vector);
            }
            vectors
        }
    }
}

async fn embed_with_retries(
    config: &RetrievalEmbeddingConfig,
    inputs: &[String],
) -> Result<Vec<Vec<f32>>, LocalEmbeddingError> {
    let mut backoff = config.retry_backoff_ms;
    let mut last_error = None;
    for attempt in 0..=config.retry_count {
        if attempt > 0 {
            tokio::time::sleep(Duration::from_millis(backoff)).await;
            backoff = backoff.saturating_mul(2).min(8_000);
        }
        match embed_local_ollama(
            &config.ollama_url,
            &config.model,
            inputs,
            config.embedding_dim,
            Duration::from_secs(config.timeout_seconds),
        )
        .await
        {
            Ok(vectors) => return Ok(vectors),
            Err(error) if error.is_transient() && attempt < config.retry_count => {
                last_error = Some(error);
            }
            Err(error) => return Err(error),
        }
    }
    Err(last_error.unwrap_or(LocalEmbeddingError::EmptyInputs))
}

fn index_vault_error(requested: Option<&str>, error: &str) -> String {
    if !error.contains("No such file or directory") && !error.contains("os error 2") {
        return error.to_owned();
    }
    let raw = requested
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
        .or_else(|| std::env::var("SYMDESK_VAULT").ok())
        .unwrap_or_default();
    if raw.is_empty() {
        return error.to_owned();
    }
    let path = PathBuf::from(raw);
    let absolute = if path.is_absolute() {
        path
    } else if let Ok(cwd) = std::env::current_dir() {
        cwd.join(path)
    } else {
        path
    };
    format!(
        "vault path does not exist: stat {}: no such file or directory",
        super::lexical_clean(&absolute).display()
    )
}

fn indexed_paths(root: &Path) -> Result<BTreeMap<String, String>, String> {
    let path = symdesk_index::path_for_vault(root).map_err(|error| error.to_string())?;
    if !path.exists() {
        return Ok(BTreeMap::new());
    }
    let connection = Connection::open(path).map_err(|error| error.to_string())?;
    let mut statement = connection
        .prepare("SELECT path, sha256 FROM files")
        .map_err(|error| error.to_string())?;
    let rows = statement
        .query_map([], |row| {
            Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
        })
        .map_err(|error| error.to_string())?;
    rows.collect::<Result<BTreeMap<_, _>, _>>()
        .map_err(|error| error.to_string())
}

fn resolve_location(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<PathBuf, String> {
    index_location_for_vault(vault_root, environment, cwd, temp_root)
        .map_err(|error| error.to_string())
}

fn backup_index(source: &Path, destination: &Path) -> Result<(), String> {
    let metadata =
        fs::metadata(source).map_err(|error| format!("stat retrieval index: {error}"))?;
    if !metadata.is_file() {
        return Err(format!(
            "retrieval index is not a regular file: {}",
            source.display()
        ));
    }
    let connection = Connection::open(source)
        .map_err(|error| format!("open retrieval index for snapshot: {error}"))?;
    backup_database(&connection, destination).map_err(|error| error.to_string())
}

fn emit_result(result: serde_json::Value, json_output: bool) -> ExitCode {
    let object = result.as_object().expect("command result is an object");
    let sorted = object.iter().collect::<BTreeMap<_, _>>();
    if json_output {
        let mut rendered = match serde_json::to_string(&sorted) {
            Ok(value) => value,
            Err(error) => return super::emit_error(error.to_string(), true),
        };
        rendered.push('\n');
        super::write_stdout(rendered)
    } else {
        let fields = sorted
            .iter()
            .map(|(key, value)| format!("{key}:{}", value.as_str().unwrap_or("")))
            .collect::<Vec<_>>()
            .join(" ");
        super::write_stdout(format!("map[{fields}]\n"))
    }
}

#[cfg(test)]
mod plain_text_read_tests {
    use super::{MAX_RETRIEVAL_SOURCE_BYTES, read_limited_plain_text};
    use std::{fs, time::SystemTime};

    #[test]
    fn bounded_reader_rejects_over_limit_file_without_loading_it_whole() {
        let nonce = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-text-reembed-limit-{}-{nonce}.txt",
            std::process::id()
        ));
        fs::write(&path, vec![b'x'; MAX_RETRIEVAL_SOURCE_BYTES + 1])
            .expect("write generated over-limit text");
        let error = read_limited_plain_text(path.to_str().expect("UTF-8 temp path"))
            .expect_err("over-limit text is rejected");
        let _ = fs::remove_file(path);
        assert!(error.contains("exceeds 10485760 byte limit (10485761 bytes)"));
    }
}
