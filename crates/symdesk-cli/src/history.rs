use std::process::ExitCode;

use symdesk_index::open_for_vault;
use symdesk_vault::HistoryStore;
use time::{UtcOffset, format_description};

use crate::{emit_error, resolve_vault, write_go_json, write_stdout};

pub fn run_tasks(vault: Option<&str>, output_json: bool) -> ExitCode {
    let vault_root = match resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    // Match Go's history command, which leaves the sidecar open through process exit.
    std::mem::forget(sidecar);
    let checkpoints = match HistoryStore::new(&vault_root).list_checkpoints() {
        Ok(checkpoints) => checkpoints,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    if output_json {
        return write_go_json(&checkpoints);
    }
    if checkpoints.is_empty() {
        return write_stdout("no task checkpoints\n".to_owned());
    }
    let format = format_description::parse("[year]-[month]-[day] [hour]:[minute]:[second]")
        .expect("static checkpoint timestamp format");
    let rendered = checkpoints
        .iter()
        .map(|checkpoint| {
            let offset = UtcOffset::local_offset_at(checkpoint.timestamp).unwrap_or(UtcOffset::UTC);
            let timestamp = checkpoint
                .timestamp
                .to_offset(offset)
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
