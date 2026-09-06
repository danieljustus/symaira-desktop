#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    process::ExitCode,
};

use clap::{Arg, ArgAction, Command};
use symdesk_core::{render_version_json, render_version_text};

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
        return write_stderr("unknown flag: --version\n", 1);
    }

    let matches = match cli().try_get_matches_from(args) {
        Ok(matches) => matches,
        Err(error) => {
            let _ = error.print();
            return ExitCode::from(1);
        }
    };
    if matches.subcommand_name() != Some("version") {
        return ExitCode::SUCCESS;
    }

    let output = matches
        .get_one::<String>("output")
        .map_or("", String::as_str);
    if !output.is_empty() && !matches!(output, "text" | "json" | "yaml") {
        return write_stderr(
            &format!("invalid --output value {output:?} (want text|json|yaml)\n"),
            1,
        );
    }
    let rendered = if matches.get_flag("json") || output == "json" {
        match render_version_json("symdesk", VERSION) {
            Ok(value) => value,
            Err(_) => return ExitCode::from(1),
        }
    } else {
        render_version_text("symdesk", VERSION)
    };
    write_stdout(rendered)
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
