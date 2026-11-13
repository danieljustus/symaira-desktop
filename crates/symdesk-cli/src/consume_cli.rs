//! `symdesk consume status|set-path` (Go `cmd/symdesk/consume.go`): the
//! configured consume (watched inbox) folder.
use std::{collections::BTreeMap, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde_json::json;
use symaira_core_exit::ExitCode as CoreExitCode;

use crate::{
    config_paths::load_root_config, emit_error, resolve_vault_root, write_go_json, write_stderr,
    write_stdout,
};

pub fn cli() -> Command {
    Command::new("consume")
        .subcommand(Command::new("status").arg(Arg::new("extra").num_args(0..).hide(true)))
        .subcommand(Command::new("set-path").arg(Arg::new("args").num_args(0..)))
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let Some((name, sub)) = command.subcommand() else {
        return crate::process_exit(CoreExitCode::Ok);
    };
    let (mut config, environment) = match load_root_config() {
        Ok(value) => value,
        Err(error) => {
            return write_stderr(
                &format!("failed to load config: {error}\n"),
                CoreExitCode::Config,
            );
        }
    };
    if let Some(flag) = vault.filter(|value| !value.is_empty()) {
        config.vault = flag.to_owned();
    }
    if name == "status" {
        let root = match resolve_vault_root(vault) {
            Ok(root) => root,
            Err(error) => {
                return emit_error(format!("failed to resolve vault root: {error}"), json);
            }
        };
        let root = root.to_string_lossy().into_owned();
        let inbox = if config.inbox.is_empty() {
            std::path::Path::new(&root)
                .join("inbox_watch")
                .to_string_lossy()
                .into_owned()
        } else {
            config.inbox.clone()
        };
        let exists = std::fs::metadata(&inbox).is_ok();
        return if json {
            write_go_json(&json!({
                "configured_path": config.inbox,
                "exists": exists,
                "inbox_path": inbox,
                "vault_path": root,
            }))
        } else {
            write_stdout(format!(
                "Inbox path: {inbox}\nConfigured path: {}\nDirectory exists: {exists}\n",
                config.inbox
            ))
        };
    }
    let new_path = sub
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .next()
        .cloned()
        .unwrap_or_default();
    config.inbox.clone_from(&new_path);
    let path = symdesk_core::config::global_path(&environment);
    if let Err(error) = symdesk_core::config::save(&path, &config) {
        return emit_error(format!("failed to save config: {error}"), json);
    }
    if json {
        let status = BTreeMap::from([
            ("inbox_path".to_owned(), new_path),
            ("status".to_owned(), "ok".to_owned()),
        ]);
        write_go_json(&status)
    } else {
        write_stdout(format!("Consume folder path set to: {new_path}\n"))
    }
}
