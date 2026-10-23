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
    use crate::goflag::{Spec, check_go_int};
    let parsed = match crate::goflag::parse_or_exit(
        args,
        &[
            Spec::string("since"),
            Spec::string("until"),
            Spec::string("kind"),
            Spec::string("author"),
            Spec::string("run"),
            Spec::checked("limit", check_go_int),
            Spec::boolean("json"),
        ],
        FLAG_USAGE,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let json = parsed.boolean("json");
    let filter = LogFilter {
        since: parsed.string("since").to_owned(),
        until: parsed.string("until").to_owned(),
        kind: parsed.string("kind").to_owned(),
        author: parsed.string("author").to_owned(),
        run: parsed.string("run").to_owned(),
        limit: crate::goflag::parse_go_int(parsed.string("limit")).unwrap_or_default(),
    };
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
