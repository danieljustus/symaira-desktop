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
    let parsed = match crate::goflag::parse_or_exit(
        args,
        &[crate::goflag::Spec::boolean("public")],
        EXPORT_USAGE,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let public = parsed.boolean("public");
    let name = parsed.positionals.first().cloned();
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
