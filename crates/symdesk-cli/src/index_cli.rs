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
    Command::new("index").subcommand(
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
        return super::process_exit(CoreExitCode::Ok);
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
