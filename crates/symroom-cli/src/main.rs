#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    process::ExitCode,
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_core::{render_version_json, render_version_text};

mod artifact_cli;
mod brain_profile_cli;
mod decide_cli;
mod identity_cli;
mod index_cli;
mod log_cli;
mod mcp;
mod member_cli;
mod note_cli;
mod run_cli;
mod verify_cli;
mod watch_cli;

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}

const VERSION: &str = match option_env!("SYMROOM_VERSION") {
    Some(version) => version,
    None => "dev",
};

const USAGE: &str = "symroom - room management and coordination tool\n\nUsage:\n  symroom <subcommand> [flags] [args]\n\nAvailable Subcommands:\n  init           Initialize a room\n  identity       Manage Ed25519 identities\n  member         Manage room members\n  note           Post a journal note\n  decide         Record a room decision\n  artifact       Manage room artifacts\n  log            Display room journal log\n  verify         Verify journal chains and signatures\n  index          Rebuild or manage derived SQLite index\n  run            Manage room runs\n  checkpoint     Manage run checkpoints\n  watch          Watch symdesk events stream\n  brain-profile  Emit a symbrain profile\n  doctor         Run system and environment checks\n  version        Print version information\n  mcp            Run MCP server mode\n\nUse \"symroom <subcommand> --help\" for more information about a subcommand.\n";

fn main() -> ExitCode {
    let args: Vec<OsString> = std::env::args_os().collect();
    let Some(command) = args.get(1) else {
        return write_stderr(USAGE, 2);
    };
    if command == "-h" || command == "--help" || command == "help" {
        return write_stdout(USAGE.to_owned());
    }
    if command == "run" {
        return run_cli::run(&args[2..]);
    }
    if command == "identity" {
        return identity_cli::run(&args[2..]);
    }
    if command == "member" {
        return member_cli::run(&args[2..]);
    }
    if command == "index" {
        return index_cli::run(&args[2..]);
    }
    if command == "verify" {
        return verify_cli::run(&args[2..]);
    }
    if command == "log" {
        return log_cli::run(&args[2..]);
    }
    if command == "decide" {
        return decide_cli::run(&args[2..]);
    }
    if command == "note" {
        return note_cli::run(&args[2..]);
    }
    if command == "artifact" {
        return artifact_cli::run(&args[2..]);
    }
    if command == "brain-profile" {
        return brain_profile_cli::run(&args[2..]);
    }
    if command == "mcp" {
        return mcp::run_cli(&args[2..]);
    }
    if command == "watch" {
        return watch_cli::run(&args[2..]);
    }
    if command != "version" {
        return write_stderr(
            &format!(
                "Unknown subcommand: {}\n\n{USAGE}",
                command.to_string_lossy()
            ),
            2,
        );
    }

    let mut json = false;
    for argument in args.iter().skip(2) {
        if argument == "-json" || argument == "--json" {
            json = true;
            continue;
        }
        if argument.to_string_lossy().starts_with('-') {
            let name = argument
                .to_string_lossy()
                .trim_start_matches('-')
                .to_owned();
            return write_stderr(
                &format!(
                    "flag provided but not defined: -{name}\nUsage of version:\n  -json\n    \tEmit version info in JSON format\n"
                ),
                2,
            );
        }
        break;
    }
    let rendered = if json {
        match render_version_json("symroom", VERSION) {
            Ok(value) => value,
            Err(_) => return process_exit(CoreExitCode::Generic),
        }
    } else {
        render_version_text("symroom", VERSION)
    };
    write_stdout(rendered)
}

fn write_stdout(value: String) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(CoreExitCode::Ok)
}

fn write_stderr(value: &str, code: u8) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    ExitCode::from(code)
}
