#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    process::ExitCode,
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::identity;

const EXPORT_USAGE: &str = "Usage of identity export:\n  -public\n    \tExport public key only\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let Some(action) = args.first() else {
        return stdout(
            "Usage: symroom identity <create|list|show|export> [args]\n",
            CoreExitCode::Ok,
        );
    };
    match action.to_string_lossy().as_ref() {
        "create" => create(args.get(1)),
        "list" => list(),
        "show" => show(args.get(1)),
        "export" => export(&args[1..]),
        other => stderr(
            &format!("Unknown identity action: {other}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn create(name: Option<&OsString>) -> ExitCode {
    let Some(name) = name else {
        return stderr(
            "Usage: symroom identity create <name>\n",
            CoreExitCode::NoInput,
        );
    };
    let name = name.to_string_lossy();
    let id = match identity::generate(&name) {
        Ok(id) => id,
        Err(error) => {
            return stderr(
                &format!("Error generating identity: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if let Err(error) = identity::save(&id) {
        return stderr(
            &format!("Error saving identity: {error}\n"),
            CoreExitCode::Generic,
        );
    }
    stdout(
        &format!("Created identity {} ({})\n", id.name, id.member_id),
        CoreExitCode::Ok,
    )
}

fn list() -> ExitCode {
    match identity::list() {
        Ok(names) => stdout(
            &names
                .iter()
                .map(|name| format!("{name}\n"))
                .collect::<String>(),
            CoreExitCode::Ok,
        ),
        Err(error) => stderr(
            &format!("Error listing identities: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn show(name: Option<&OsString>) -> ExitCode {
    let Some(name) = name else {
        return stderr(
            "Usage: symroom identity show <name>\n",
            CoreExitCode::NoInput,
        );
    };
    let name = name.to_string_lossy();
    match identity::load(&name) {
        Ok(id) => stdout(
            &format!(
                "Name: {}\nMember ID: {}\nPublic Key: {}\n",
                id.name,
                id.member_id,
                hex::encode(id.public_key)
            ),
            CoreExitCode::Ok,
        ),
        Err(error) => stderr(
            &format!("Error loading identity: {error}\n"),
            CoreExitCode::NotFound,
        ),
    }
}

fn export(args: &[OsString]) -> ExitCode {
    let mut public = false;
    let mut name = None;
    let mut parsing_flags = true;
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if parsing_flags && argument == "--" {
            parsing_flags = false;
            index += 1;
            continue;
        }
        if parsing_flags && argument.starts_with('-') {
            let flag = argument.trim_start_matches('-');
            if let Some(value) = flag.strip_prefix("public=") {
                match parse_bool(value) {
                    Some(value) => public = value,
                    None => return invalid_bool(value),
                }
                index += 1;
                continue;
            }
            if flag == "public" {
                public = true;
                index += 1;
                continue;
            }
            if matches!(flag, "h" | "help") {
                return stderr(EXPORT_USAGE, CoreExitCode::Ok);
            }
            return stderr(
                &format!("flag provided but not defined: -{flag}\n{EXPORT_USAGE}"),
                CoreExitCode::NoInput,
            );
        }
        parsing_flags = false;
        if name.is_none() {
            name = Some(argument.into_owned());
        }
        index += 1;
    }
    let Some(name) = name else {
        return stderr(
            "Usage: symroom identity export <name> --public\n",
            CoreExitCode::NoInput,
        );
    };
    let id = match identity::load(&name) {
        Ok(id) => id,
        Err(error) => {
            return stderr(
                &format!("Error loading identity: {error}\n"),
                CoreExitCode::NotFound,
            );
        }
    };
    if !public {
        return stderr(
            "Exporting private key is forbidden for security\n",
            CoreExitCode::Forbidden,
        );
    }
    stdout(
        &format!("{}\n", hex::encode(id.public_key)),
        CoreExitCode::Ok,
    )
}

fn parse_bool(value: &str) -> Option<bool> {
    match value {
        "1" | "t" | "T" | "TRUE" | "True" | "true" => Some(true),
        "0" | "f" | "F" | "FALSE" | "False" | "false" => Some(false),
        _ => None,
    }
}

fn invalid_bool(value: &str) -> ExitCode {
    stderr(
        &format!("invalid boolean value \"{value}\" for -public: parse error\n{EXPORT_USAGE}"),
        CoreExitCode::NoInput,
    )
}

fn stdout(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}
