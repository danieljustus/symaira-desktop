use std::{path::Path, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use serde_json::json;
use symdesk_index::{SourceRegistry, open_for_vault};

pub fn cli() -> Command {
    Command::new("sources")
        .visible_alias("source")
        .subcommand_required(true)
        .subcommand(Command::new("add").arg(Arg::new("path").required(true)))
        .subcommand(Command::new("list"))
        .subcommand(Command::new("remove").arg(Arg::new("id-or-path").required(true)))
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let vault = match super::resolve_vault(vault) {
        Ok(path) => path,
        Err(error) => return super::emit_error(error, json_output),
    };
    let registry = match SourceRegistry::open(&vault) {
        Ok(registry) => registry,
        Err(error) => return super::emit_error(error.to_string(), json_output),
    };
    match command.subcommand() {
        Some(("add", args)) => {
            let Some(path) = args.get_one::<String>("path") else {
                return super::emit_error("source folder is required".to_owned(), json_output);
            };
            let source = match registry.add(Path::new(path)) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            let mut sidecar = match open_for_vault(&vault) {
                Ok(sidecar) => sidecar,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            if let Err(error) = sidecar.refresh_external_source(Path::new(&source.path)) {
                return super::emit_error(
                    format!("index external source {}: {error}", source.path),
                    json_output,
                );
            }
            emit(
                &json!({"status":"indexed", "source":source}),
                format!("indexed {} ({})\n", source.path, source.id),
                json_output,
            )
        }
        Some(("list", _)) => match registry.list() {
            Ok(sources) => {
                if json_output {
                    super::write_go_json(&sources)
                } else {
                    super::write_stdout(format!(
                        "{}\n",
                        sources
                            .iter()
                            .map(|source| format!("{} {}", source.id, source.path))
                            .collect::<Vec<_>>()
                            .join("\n")
                    ))
                }
            }
            Err(error) => super::emit_error(error.to_string(), json_output),
        },
        Some(("remove", args)) => {
            let Some(id_or_path) = args.get_one::<String>("id-or-path") else {
                return super::emit_error("source id or path is required".to_owned(), json_output);
            };
            let source = match registry.list().and_then(|sources| {
                sources
                    .into_iter()
                    .find(|source| source.id == *id_or_path || source.path == *id_or_path)
                    .ok_or_else(|| {
                        symdesk_index::SidecarError::Contract(format!(
                            "external source {id_or_path:?} not registered"
                        ))
                    })
            }) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            let mut sidecar = match open_for_vault(&vault) {
                Ok(sidecar) => sidecar,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            let removed = match sidecar.remove_external_source(Path::new(&source.path)) {
                Ok(removed) => removed,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            if let Err(error) = registry.remove(&source.id) {
                return super::emit_error(error.to_string(), json_output);
            }
            emit(
                &json!({"status":"removed", "source":source, "removed":removed}),
                format!("removed {} ({} indexed documents)\n", source.path, removed),
                json_output,
            )
        }
        _ => super::emit_error("unknown sources subcommand".to_owned(), json_output),
    }
}

fn emit<T: Serialize>(value: &T, text: String, json_output: bool) -> ExitCode {
    if json_output {
        super::write_go_json(value)
    } else {
        super::write_stdout(text)
    }
}
