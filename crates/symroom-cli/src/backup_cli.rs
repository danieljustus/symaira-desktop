//! RUST-018 data safety for `symroom`: the one state it owns outside a room
//! directory is the identity store (`$XDG_DATA_HOME/symroom/identities`, the
//! Ed25519 private keys that sign journal events). Rooms themselves are
//! user-chosen directories (often git repositories) and are never copied; the
//! derived `.symroom/index.db` inside a room is rebuildable.
//!
//! First Rust run: back the identity store up once (see
//! `symdesk_core::pre_rust_backup`). Hidden `symroom backup restore-pre-rust
//! [--dry-run]` restores it.
#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symdesk_core::{
    config,
    pre_rust_backup::{self, Outcome, Spec},
};

use crate::VERSION;

fn spec() -> Option<Spec> {
    let environment = config::environment_snapshot().ok()?;
    let data = PathBuf::from(config::resolve_data_home(&environment)).join("symroom");
    let configuration = PathBuf::from(config::resolve_config_home(&environment)).join("symroom");
    if !data.is_absolute() || !configuration.is_absolute() {
        return None;
    }
    Some(Spec {
        tool: "symroom".to_owned(),
        version: VERSION.to_owned(),
        backups_dir: data.join("backups"),
        roots: vec![data, configuration],
        extra_files: Vec::new(),
    })
}

fn to_stderr(text: &str) {
    let _ = io::stderr().write_all(text.as_bytes());
}

/// Runs before any subcommand. `Err` aborts: state exists but could not be
/// backed up and nothing was changed. Never writes stdout (MCP framing).
pub fn gate() -> Result<(), ExitCode> {
    let Some(spec) = spec() else {
        return Ok(());
    };
    match pre_rust_backup::ensure(&spec) {
        Ok(Outcome::Created { dir, files }) => {
            to_stderr(&pre_rust_backup::created_notice("symroom", &dir, files));
            Ok(())
        }
        Ok(_) => Ok(()),
        Err(error) => {
            to_stderr(&pre_rust_backup::failure_message("symroom", &error));
            Err(ExitCode::from(1))
        }
    }
}

/// `symroom backup restore-pre-rust [--dry-run]` (hidden: not in the usage).
pub fn intercept(args: &[OsString]) -> Option<ExitCode> {
    if args.get(1).is_none_or(|word| word != "backup")
        || args.get(2).is_none_or(|word| word != "restore-pre-rust")
    {
        return None;
    }
    let rest: Vec<String> = args[3..]
        .iter()
        .map(|arg| arg.to_string_lossy().into_owned())
        .collect();
    let result = match spec() {
        Some(spec) => pre_rust_backup::restore_command("symroom", &spec, &rest),
        None => pre_rust_backup::CommandResult {
            code: 1,
            stdout: String::new(),
            stderr: "symroom: cannot resolve the state locations (set HOME or XDG_DATA_HOME)\n"
                .to_owned(),
        },
    };
    let _ = io::stdout().write_all(result.stdout.as_bytes());
    to_stderr(&result.stderr);
    Some(ExitCode::from(result.code))
}
