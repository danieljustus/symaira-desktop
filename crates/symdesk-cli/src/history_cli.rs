//! Snapshot inspection and retention over the vault-local history store.
use clap::{Arg, ArgMatches, Command};
use serde_json::json;
use std::process::ExitCode;
use symdesk_index::open_for_vault;
use symdesk_vault::{HistoryStore, history::HistoryRetentionPolicy, secure_path};

use crate::{emit_error, relative_path, resolve_vault, write_go_json, write_stdout};

fn positions() -> Arg {
    Arg::new("args").num_args(0..)
}
fn integer(name: &'static str) -> Arg {
    Arg::new(name)
        .long(name)
        .num_args(1)
        .allow_hyphen_values(true)
        .overrides_with(name)
}
pub fn cli() -> Command {
    Command::new("history")
        .arg(positions())
        .subcommand(Command::new("tasks").arg(positions()))
        .subcommand(Command::new("show").arg(positions()))
        .subcommand(
            Command::new("prune")
                .arg(positions())
                .arg(integer("max-per-file"))
                .arg(integer("max-age-days"))
                .arg(integer("checkpoint-age-days")),
        )
}
pub fn trash_cli() -> Command {
    Command::new("trash")
        .subcommand(Command::new("restore").arg(positions()))
        .subcommand(Command::new("list").arg(positions()))
        .subcommand(
            Command::new("purge")
                .arg(positions())
                .arg(integer("older-than-days"))
                .arg(Arg::new("all").long("all").action(clap::ArgAction::SetTrue)),
        )
}
fn args(command: &ArgMatches) -> Vec<&str> {
    command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .map(String::as_str)
        .collect()
}
fn exact<'a>(command: &'a ArgMatches, n: usize, path: &str) -> Result<Vec<&'a str>, String> {
    let values = args(command);
    if values.len() == n {
        Ok(values)
    } else if n == 0 {
        Err(format!(
            "unknown command {} for {}",
            symdesk_vault::go_quote(values[0]),
            symdesk_vault::go_quote(path)
        ))
    } else {
        Err(format!("accepts {n} arg(s), received {}", values.len()))
    }
}
fn int(command: &ArgMatches, name: &str, default: i64) -> Result<i64, String> {
    command.get_one::<String>(name).map_or(Ok(default), |raw| {
        crate::duplicates_cli::parse_go_int(raw).map_err(|why| {
            format!(
                "invalid argument {} for \"--{name}\" flag: strconv.ParseInt: parsing {}: {why}",
                symdesk_vault::go_quote(raw),
                symdesk_vault::go_quote(raw)
            )
        })
    })
}
fn days(value: i64) -> time::Duration {
    // Go's time.Duration multiplications wrap at signed 64-bit nanoseconds.
    time::Duration::nanoseconds(value.wrapping_mul(24).wrapping_mul(3_600_000_000_000))
}
fn date(timestamp: time::OffsetDateTime) -> String {
    timestamp
        .to_offset(crate::local_offset_at(timestamp))
        .format(
            &time::format_description::parse_borrowed::<2>(
                "[year]-[month]-[day] [hour]:[minute]:[second]",
            )
            .expect("static format"),
        )
        .unwrap_or_default()
}
pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool, trash: bool) -> ExitCode {
    let (operation, command) = command.subcommand().unwrap_or(("list", command));
    if trash && operation == "restore" {
        return crate::restore_cli::run(command, vault, output_json, true);
    }
    if !trash && operation == "tasks" {
        return match exact(command, 0, "symdesk history tasks") {
            Ok(_) => crate::history::run_tasks(vault, output_json),
            Err(e) => emit_error(e, output_json),
        };
    }
    let result = (|| -> Result<ExitCode, String> {
        let path = format!(
            "symdesk {} {operation}",
            if trash { "trash" } else { "history" }
        );
        let values = exact(command, usize::from(!trash && operation != "prune"), &path)?;
        let (config, _) = crate::config_paths::load_root_config()?;
        let per_file = if !trash && operation == "prune" {
            int(command, "max-per-file", config.history_max_per_file)?
        } else {
            0
        };
        let max_age = if !trash && operation == "prune" {
            int(command, "max-age-days", config.history_max_age_days)?
        } else {
            0
        };
        let checkpoint_age = if !trash && operation == "prune" {
            int(
                command,
                "checkpoint-age-days",
                config.history_checkpoint_max_age_days,
            )?
        } else {
            0
        };
        let trash_age = if trash && operation == "purge" {
            int(command, "older-than-days", config.trash_retention_days)?
        } else {
            0
        };
        let root = resolve_vault(vault).map_err(|e| {
            if e.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                crate::index_cli::index_vault_error(vault, &e)
            }
        })?;
        let mut sidecar = open_for_vault(&root).map_err(|e| e.to_string())?;
        let store = HistoryStore::new(&root);
        let result = (|| -> Result<ExitCode, String> {
            if trash {
                if operation == "purge" {
                    let removed = store
                        .trash_purge(if command.get_flag("all") {
                            time::Duration::ZERO
                        } else {
                            days(trash_age)
                        })
                        .map_err(|e| e.to_string())?;
                    return Ok(if output_json {
                        write_go_json(&json!({"removed":removed,"status":"purged"}))
                    } else {
                        write_stdout(format!("map[removed:{removed} status:purged]\n"))
                    });
                }
                let entries = store.trash_list().map_err(|e| e.to_string())?;
                return Ok(if output_json {
                    if entries.is_empty() {
                        write_stdout("null\n".to_owned())
                    } else {
                        write_go_json(&entries)
                    }
                } else if entries.is_empty() {
                    write_stdout("trash is empty\n".to_owned())
                } else {
                    write_stdout(
                        entries
                            .iter()
                            .map(|e| {
                                format!(
                                    "{}  {}  (deleted {})\n",
                                    e.name,
                                    e.original_path,
                                    date(e.deleted_at)
                                )
                            })
                            .collect(),
                    )
                });
            }
            match operation {
                "prune" => {
                    let removed = store
                        .prune(HistoryRetentionPolicy {
                            max_per_file: per_file,
                            max_age: days(max_age),
                            max_checkpoint_age: days(checkpoint_age),
                        })
                        .map_err(|e| e.to_string())?;
                    Ok(if output_json {
                        write_go_json(&json!({"removed":removed,"status":"pruned"}))
                    } else {
                        write_stdout(format!("map[removed:{removed} status:pruned]\n"))
                    })
                }
                "show" => {
                    let content = store.content(values[0]).map_err(|e| e.to_string())?;
                    Ok(if output_json {
                        write_go_json(
                            &json!({"content":String::from_utf8_lossy(&content), "id":values[0]}),
                        )
                    } else {
                        crate::write_stdout_bytes(&content)
                    })
                }
                _ => {
                    let path = secure_path(&root, values[0]).map_err(|e| e.to_string())?;
                    let rel = relative_path(&root, &path.to_string_lossy());
                    let entries = store.list(&rel).map_err(|e| e.to_string())?;
                    Ok(if output_json {
                        write_go_json(&entries)
                    } else if entries.as_ref().is_none_or(Vec::is_empty) {
                        write_stdout(format!("no snapshots recorded for {}\n", values[0]))
                    } else {
                        write_stdout(
                            entries
                                .unwrap_or_default()
                                .iter()
                                .map(|e| {
                                    format!(
                                        "{}  {}  {:6} bytes\n",
                                        &e.id[..12.min(e.id.len())],
                                        date(e.timestamp),
                                        e.size
                                    )
                                })
                                .collect(),
                        )
                    })
                }
            }
        })();
        if let Err(e) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {e}");
        }
        result
    })();
    match result {
        Ok(exit) => exit,
        Err(e) => emit_error(e, output_json),
    }
}
