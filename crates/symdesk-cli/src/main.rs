#![deny(unsafe_code)]

mod http;
mod mcp;

use std::{
    ffi::OsString,
    io::{self, Write},
    path::{Component, Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgAction, Command};
use serde::Serialize;
use serde_json::json;
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_core::{render_version_json, render_version_text};
use symdesk_index::{ListedDocument, Sidecar, path_for_vault};

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}

const VERSION: &str = match option_env!("SYMDESK_VERSION") {
    Some(version) => version,
    None => "devel",
};

fn main() -> ExitCode {
    let args: Vec<OsString> = std::env::args_os().collect();
    if args.get(1).is_some_and(|arg| arg == "--version") {
        return write_stdout(format!("symdesk version {VERSION}\n"));
    }
    if args.iter().skip(2).any(|arg| arg == "--version") {
        return write_stderr("unknown flag: --version\n", CoreExitCode::Generic);
    }

    let matches = match cli().try_get_matches_from(args) {
        Ok(matches) => matches,
        Err(error) => {
            let _ = error.print();
            return process_exit(CoreExitCode::Generic);
        }
    };
    let output = matches
        .get_one::<String>("output")
        .map_or("", String::as_str);
    if !output.is_empty() && !matches!(output, "text" | "json" | "yaml") {
        return write_stderr(
            &format!("invalid --output value {output:?} (want text|json|yaml)\n"),
            CoreExitCode::Generic,
        );
    }
    let output_json = matches.get_flag("json") || output == "json";
    match matches.subcommand() {
        Some(("version", _)) => {
            let rendered = if output_json {
                match render_version_json("symdesk", VERSION) {
                    Ok(value) => value,
                    Err(_) => return process_exit(CoreExitCode::Generic),
                }
            } else {
                render_version_text("symdesk", VERSION)
            };
            write_stdout(rendered)
        }
        Some(("ls", command)) => run_representative(
            RepresentativeArgs {
                command: Some("ls".to_owned()),
                dir: command.get_one::<String>("dir").cloned(),
                vault: matches.get_one::<String>("vault").cloned(),
                ..RepresentativeArgs::default()
            },
            output_json,
        ),
        Some(("search", command)) => {
            let queries = command
                .get_many::<String>("query")
                .map(|values| values.cloned().collect::<Vec<_>>())
                .unwrap_or_default();
            if queries.len() > 1 {
                return emit_error(
                    format!("accepts at most 1 arg(s), received {}", queries.len()),
                    output_json,
                );
            }
            run_representative(
                RepresentativeArgs {
                    command: Some("search".to_owned()),
                    query: queries.into_iter().next(),
                    vault: matches.get_one::<String>("vault").cloned(),
                    ..RepresentativeArgs::default()
                },
                output_json,
            )
        }
        Some(("mcp", _)) => match mcp::serve(matches.get_one::<String>("vault").cloned()) {
            Ok(()) => process_exit(CoreExitCode::Ok),
            Err(error) => write_stderr(&format!("mcp: {error}\n"), CoreExitCode::Generic),
        },
        Some(("serve", command)) => run_http_server(
            command.get_one::<String>("listen").cloned(),
            command.get_one::<String>("token").cloned(),
            matches.get_one::<String>("vault").cloned(),
        ),
        _ => process_exit(CoreExitCode::Ok),
    }
}

