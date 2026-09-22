//! Go: `cmd/symdesk/retention.go`.
//!
//! This module ports the retention commands that only use the sidecar open and
//! `symdesk_vault::retention` state APIs: `list`, `reject`, `diff`, and
//! `history`. `eval` and `accept` remain out of scope because they require the
//! service mutation layer.

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};

use clap::{Arg, ArgAction, Command};
use serde_json::{Value, json};

use symdesk_index::open_for_vault;
use symdesk_vault::retention::{
    PROPOSAL_STATUS_FAILED, PROPOSAL_STATUS_PARTIAL, PROPOSAL_STATUS_PENDING, Proposal,
    ProposalItem, history_path, load_history, load_proposal, proposal_dir, write_proposal,
};

use crate::{emit_error, write_go_json, write_stdout};

/// Go: `newRetentionCmd`.
pub fn cli() -> Command {
    Command::new("retention")
        .about("Evaluate and apply document retention rules")
        .arg(
            Arg::new("verbose")
                .long("verbose")
                .action(ArgAction::SetTrue),
        )
        .subcommand(Command::new("list").about("List documents due to expire"))
        .subcommand(
            Command::new("reject")
                .about("Reject a pending retention proposal")
                .arg(Arg::new("run-id").num_args(0..).action(ArgAction::Append)),
        )
        .subcommand(
            Command::new("diff")
                .about("Show the proposed retention actions")
                .arg(Arg::new("run-id").num_args(0..).action(ArgAction::Append)),
        )
        .subcommand(
            Command::new("history")
                .about("Show the history of executed retention actions")
                .arg(Arg::new("extra").num_args(0..).action(ArgAction::Append)),
        )
}

/// Go: `newRetentionListCmd`'s `RunE`.
pub fn run_list(vault: Option<&str>, output_json: bool) -> std::process::ExitCode {
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };

    let dir = proposal_dir(&vault_root);
    let mut entries = match fs::read_dir(&dir) {
        Ok(entries) => entries,
        Err(error) => {
            if error.kind() == std::io::ErrorKind::NotFound {
                // Go: `outputResult(map[string]interface{}{"proposals": []string{}, "message": "no pending proposals"})`
                return output(
                    json!({"proposals": Vec::<String>::new(), "message": "no pending proposals"}),
                    output_json,
                );
            }
            return emit_error(format!("open {}: {error}", dir.display()), output_json);
        }
    };

    // Go's `os.ReadDir` returns entries sorted by file name.
    let mut sorted: Vec<_> = entries.by_ref().flatten().collect();
    sorted.sort_by_key(|entry| entry.file_name());

    let mut proposals: Vec<Proposal> = Vec::new();
    for entry in sorted {
        let name = entry.file_name();
        let name = name.to_string_lossy().to_string();
        let path = entry.path();
        if path.is_dir() || path.extension().and_then(|ext| ext.to_str()) != Some("json") {
            continue;
        }
        let Some(run_id) = name.strip_suffix(".json") else {
            continue;
        };
        let proposal = match load_proposal(&vault_root, run_id) {
            Ok(proposal) => proposal,
            // Go skips an unreadable or undecodable proposal.
            Err(_) => continue,
        };
        if !matches!(
            proposal.status.as_str(),
            PROPOSAL_STATUS_PENDING | PROPOSAL_STATUS_FAILED | PROPOSAL_STATUS_PARTIAL
        ) {
            continue;
        }
        proposals.push(proposal);
    }

    if output_json {
        if proposals.is_empty() {
            return write_stdout("null\n".to_owned());
        }
        return write_go_json(&proposals);
    }

    if proposals.is_empty() {
        return write_stdout("no pending retention proposals\n".to_owned());
    }

    let mut rendered = String::new();
    for proposal in &proposals {
        rendered.push_str(&format!(
            "Proposal {} ({}): {} items pending review\n",
            proposal.run_id,
            go_local_time(proposal.created, false),
            proposal.items.as_ref().map_or(0, Vec::len)
        ));
        for item in proposal.items.as_deref().unwrap_or(&[]) {
            rendered.push_str(&format!(
                "  {} — expires {} → {}\n",
                item.path, item.expires_at, item.action
            ));
        }
    }
    write_stdout(rendered)
}

/// Go: `newRetentionRejectCmd`'s `RunE`.
pub fn run_reject(
    vault: Option<&str>,
    run_ids: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    let run_id = match exact_one(run_ids, output_json) {
        Ok(run_id) => run_id,
        Err(exit) => return exit,
    };
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };
    let mut proposal = match load_proposal_for_cli(&vault_root, run_id) {
        Ok(proposal) => proposal,
        Err(error) => return emit_error(error, output_json),
    };
    proposal.status = "rejected".to_owned();
    if let Err(error) = write_proposal(&vault_root, &proposal) {
        return emit_error(error.to_string(), output_json);
    }

    if output_json {
        let result = BTreeMap::from([("run_id", run_id), ("status", "rejected")]);
        write_go_json(&result)
    } else {
        write_stdout(format!("map[run_id:{run_id} status:rejected]\n"))
    }
}

