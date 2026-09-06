#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    process::ExitCode,
};

use symdesk_core::{render_version_json, render_version_text};

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
            Err(_) => return ExitCode::from(1),
        }
    } else {
        render_version_text("symroom", VERSION)
    };
    write_stdout(rendered)
}

fn write_stdout(value: String) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        return ExitCode::from(1);
    }
    ExitCode::SUCCESS
}

fn write_stderr(value: &str, code: u8) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        return ExitCode::from(1);
    }
    ExitCode::from(code)
}