fn run_http_server(
    listen: Option<String>,
    token: Option<String>,
    vault: Option<String>,
) -> ExitCode {
    let vault = match resolve_vault(vault.as_deref()) {
        Ok(path) => path,
        Err(error) => return write_stderr(&format!("http: {error}\n"), CoreExitCode::Generic),
    };
    let token = token
        .filter(|value| !value.is_empty())
        .or_else(|| std::env::var("SYMDESK_SERVER_TOKEN").ok())
        .unwrap_or_default();
    let config = http::HttpConfig {
        listen_address: listen
            .filter(|value| !value.is_empty())
            .or_else(|| std::env::var("SYMDESK_SERVER_LISTEN").ok())
            .unwrap_or_else(|| "127.0.0.1:8787".to_owned()),
        vault_root: vault,
        token,
        version: VERSION.to_owned(),
    };
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => {
            return write_stderr(
                &format!("http: create runtime: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    match runtime.block_on(http::run(config)) {
        Ok(()) => process_exit(CoreExitCode::Ok),
        Err(error) => write_stderr(&format!("http: {error}\n"), CoreExitCode::Generic),
    }
}

fn cli() -> Command {
    Command::new("symdesk")
        .disable_version_flag(true)
        .arg(
            Arg::new("json")
                .long("json")
                .global(true)
                .action(ArgAction::SetTrue),
        )
        .arg(Arg::new("output").long("output").global(true).num_args(1))
        .arg(Arg::new("vault").long("vault").global(true).num_args(1))
        .subcommand(Command::new("version").arg(Arg::new("extra").num_args(0..)))
        .subcommand(Command::new("ls").arg(Arg::new("dir").long("dir").num_args(1)))
        .subcommand(
            Command::new("search").arg(Arg::new("query").num_args(0..).action(ArgAction::Append)),
        )
        .subcommand(Command::new("mcp"))
        .subcommand(
            Command::new("serve")
                .arg(Arg::new("listen").long("listen").num_args(1))
                .arg(Arg::new("token").long("token").num_args(1)),
        )
}

fn run_representative(parsed: RepresentativeArgs, output_json: bool) -> ExitCode {
    let Some(command) = parsed.command.as_deref() else {
        return emit_error("no command specified".to_owned(), output_json);
    };
    if command == "search" && parsed.query.is_none() {
        return emit_error("search query is required".to_owned(), output_json);
    }
    let vault = match resolve_vault(parsed.vault.as_deref()) {
        Ok(path) => path,
        Err(error) => return emit_error(error, output_json),
    };
    let sidecar_path = match path_for_vault(&vault) {
        Ok(path) => path,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    let mut sidecar = match Sidecar::open(&sidecar_path) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };

    match command {
        "ls" => {
            let mut files = match sidecar.list_files(parsed.dir.as_deref().unwrap_or("")) {
                Ok(files) => files,
                Err(error) => return emit_error(error.to_string(), output_json),
            };
            if files.is_empty() {
                if let Err(error) = sidecar.refresh_index(&vault) {
                    return emit_error(error.to_string(), output_json);
                }
                files = match sidecar.list_files(parsed.dir.as_deref().unwrap_or("")) {
                    Ok(files) => files,
                    Err(error) => return emit_error(error.to_string(), output_json),
                };
            }
            render_ls(&vault, &files, output_json)
        }
        "search" => {
            let Some(query) = parsed.query.as_deref() else {
                return emit_error("search query is required".to_owned(), output_json);
            };
            let hits = match sidecar.search(query) {
                Ok(hits) => hits,
                Err(error) => return emit_error(error.to_string(), output_json),
            };
            render_search(&vault, &hits, output_json)
        }
        _ => emit_error(format!("unknown command {command:?}"), output_json),
    }
}

#[derive(Default)]
struct RepresentativeArgs {
    command: Option<String>,
    query: Option<String>,
    dir: Option<String>,
    vault: Option<String>,
}

fn resolve_vault(flag: Option<&str>) -> Result<PathBuf, String> {
    let raw = flag
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
        .or_else(|| {
            std::env::var("SYMDESK_VAULT")
                .ok()
                .filter(|value| !value.trim().is_empty())
        })
        .ok_or_else(|| "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned())?;
    let path = PathBuf::from(raw);
    let absolute = if path.is_absolute() {
        path
    } else {
        std::env::current_dir()
            .map_err(|error| format!("failed to get absolute vault path: {error}"))?
            .join(path)
    };
    let absolute = lexical_clean(&absolute);
    let metadata = std::fs::metadata(&absolute)
        .map_err(|error| format!("vault path does not exist: {error}"))?;
    if !metadata.is_dir() {
        return Err("vault path is not a directory".to_owned());
    }
    Ok(absolute)
}

fn lexical_clean(path: &Path) -> PathBuf {
    let mut output = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                let _ = output.pop();
            }
            other => output.push(other.as_os_str()),
        }
    }
    output
}

