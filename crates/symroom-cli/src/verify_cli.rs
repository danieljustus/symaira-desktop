#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symroom_core::journal;

const FLAG_USAGE: &str = "Usage of verify:\n  -json\n    \tOutput verification findings as JSON\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let mut json = false;
    for arg in args {
        let value = arg.to_string_lossy();
        if value == "--" || !value.starts_with('-') {
            break;
        }
        let flag = value.trim_start_matches('-');
        if flag == "json" {
            json = true;
        } else if let Some(value) = flag.strip_prefix("json=") {
            json = match value {
                "1" | "t" | "T" | "TRUE" | "True" | "true" => true,
                "0" | "f" | "F" | "FALSE" | "False" | "false" => false,
                _ => {
                    return stderr(
                        &format!(
                            "invalid boolean value \"{value}\" for -json: parse error\n{FLAG_USAGE}"
                        ),
                        2,
                    );
                }
            };
        } else if matches!(flag, "h" | "help") {
            return stderr(FLAG_USAGE, 0);
        } else {
            return stderr(
                &format!("flag provided but not defined: -{flag}\n{FLAG_USAGE}"),
                2,
            );
        }
    }
    let room = std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."));
    let report = match journal::verify(&room) {
        Ok(report) => report,
        Err(error) => {
            return stderr(
                &format!("Error verifying journal: read all segments: {error}\n"),
                1,
            );
        }
    };
    let output = if json {
        match serde_json::to_string_pretty(&report) {
            Ok(value) => format!("{value}\n"),
            Err(_) => return ExitCode::from(1),
        }
    } else if report.valid {
        "Journal verification PASSED: zero findings\n".to_owned()
    } else {
        let mut output = format!(
            "Journal verification FAILED: {} finding(s):\n",
            report.findings.len()
        );
        for finding in &report.findings {
            output.push_str(&format!(
                "  - [{}] {} (event: {}, author: {})\n",
                finding.code, finding.message, finding.event_id, finding.author
            ));
        }
        output
    };
    stdout(&output, if report.valid { 0 } else { 1 })
}

fn stdout(message: &str, code: u8) -> ExitCode {
    write(io::stdout(), message, code)
}

fn stderr(message: &str, code: u8) -> ExitCode {
    write(io::stderr(), message, code)
}

fn write(mut output: impl Write, message: &str, code: u8) -> ExitCode {
    if output.write_all(message.as_bytes()).is_err() {
        return ExitCode::from(1);
    }
    ExitCode::from(code)
}
