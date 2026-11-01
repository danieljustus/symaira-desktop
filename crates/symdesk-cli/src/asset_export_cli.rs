//! `symdesk asset store` and `symdesk export profiles` (Go
//! `cmd/symdesk/asset.go`, `cmd/symdesk/export.go`).
use std::{collections::BTreeMap, io::Read, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use symdesk_vault::activity_journal::append_activity;

use crate::{
    service_cli::{go_string_map, with_deps},
    write_go_json, write_stdout,
};

pub fn asset_cli() -> Command {
    Command::new("asset").subcommand(
        Command::new("store")
            .arg(Arg::new("args").num_args(0..))
            .arg(Arg::new("name").long("name").default_value("")),
    )
}

pub fn export_cli() -> Command {
    Command::new("export")
        .arg(Arg::new("extra").num_args(0..).hide(true))
        .subcommand(Command::new("profiles").arg(Arg::new("extra").num_args(0..).hide(true)))
}

/// Go `os.ReadFile` / `io.ReadAll` failure text without Rust's `(os error N)`.
fn go_os_error(error: &std::io::Error) -> String {
    let text = error.to_string();
    let text = text.split(" (os error").next().unwrap_or(&text);
    let mut chars = text.chars();
    chars
        .next()
        .map(|first| first.to_lowercase().collect::<String>() + chars.as_str())
        .unwrap_or_default()
}

pub fn run_asset(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let Some(("store", store)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let source = store
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .next()
        .cloned()
        .unwrap_or_default();
    let preferred = store.get_one::<String>("name").cloned().unwrap_or_default();
    // Go reads the data before it opens the vault.
    let data = if source == "-" {
        let mut buffer = Vec::new();
        std::io::stdin()
            .read_to_end(&mut buffer)
            .map(|_| buffer)
            .map_err(|error| format!("read asset data: {}", go_os_error(&error)))
    } else {
        std::fs::read(&source).map_err(|error| {
            format!(
                "read asset data: {}",
                symdesk_vault::go_path_error(
                    // Opening a directory succeeds; the read then fails (EISDIR).
                    if error.raw_os_error() == Some(21) { "read" } else { "open" },
                    std::path::Path::new(&source),
                    &error,
                )
            )
        })
    };
    let data = match data {
        Ok(data) => data,
        Err(error) => return crate::emit_error(error, json),
    };
    let name = if preferred.is_empty() && source != "-" {
        // filepath.Base
        let trimmed = source.trim_end_matches('/');
        if trimmed.is_empty() {
            "/".to_owned()
        } else {
            trimmed.rsplit('/').next().unwrap_or(trimmed).to_owned()
        }
    } else {
        preferred
    };
    let extension = {
        let last = name.rsplit('/').next().unwrap_or(&name);
        last.rfind('.').map_or("", |at| &last[at..]).to_owned()
    };
    with_deps(vault, json, |root, _db| {
        let path = crate::mcp::write::store_asset(root, &data, &name, &extension)?;
        let _ = append_activity(root, "asset_added", &path, &name, "stored asset");
        let filename = path.rsplit('/').next().unwrap_or(&path);
        let status = BTreeMap::from([
            (
                "markdown_link".to_owned(),
                format!("![{filename}]({})", path.replace(' ', "%20")),
            ),
            ("path".to_owned(), path),
            ("status".to_owned(), "ok".to_owned()),
        ]);
        Ok(if json {
            write_go_json(&status)
        } else {
            write_stdout(format!("{}\n", go_string_map(status.iter())))
        })
    })
}

#[derive(Serialize)]
struct Profile {
    name: &'static str,
    title: &'static str,
    description: &'static str,
    stability: &'static str,
}

pub fn run_export(command: &ArgMatches, json: bool) -> ExitCode {
    let Some(("profiles", _)) = command.subcommand() else {
        return crate::emit_error("unknown command \"export\"".to_owned(), json);
    };
    let profiles: Vec<Profile> = symdesk_render::press::profiles()
        .into_iter()
        .map(|p| Profile {
            name: p.name,
            title: p.title,
            description: p.description,
            stability: p.stability,
        })
        .collect();
    if json {
        return write_go_json(&profiles);
    }
    write_stdout(format!(
        "[{}]\n",
        profiles
            .iter()
            .map(|p| format!(
                "{{Name:{} Title:{} Description:{} Stability:{}}}",
                p.name, p.title, p.description, p.stability
            ))
            .collect::<Vec<_>>()
            .join(" ")
    ))
}