fn relative_path(root: &Path, path: &str) -> String {
    let path = Path::new(path);
    let relative = path
        .strip_prefix(root)
        .ok()
        .map(Path::to_path_buf)
        .or_else(|| {
            let canonical_root = std::fs::canonicalize(root).ok()?;
            let canonical_path = std::fs::canonicalize(path).ok()?;
            canonical_path
                .strip_prefix(canonical_root)
                .ok()
                .map(Path::to_path_buf)
        })
        .map(|value| {
            value
                .to_string_lossy()
                .trim_start_matches(['/', '\\'])
                .to_owned()
        });
    relative
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| path.to_string_lossy().into_owned())
}

#[derive(Serialize)]
struct LsJsonEntry {
    path: String,
    title: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    document_type: String,
    modified: String,
}

#[derive(Serialize)]
struct SearchJsonHit {
    path: String,
    title: String,
    snippet: String,
    score: i32,
}

#[derive(Serialize)]
struct SearchJsonResponse {
    results: Vec<SearchJsonHit>,
}

fn render_ls(root: &Path, files: &[ListedDocument], json_output: bool) -> ExitCode {
    if json_output {
        if files.is_empty() {
            return write_stdout("null\n".to_owned());
        }
        let entries = files
            .iter()
            .map(|file| LsJsonEntry {
                path: relative_path(root, &file.path),
                title: file.title.clone(),
                document_type: file.document_type.clone(),
                modified: file.modified_at.clone(),
            })
            .collect::<Vec<_>>();
        return write_stdout(format!(
            "{}\n",
            serde_json::to_string(&entries).unwrap_or_default()
        ));
    }
    let entries = files
        .iter()
        .map(|file| {
            format!(
                "{{Path:{} Title:{} Type:{} Modified:{}}}",
                relative_path(root, &file.path),
                file.title,
                file.document_type,
                file.modified_at
            )
        })
        .collect::<Vec<_>>();
    write_stdout(format!("[{}]\n", entries.join(" ")))
}

fn render_search(root: &Path, hits: &[symdesk_index::SearchHit], json_output: bool) -> ExitCode {
    if json_output {
        let results = hits
            .iter()
            .map(|hit| SearchJsonHit {
                path: relative_path(root, &hit.path),
                title: hit.title.clone(),
                snippet: hit.snippet.clone(),
                score: 0,
            })
            .collect::<Vec<_>>();
        return write_stdout(format!(
            "{}\n",
            serde_json::to_string(&SearchJsonResponse { results }).unwrap_or_default()
        ));
    }
    let results = hits
        .iter()
        .map(|hit| {
            format!(
                "{{Path:{} Title:{} Snippet:{} Score:0 Anchor:<nil> MetadataMatches:[] SourceType: ReadOnly:false}}",
                relative_path(root, &hit.path),
                hit.title,
                hit.snippet
            )
        })
        .collect::<Vec<_>>();
    write_stdout(format!("{{Results:[{}] Hint:}}\n", results.join(" ")))
}

fn emit_error(error: String, json_output: bool) -> ExitCode {
    if json_output {
        let result = write_stdout(format!("{}\n", json!({"error": error})));
        if result == process_exit(CoreExitCode::Ok) {
            process_exit(CoreExitCode::Generic)
        } else {
            result
        }
    } else {
        write_stderr(&format!("{error}\n"), CoreExitCode::Generic)
    }
}

fn write_stdout(value: String) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(CoreExitCode::Ok)
}

fn write_stderr(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(code)
}

#[cfg(test)]
mod exit_code_tests {
    use super::CoreExitCode;

    #[test]
    fn corekit_exit_code_taxonomy_is_pinned() {
        let codes = [
            (CoreExitCode::Ok, 0),
            (CoreExitCode::Generic, 1),
            (CoreExitCode::NoInput, 2),
            (CoreExitCode::NoAuth, 3),
            (CoreExitCode::Forbidden, 4),
            (CoreExitCode::NotFound, 5),
            (CoreExitCode::Conflict, 6),
            (CoreExitCode::Software, 7),
            (CoreExitCode::Data, 8),
            (CoreExitCode::Config, 9),
            (CoreExitCode::Interrupted, 10),
        ];
        for (code, expected) in codes {
            assert_eq!(code.as_u8(), expected);
        }
    }
}
