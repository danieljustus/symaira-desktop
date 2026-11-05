//! RUST-018 data safety for the Rust-only cutover: the automatic one-time
//! backup of existing state before the first Rust run, and the hidden
//! `symdesk backup restore-pre-rust [--dry-run]` command.
//!
//! The restore command is deliberately not part of the cobra command tree: that
//! tree is the Go-recorded structural contract (`symdesk --help`, completion and
//! the 207-node tree fixture stay byte-identical), so the command is intercepted
//! before cobra resolution, exactly like a hidden command.
#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_core::{
    config,
    pre_rust_backup::{self, Outcome, Spec},
};

use crate::{VERSION, config_paths};

/// State locations of `symdesk config paths`, plus the legacy fallback
/// directories of the absorbed tools. `None` when the locations cannot be
/// resolved to absolute paths (then the command reports its own error).
fn spec() -> Option<Spec> {
    let environment = config::environment_snapshot().ok()?;
    let data_home = PathBuf::from(config::resolve_data_home(&environment));
    let config_home = PathBuf::from(config::resolve_config_home(&environment));
    if !data_home.is_absolute() || !config_home.is_absolute() {
        return None;
    }
    let mut roots = vec![
        PathBuf::from(config::data_dir(&environment)),
        PathBuf::from(config::config_dir(&environment)),
    ];
    for legacy in ["symingest", "symrelate", "symaira-seek"] {
        roots.push(data_home.join(legacy));
        roots.push(config_home.join(legacy));
    }
    if let Some(home) = environment.get("HOME").filter(|home| !home.is_empty()) {
        // Go symingest read its global config from $HOME/.config/symingest.
        roots.push(PathBuf::from(home).join(".config").join("symingest"));
    }
    if let Some(override_dir) = environment
        .get("SYMRELATE_DATA_HOME")
        .map(|value| value.trim())
        .filter(|value| !value.is_empty())
    {
        roots.push(PathBuf::from(override_dir));
    }
    let mut extra_files = vec![PathBuf::from(config::global_path(&environment))];
    extra_files.push(PathBuf::from(config::contacts_paths(&environment).db_path));
    if let Ok(path) = config::ingest_data_path(&environment, "symingest.db") {
        extra_files.push(PathBuf::from(path));
    }
    if let Ok(resolved) = config_paths::resolve_ingest() {
        extra_files.push(PathBuf::from(resolved.db_path));
    }
    extra_files.retain(|path| path.is_absolute());
    Some(Spec {
        tool: "symdesk".to_owned(),
        version: VERSION.to_owned(),
        backups_dir: PathBuf::from(config::data_dir(&environment)).join("backups"),
        roots,
        extra_files,
    })
}

fn to_stderr(text: &str) {
    // A closed stderr must not turn a successful backup into a failure.
    let _ = io::stderr().write_all(text.as_bytes());
}

/// Runs before any command executes. `Err` aborts the command: state exists
/// but could not be backed up, and nothing was changed. Never writes stdout.
/// `symdesk version` reads no state and keeps its exact Go bytes, so it neither
/// triggers the backup nor prints the notice.
pub fn gate(args: &[OsString]) -> Result<(), ExitCode> {
    if crate::cobra::resolved_key(args).as_deref() == Some("version") {
        return Ok(());
    }
    let Some(spec) = spec() else {
        return Ok(());
    };
    match pre_rust_backup::ensure(&spec) {
        Ok(Outcome::Created { dir, files }) => {
            to_stderr(&pre_rust_backup::created_notice("symdesk", &dir, files));
            Ok(())
        }
        Ok(_) => Ok(()),
        Err(error) => {
            to_stderr(&pre_rust_backup::failure_message("symdesk", &error));
            Err(ExitCode::from(CoreExitCode::Generic.as_u8()))
        }
    }
}

/// Handles `symdesk backup restore-pre-rust [--dry-run]`; `None` leaves every
/// other invocation to the cobra front half (so `symdesk backup` stays Go's
/// "unknown command").
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
        Some(spec) => pre_rust_backup::restore_command("symdesk", &spec, &rest),
        None => pre_rust_backup::CommandResult {
            code: 1,
            stdout: String::new(),
            stderr: "symdesk: cannot resolve the state locations (set HOME or XDG_DATA_HOME)\n"
                .to_owned(),
        },
    };
    let _ = io::stdout().write_all(result.stdout.as_bytes());
    to_stderr(&result.stderr);
    Some(ExitCode::from(result.code))
}
