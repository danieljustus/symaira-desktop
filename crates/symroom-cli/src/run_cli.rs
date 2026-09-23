use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::runs::{self, RunQueryError};

const USAGE: &str = "Usage: symroom run <request|list|show|start|cancel> [flags] [args]\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let Some(action) = args.first() else {
        return stdout(USAGE.to_owned(), CoreExitCode::Ok);
    };
    match action.to_string_lossy().as_ref() {
        "list" => list(&args[1..]),
        "show" => show(&args[1..]),
        action => stderr(
            &format!("Unknown run action: {action}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn list(args: &[OsString]) -> ExitCode {
    let mut pending = false;
    let mut json = false;
    let mut parsing_flags = true;
    for argument in args {
        let value = argument.to_string_lossy();
        if parsing_flags && value == "--" {
            parsing_flags = false;
            continue;
        }
        if parsing_flags && value.starts_with('-') {
            match flag_value(&value, "pending") {
                Some(value) => pending = value,
                None => match flag_value(&value, "json") {
                    Some(value) => json = value,
                    None => return unknown_flag("run list", &value, true),
                },
            }
        } else {
            parsing_flags = false;
        }
    }

    let room = room_dir();
    let records = match runs::list(&room, pending) {
        Ok(records) => records,
        Err(error) => {
            return stderr(
                &format!("Error listing runs: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if json {
        let rendered = if records.is_empty() {
            "null".to_owned()
        } else {
            match pretty_go_json(&records) {
                Ok(rendered) => rendered,
                Err(_) => return process_exit(CoreExitCode::Generic),
            }
        };
        return stdout(format!("{rendered}\n"), CoreExitCode::Ok);
    }
    let mut output = String::new();
    for record in records {
        output.push_str(&format!(
            "{}\t[{}]\t{}\t{}\n",
            record.id, record.state, record.author, record.title
        ));
    }
    stdout(output, CoreExitCode::Ok)
}

fn show(args: &[OsString]) -> ExitCode {
    let mut json = false;
    let mut positional = Vec::new();
    let mut parsing_flags = true;
    for argument in args {
        let value = argument.to_string_lossy();
        if parsing_flags && value == "--" {
            parsing_flags = false;
            continue;
        }
        if parsing_flags && value.starts_with('-') {
            match flag_value(&value, "json") {
                Some(value) => json = value,
                None => return unknown_flag("run show", &value, false),
            }
        } else {
            parsing_flags = false;
            positional.push(value.into_owned());
        }
    }
    let Some(run_id) = positional.first() else {
        return stderr(
            "Usage: symroom run show <run_id> [--json]\n",
            CoreExitCode::NoInput,
        );
    };
    let record = match runs::get(&room_dir(), run_id) {
        Ok(record) => record,
        Err(RunQueryError::NotFound) => {
            return stderr(
                &format!("Error: run {run_id} not found\n"),
                CoreExitCode::NotFound,
            );
        }
        Err(error) => {
            return stderr(
                &format!("Error showing run: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if json {
        let rendered = match pretty_go_json(&record) {
            Ok(rendered) => rendered,
            Err(_) => return process_exit(CoreExitCode::Generic),
        };
        return stdout(format!("{rendered}\n"), CoreExitCode::Ok);
    }
    let mut output = format!(
        "Run ID:     {}\nTitle:      {}\nState:      {}\nAuthor:     {}\nCreated At: {}\n",
        record.id, record.title, record.state, record.author, record.created_at
    );
    if let Some(summary) = &record.summary {
        output.push_str(&format!("Summary:    {summary}\n"));
    }
    if let Some(error) = &record.error {
        output.push_str(&format!("Error:      {error}\n"));
    }
    stdout(output, CoreExitCode::Ok)
}

fn flag_value(argument: &str, name: &str) -> Option<bool> {
    let trimmed = argument.trim_start_matches('-');
    if trimmed == name {
        return Some(true);
    }
    trimmed
        .strip_prefix(&format!("{name}="))
        .and_then(|value| match value {
            "true" => Some(true),
            "false" => Some(false),
            _ => None,
        })
}

fn unknown_flag(command: &str, argument: &str, pending_flag: bool) -> ExitCode {
    let name = argument.trim_start_matches('-');
    let mut usage = format!(
        "flag provided but not defined: -{name}\nUsage of {command}:\n  -json\n    \tOutput as JSON\n"
    );
    if pending_flag {
        usage.push_str("  -pending\n    \tShow pending runs only\n");
    }
    stderr(&usage, CoreExitCode::NoInput)
}

fn pretty_go_json(value: &impl serde::Serialize) -> Result<String, serde_json::Error> {
    let json = serde_json::to_string_pretty(value)?;
    Ok(json
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029"))
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn stdout(value: String, code: CoreExitCode) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(code)
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(code)
}

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}