/// Go: `newRetentionDiffCmd`'s `RunE`.
pub fn run_diff(
    vault: Option<&str>,
    run_ids: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    let run_id = match exact_one(run_ids, output_json) {
        Ok(run_id) => run_id,
        Err(exit) => return exit,
    };
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };
    let proposal = match load_proposal_for_cli(&vault_root, run_id) {
        Ok(proposal) => proposal,
        Err(error) => return emit_error(error, output_json),
    };

    if output_json {
        return write_go_json(&proposal.items);
    }
    write_stdout(render_items(proposal.items.as_deref().unwrap_or(&[])))
}

/// Go: `newRetentionHistoryCmd`'s `RunE`.
pub fn run_history(
    vault: Option<&str>,
    extra: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    if let Some(extra) = extra.first() {
        return emit_error(
            format!("unknown command {extra:?} for \"symdesk retention history\""),
            output_json,
        );
    }
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };
    let path = history_path(&vault_root);
    let missing =
        fs::metadata(&path).is_err_and(|error| error.kind() == std::io::ErrorKind::NotFound);
    let entries = match load_history(&vault_root) {
        Ok(entries) => entries,
        Err(error) => return emit_error(error.to_string(), output_json),
    };

    if output_json {
        if missing {
            return write_stdout("null\n".to_owned());
        }
        return write_go_json(&entries);
    }
    if entries.is_empty() {
        return write_stdout("no retention actions recorded\n".to_owned());
    }

    let mut rendered = String::new();
    for entry in &entries {
        rendered.push_str(&format!(
            "{}  {}  {} → {}\n",
            go_local_time(entry.timestamp, true),
            entry.rule_name,
            entry.path,
            entry.action
        ));
    }
    write_stdout(rendered)
}

fn retention_vault(
    vault: Option<&str>,
    output_json: bool,
) -> Result<PathBuf, std::process::ExitCode> {
    let vault_root = crate::resolve_vault(vault).map_err(|error| emit_error(error, output_json))?;
    // Go discards these `*sidecar.DB` handles without closing them for list,
    // reject, diff, and history. Keep the Rust connection alive until process
    // exit so metadata.json plus sidecar.db-wal/sidecar.db-shm match Go.
    let sidecar =
        open_for_vault(&vault_root).map_err(|error| emit_error(error.to_string(), output_json))?;
    std::mem::forget(sidecar);
    Ok(vault_root)
}

fn exact_one(values: &[String], output_json: bool) -> Result<&str, std::process::ExitCode> {
    if values.len() != 1 {
        return Err(emit_error(
            format!("accepts 1 arg(s), received {}", values.len()),
            output_json,
        ));
    }
    Ok(values[0].as_str())
}

fn load_proposal_for_cli(vault_root: &Path, run_id: &str) -> Result<Proposal, String> {
    load_proposal(vault_root, run_id).map_err(|error| error.to_string())
}

fn render_items(items: &[ProposalItem]) -> String {
    let rendered = items
        .iter()
        .map(|item| {
            format!(
                "{{Path:{} Title:{} ReferenceDate:{} ExpiresAt:{} Action:{} RuleName:{} Fingerprint:{} Status:{} Failure:{}}}",
                item.path,
                item.title,
                item.reference_date,
                item.expires_at,
                item.action,
                item.rule_name,
                item.fingerprint,
                item.status,
                item.failure
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!("[{rendered}]\n")
}

/// Go: `.Local().Format("2006-01-02 15:04[:05]")`; resolve the offset at the
/// event instant so historical daylight-saving transitions match.
fn go_local_time(value: time::OffsetDateTime, seconds: bool) -> String {
    let offset = time::UtcOffset::local_offset_at(value).unwrap_or(time::UtcOffset::UTC);
    let local = value.to_offset(offset);
    if seconds {
        format!(
            "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
            local.year(),
            u8::from(local.month()),
            local.day(),
            local.hour(),
            local.minute(),
            local.second()
        )
    } else {
        format!(
            "{:04}-{:02}-{:02} {:02}:{:02}",
            local.year(),
            u8::from(local.month()),
            local.day(),
            local.hour(),
            local.minute()
        )
    }
}

fn output(value: Value, output_json: bool) -> std::process::ExitCode {
    if output_json {
        write_go_json(&value)
    } else {
        // Go prints the map with `fmt.Printf("%+v\n", data)`.
        let rendered = match &value {
            Value::Object(map) => {
                let inner = map
                    .iter()
                    .map(|(key, value)| format!("{key}:{}", go_value(value)))
                    .collect::<Vec<_>>()
                    .join(" ");
                format!("map[{inner}]")
            }
            other => go_value(other),
        };
        write_stdout(format!("{rendered}\n"))
    }
}

fn go_value(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Array(items) if items.is_empty() => "[]".to_owned(),
        Value::Array(items) => format!(
            "[{}]",
            items.iter().map(go_value).collect::<Vec<_>>().join(" ")
        ),
        Value::Null => "<nil>".to_owned(),
        other => other.to_string(),
    }
}
