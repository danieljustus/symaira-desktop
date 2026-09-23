use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgMatches, Command};
use rusqlite::Connection;
use serde_json::json;
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_index::{
    backup_database, index_location_for_vault, relocate_index_for_vault, restore_database,
};

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
    let Some(("maintenance", maintenance)) = command.subcommand() else {
        return run_build(command, vault, json_output, json_flag);
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

fn run_build(
    command: &ArgMatches,
    vault: Option<&str>,
    json_output: bool,
    json_flag: bool,
) -> ExitCode {
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
    if reembed {
        let reembedded = match reembed_pending_documents() {
            Ok(count) => count,
            Err(error) => {
                return super::emit_error(format!("re-embed failed: {error}"), json_output);
            }
        };
        if !json_flag {
            let code = super::write_stdout(format!(
                "Re-embedded {reembedded} document(s) with pending chunks.\n"
            ));
            if code != super::process_exit(CoreExitCode::Ok) {
                return code;
            }
        }
    }

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
    let mut result = json!({"status":"ok", "indexed":indexed, "skipped":skipped});
    if let Some(count) = pruned {
        result["pruned"] = json!(count);
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

fn reembed_pending_documents() -> Result<usize, String> {
    // Preserve the exact, provider-free no-pending path. Rebuilding pending
    // chunks needs the Go retrieval parser/chunker and embedding engine, which
    // this Rust workspace does not expose yet; fail visibly rather than
    // claiming that pending vectors were repaired.
    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let cwd = std::env::current_dir().map_err(|error| error.to_string())?;
    let temp_root = std::env::temp_dir();
    let path = index_location_for_vault("", &environment, &cwd, &temp_root)
        .map_err(|error| error.to_string())?;
    if !path.exists() {
        return Ok(0);
    }
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
        return Ok(0);
    }
    let pending_documents: i64 = connection
        .query_row(
            "SELECT COUNT(DISTINCT document_path) FROM chunks WHERE embedding_pending=1",
            [],
            |row| row.get(0),
        )
        .map_err(|error| error.to_string())?;
    if pending_documents == 0 {
        return Ok(0);
    }
    Err(format!(
        "{pending_documents} document(s) have pending chunks, but the Rust retrieval embedding engine is not available"
    ))
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
    if json_output {
        let mut rendered = match serde_json::to_string(&result) {
            Ok(value) => value,
            Err(error) => return super::emit_error(error.to_string(), true),
        };
        rendered.push('\n');
        super::write_stdout(rendered)
    } else {
        let object = result.as_object().expect("command result is an object");
        let fields = object
            .iter()
            .map(|(key, value)| format!("{key}:{}", value.as_str().unwrap_or("")))
            .collect::<Vec<_>>()
            .join(" ");
        super::write_stdout(format!("map[{fields}]\n"))
    }
}
