use std::{
    path::{Path, PathBuf},
    process::ExitCode,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use serde_json::json;
use symdesk_index::{SearchSource, Sidecar, SourceRegistry, open_for_vault};

pub fn cli() -> Command {
    Command::new("sources")
        .visible_alias("source")
        .subcommand_required(true)
        .subcommand(Command::new("add").arg(Arg::new("path").required(true)))
        .subcommand(Command::new("list"))
        .subcommand(Command::new("remove").arg(Arg::new("id-or-path").required(true)))
        .subcommand(Command::new("watch").arg(Arg::new("id-or-path").required(true)))
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
                    if sources.is_empty() {
                        super::write_stdout("null\n".to_owned())
                    } else {
                        super::write_go_json(&sources)
                    }
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
            let source = match find_source(&registry, id_or_path) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error, json_output),
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
        Some(("watch", args)) => {
            let Some(id_or_path) = args.get_one::<String>("id-or-path") else {
                return super::emit_error("source id or path is required".to_owned(), json_output);
            };
            let source = match find_source(&registry, id_or_path) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error, json_output),
            };
            let sidecar = match open_for_vault(&vault) {
                Ok(sidecar) => sidecar,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            run_watch(sidecar, PathBuf::from(source.path), json_output)
        }
        _ => super::emit_error("unknown sources subcommand".to_owned(), json_output),
    }
}

fn find_source(registry: &SourceRegistry, id_or_path: &str) -> Result<SearchSource, String> {
    registry
        .list()
        .map_err(|error| error.to_string())?
        .into_iter()
        .find(|source| source.id == id_or_path || source.path == id_or_path)
        .ok_or_else(|| format!("external source {id_or_path:?} not registered"))
}

fn run_watch(mut sidecar: Sidecar, root: PathBuf, json_output: bool) -> ExitCode {
    let stop = Arc::new(AtomicBool::new(false));
    let worker_stop = Arc::clone(&stop);
    let runtime = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => return super::emit_error(error.to_string(), json_output),
    };
    let mut worker =
        runtime.spawn_blocking(move || sidecar.watch_external_source(&root, &worker_stop));
    let result = runtime.block_on(async {
        tokio::select! {
            result = &mut worker => result.map_err(|error| error.to_string())?.map_err(|error| error.to_string()),
            signal = tokio::signal::ctrl_c() => {
                signal.map_err(|error| error.to_string())?;
                stop.store(true, Ordering::SeqCst);
                worker.await.map_err(|error| error.to_string())?.map_err(|error| error.to_string())
            }
        }
    });
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => super::emit_error(error, json_output),
    }
}

fn emit<T: Serialize>(value: &T, text: String, json_output: bool) -> ExitCode {
    if json_output {
        super::write_go_json(value)
    } else {
        super::write_stdout(text)
    }
}
