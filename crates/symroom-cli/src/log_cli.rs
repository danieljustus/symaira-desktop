#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symroom_core::log::{self, LogFilter};

const FLAG_USAGE: &str = "Usage of log:\n  -author string\n    \tFilter events by author member ID\n  -json\n    \tOutput events as NDJSON\n  -kind string\n    \tFilter events by kind\n  -limit int\n    \tLimit number of events returned\n  -run string\n    \tFilter events by run ID\n  -since string\n    \tFilter events since RFC3339 timestamp\n  -until string\n    \tFilter events until RFC3339 timestamp\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let mut filter = LogFilter::default();
    let mut json = false;
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if argument == "--" || !argument.starts_with('-') || argument == "-" {
            break;
        }
        let name = argument.trim_start_matches('-');
        let (name, inline) = name
            .split_once('=')
            .map_or((name, None), |(name, value)| (name, Some(value)));
        if matches!(name, "h" | "help") {
            return stderr(FLAG_USAGE, 0);
        }
        if name == "json" {
            json = match inline {
                None => true,
                Some("1" | "t" | "T" | "TRUE" | "True" | "true") => true,
                Some("0" | "f" | "F" | "FALSE" | "False" | "false") => false,
                Some(value) => {
                    return stderr(
                        &format!(
                            "invalid boolean value \"{value}\" for -json: parse error\n{FLAG_USAGE}"
                        ),
                        2,
                    );
                }
            };
            index += 1;
            continue;
        }
        if !matches!(
            name,
            "since" | "until" | "kind" | "author" | "run" | "limit"
        ) {
            return stderr(
                &format!("flag provided but not defined: -{name}\n{FLAG_USAGE}"),
                2,
            );
        }
        let value = match inline {
            Some(value) => value.to_owned(),
            None => match args.get(index + 1) {
                Some(value) => {
                    index += 1;
                    value.to_string_lossy().into_owned()
                }
                None => {
                    return stderr(&format!("flag needs an argument: -{name}\n{FLAG_USAGE}"), 2);
                }
            },
        };
        match name {
            "since" => filter.since = value,
            "until" => filter.until = value,
            "kind" => filter.kind = value,
            "author" => filter.author = value,
            "run" => filter.run = value,
            "limit" => match value.parse::<i64>() {
                Ok(limit) => filter.limit = limit,
                Err(_) => {
                    return stderr(
                        &format!(
                            "invalid value \"{value}\" for flag -limit: parse error\n{FLAG_USAGE}"
                        ),
                        2,
                    );
                }
            },
            _ => unreachable!(),
        }
        index += 1;
    }
    let room = std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."));
    let result = match log::query(&room, &filter) {
        Ok(result) => result,
        Err(error) => return stderr(&format!("Error querying log: {error}\n"), 1),
    };
    if result.invalid_count > 0 {
        let warning = format!(
            "Warning: {} invalid event(s) omitted from log. Run 'symroom verify' for details.\n",
            result.invalid_count
        );
        if io::stderr().write_all(warning.as_bytes()).is_err() {
            return ExitCode::from(1);
        }
    }
    for event in &result.events {
        let output = if json {
            match event.marshal_json_line() {
                Ok(line) => line,
                Err(_) => return ExitCode::from(1),
            }
        } else {
            format!("{}\n", log::format_event_human(event)).into_bytes()
        };
        if io::stdout().write_all(&output).is_err() {
            return ExitCode::from(1);
        }
    }
    ExitCode::from(0)
}

fn stderr(message: &str, code: u8) -> ExitCode {
    if io::stderr().write_all(message.as_bytes()).is_err() {
        return ExitCode::from(1);
    }
    ExitCode::from(code)
}
