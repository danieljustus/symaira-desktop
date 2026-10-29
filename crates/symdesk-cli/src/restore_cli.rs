//! Snapshot and trash restoration through the existing vault/index adapters.
use crate::{emit_error, resolve_vault, write_go_json, write_stdout};
use clap::{Arg, ArgMatches, Command};
use serde_json::json;
use std::{fs, path::Path, process::ExitCode};
use symdesk_index::{IndexedDocument, open_for_vault};
use symdesk_vault::{HistoryStore, activity_journal::append_activity, parse_bytes, secure_path};

pub fn cli() -> Command {
    Command::new("restore")
        .arg(Arg::new("args").num_args(0..))
        .arg(Arg::new("at").long("at").num_args(1).overrides_with("at"))
}
fn reindex(root: &Path, path: &Path, sidecar: &mut symdesk_index::Sidecar) -> Result<(), String> {
    use std::io::Read;
    let root = fs::canonicalize(root).map_err(|e| e.to_string())?;
    let relative = path.strip_prefix(&root).map_err(|e| e.to_string())?;
    let vault = cap_std::fs::Dir::open_ambient_dir(&root, cap_std::ambient_authority())
        .map_err(|e| format!("restored file but failed to parse for indexing: {e}"))?;
    let mut file = vault
        .open(relative)
        .map_err(|e| format!("restored file but failed to parse for indexing: {e}"))?;
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)
        .map_err(|e| format!("restored file but failed to parse for indexing: {e}"))?;
    let key = symdesk_index::document_storage_key(path).map_err(|e| e.to_string())?;
    let doc = parse_bytes(&key.to_string_lossy(), &bytes)
        .map_err(|e| format!("restored file but failed to parse for indexing: {e}"))?;
    let mtime = file
        .metadata()
        .and_then(|m| m.modified())
        .ok()
        .map(|t| time::OffsetDateTime::from(t.into_std()))
        .and_then(|t| i64::try_from(t.unix_timestamp_nanos()).ok());
    let indexed = IndexedDocument::from_vault(&doc, mtime)
        .map_err(|e| format!("restored file but failed to re-index: {e}"))?;
    sidecar
        .index_document(&indexed)
        .map_err(|e| format!("restored file but failed to re-index: {e}"))
}
pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool, trash: bool) -> ExitCode {
    let args: Vec<_> = command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .collect();
    if args.len() != 1 {
        return emit_error(
            format!("accepts 1 arg(s), received {}", args.len()),
            output_json,
        );
    }
    let result = (|| -> Result<ExitCode, String> {
        let root = resolve_vault(vault).map_err(|e| {
            if e.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                crate::index_cli::index_vault_error(vault, &e)
            }
        })?;
        let mut sidecar = open_for_vault(&root).map_err(|e| e.to_string())?;
        let result = (|| -> Result<ExitCode, String> {
            if trash {
                let entry = HistoryStore::new(&root)
                    .trash_restore(args[0])
                    .map_err(|e| e.to_string())?;
                let path = secure_path(&root, &entry.original_path).map_err(|e| e.to_string())?;
                if path.extension().is_some_and(|ext| ext == "md") {
                    reindex(&root, &path, &mut sidecar)?;
                }
                let _ = append_activity(
                    &root,
                    "file_added",
                    &entry.original_path,
                    "",
                    "restored from trash",
                );
                return Ok(if output_json {
                    write_go_json(&json!({"file":entry.original_path,"status":"restored"}))
                } else {
                    write_stdout(format!(
                        "map[file:{} status:restored]\n",
                        entry.original_path
                    ))
                });
            }
            let id = command
                .get_one::<String>("at")
                .map(String::as_str)
                .filter(|s| !s.is_empty());
            let entry = symdesk_index::history_restore(&root, &mut sidecar, args[0], id)
                .map_err(|e| e.to_string())?;
            Ok(if output_json {
                write_go_json(
                    &json!({"file":args[0],"id":entry.id,"status":"restored","timestamp":symdesk_vault::history::format_rfc3339_nano(entry.timestamp)}),
                )
            } else {
                let ts = symdesk_vault::history::format_rfc3339_nano(entry.timestamp);
                let timestamp = crate::index_cli::go_time_text(&ts)?;
                write_stdout(format!(
                    "map[file:{} id:{} status:restored timestamp:{timestamp}]\n",
                    args[0], entry.id
                ))
            })
        })();
        if let Err(e) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {e}")
        };
        result
    })();
    match result {
        Ok(exit) => exit,
        Err(e) => emit_error(e, output_json),
    }
}
