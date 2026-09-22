//! Go: `cmd/symdesk/retention.go`.
//!
//! Scope: this module ports the `retention list` subcommand. Go's `eval`,
//! `accept`, `reject`, `diff` and `history` read and mutate authoritative state
//! through `internal/service` (sidecar index, retention fingerprints and the
//! trash/purge actions); that service layer is not ported yet, so those
//! subcommands are deliberately absent here rather than approximated. They stay
//! open in the port ledger.
//!
//! The JSON branch is compared byte-for-byte against the Go binary by
//! `make retention-cli-differential`; the human-readable branch mirrors Go's
//! format strings but is not covered by a differential case yet.

use std::fs;

use clap::{Arg, Command};
use serde_json::{Value, json};

use symdesk_vault::retention::{
    PROPOSAL_STATUS_FAILED, PROPOSAL_STATUS_PARTIAL, PROPOSAL_STATUS_PENDING, Proposal,
    load_proposal, proposal_dir,
};

use symdesk_index::open_for_vault;

use crate::{emit_error, write_stdout};

/// Go: `newRetentionCmd`.
pub fn cli() -> Command {
    Command::new("retention")
        .about("Evaluate and apply document retention rules")
        .arg(
            Arg::new("verbose")
                .long("verbose")
                .action(clap::ArgAction::SetTrue),
        )
        .subcommand(Command::new("list").about("List documents due to expire"))
}

/// Go: `newRetentionListCmd`'s `RunE`.
pub fn run_list(vault: Option<&str>, output_json: bool) -> std::process::ExitCode {
    let vault_root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };

    // Go's `initServiceDeps` opens (and therefore creates) the vault's sidecar
    // index before the command runs, and closes it afterwards. The handle is
    // unused here, but the filesystem side effect is part of the contract the
    // differential compares.
    let sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
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
        // Go skips directories and anything that is not a `.json` file.
        if path.is_dir() || path.extension().and_then(|ext| ext.to_str()) != Some("json") {
            continue;
        }
        let Some(run_id) = name.strip_suffix(".json") else {
            continue;
        };
        let proposal = match load_proposal(&vault_root, run_id) {
            Ok(proposal) => proposal,
            // Go: `if err != nil { continue }` — an unreadable proposal is skipped.
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

    // Go closes the handle here; a close failure is only a warning there.
    drop(sidecar);

    if output_json {
        // Go marshals the `[]retention.Proposal` slice, so the struct field
        // order decides the byte order of every object.
        return match serde_json::to_string(&proposals) {
            Ok(rendered) => write_stdout(format!("{rendered}\n")),
            Err(error) => emit_error(error.to_string(), true),
        };
    }

    if proposals.is_empty() {
        return write_stdout("no pending retention proposals\n".to_owned());
    }

    let mut rendered = String::new();
    for proposal in &proposals {
        rendered.push_str(&format!(
            "Proposal {} ({}): {} items pending review\n",
            proposal.run_id,
            go_local_minutes(proposal.created),
            proposal.items.len()
        ));
        for item in &proposal.items {
            rendered.push_str(&format!(
                "  {} — expires {} → {}\n",
                item.path, item.expires_at, item.action
            ));
        }
    }
    write_stdout(rendered)
}

/// Go: `.Local().Format("2006-01-02 15:04")`. Falls back to UTC when the local
/// offset cannot be determined.
fn go_local_minutes(value: time::OffsetDateTime) -> String {
    let offset = time::UtcOffset::current_local_offset().unwrap_or(time::UtcOffset::UTC);
    let local = value.to_offset(offset);
    format!(
        "{:04}-{:02}-{:02} {:02}:{:02}",
        local.year(),
        u8::from(local.month()),
        local.day(),
        local.hour(),
        local.minute()
    )
}

fn output(value: Value, output_json: bool) -> std::process::ExitCode {
    if output_json {
        write_stdout(format!("{value}\n"))
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
