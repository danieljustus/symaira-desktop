use std::process::ExitCode;

use symdesk_index::open_for_vault;
use symdesk_vault::HistoryStore;
use time::format_description;

use crate::{emit_error, local_offset_at, resolve_vault, write_go_json, write_stdout};

pub fn run_tasks(vault: Option<&str>, output_json: bool) -> ExitCode {
    let vault_root = match resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let mut sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    let result = match HistoryStore::new(&vault_root).list_checkpoints() {
        Ok(checkpoints) if output_json && checkpoints.is_empty() => {
            write_stdout("null\n".to_owned())
        }
        Ok(checkpoints) if output_json => write_go_json(&checkpoints),
        Ok(checkpoints) if checkpoints.is_empty() => {
            write_stdout("no task checkpoints\n".to_owned())
        }
        Ok(checkpoints) => {
            let format = format_description::parse_borrowed::<2>(
                "[year]-[month]-[day] [hour]:[minute]:[second]",
            )
            .expect("static checkpoint timestamp format");
            let rendered = checkpoints
                .iter()
                .map(|checkpoint| {
                    let timestamp = checkpoint
                        .timestamp
                        .to_offset(local_offset_at(checkpoint.timestamp))
                        .format(&format)
                        .unwrap_or_default();
                    format!(
                        "{}  {}  {} files, {} new{}",
                        checkpoint.task_id,
                        timestamp,
                        checkpoint.files.len(),
                        checkpoint.new_files.len(),
                        if checkpoint.partial() {
                            " (partial)"
                        } else {
                            ""
                        },
                    )
                })
                .collect::<Vec<_>>()
                .join("\n");
            write_stdout(format!("{rendered}\n"))
        }
        Err(error) => emit_error(error.to_string(), output_json),
    };
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    result
}

/// `symdesk history checkpoint <task-id> [file...]` and
/// `symdesk history undo-task <task-id>` (Go `Service.CheckpointBegin`,
/// `CheckpointFiles` and `CheckpointUndo`).
pub fn run_checkpoint(
    command: &clap::ArgMatches,
    operation: &str,
    vault: Option<&str>,
    output_json: bool,
) -> ExitCode {
    let args: Vec<&String> = command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .collect();
    crate::service_cli::with_deps(vault, output_json, |root, sidecar| {
        let store = HistoryStore::new(root);
        let checkpoint = if operation == "checkpoint" {
            if args.len() == 1 {
                store.begin_checkpoint(args[0])
            } else {
                let canonical = std::fs::canonicalize(root).map_err(|error| error.to_string())?;
                store
                    .begin_checkpoint(args[0])
                    .map_err(|error| error.to_string())?;
                let mut current = None;
                for file in &args[1..] {
                    let path = symdesk_vault::secure_path(root, file)
                        .map_err(|error| error.to_string())?;
                    let relative = path
                        .strip_prefix(&canonical)
                        .map_err(|error| error.to_string())?
                        .to_string_lossy()
                        .into_owned();
                    current = Some(
                        store
                            .checkpoint_file(args[0], &relative)
                            .map_err(|error| error.to_string())?,
                    );
                }
                Ok(current.expect("at least one file"))
            }
        } else {
            store.undo_checkpoint(args[0])
        }
        .map_err(|error| match error {
            // Go reports the os.Root open of the missing manifest.
            symdesk_vault::HistoryError::Io(io)
                if io.kind() == std::io::ErrorKind::NotFound && operation == "undo-task" =>
            {
                format!(
                    "openat {}/{}.json: no such file or directory",
                    ".symdesk/history/checkpoints", args[0]
                )
            }
            other => other.to_string(),
        })?;
        if operation == "undo-task" {
            reindex_after_undo(root, sidecar, &checkpoint);
        }
        Ok(if output_json {
            write_go_json(&checkpoint)
        } else {
            let verb = if operation == "checkpoint" {
                format!(
                    "checkpoint {}: {} files, {} new, {} skipped\n",
                    checkpoint.task_id,
                    checkpoint.files.len(),
                    checkpoint.new_files.len(),
                    checkpoint.skipped.len()
                )
            } else {
                format!(
                    "undid task {}: {} files restored, {} new files deleted, {} skipped\n",
                    checkpoint.task_id,
                    checkpoint.files.len(),
                    checkpoint.new_files.len(),
                    checkpoint.skipped.len()
                )
            };
            let warning = if checkpoint.partial() {
                format!(
                    "warning: partial {} — skipped: [{}]\n",
                    if operation == "checkpoint" {
                        "checkpoint"
                    } else {
                        "undo"
                    },
                    checkpoint.skipped.join(" ")
                )
            } else {
                String::new()
            };
            write_stdout(format!("{verb}{warning}"))
        })
    })
}

/// Re-indexes every restored Markdown file and drops the files the task
/// created, so the sidecar matches the vault again (errors are ignored like
/// in Go).
fn reindex_after_undo(
    root: &std::path::Path,
    sidecar: &mut symdesk_index::Sidecar,
    checkpoint: &symdesk_vault::history::Checkpoint,
) {
    for file in &checkpoint.files {
        let Ok(path) = symdesk_vault::secure_path(root, &file.rel_path) else {
            continue;
        };
        if path.extension().is_some_and(|extension| extension == "md") {
            let _ = crate::mutate_cli::reindex(sidecar, &path);
        }
    }
    for relative in &checkpoint.new_files {
        let Ok(path) = symdesk_vault::secure_path(root, relative) else {
            continue;
        };
        if let Ok(key) = symdesk_index::document_storage_key(&path) {
            let _ = sidecar.delete_document(&key.to_string_lossy());
        }
    }
}
