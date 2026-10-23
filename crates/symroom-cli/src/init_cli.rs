#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use crate::member_cli;
use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{identity, room_init};

const USAGE: &str = "Usage: symroom init <dir> --identity <name> [--name <display_name>]\n";
const FLAG_USAGE: &str = "Usage of init:\n  -identity string\n    \tOwner identity name\n  -name string\n    \tRoom display name (default \"Default Room\")\n";

pub fn run(args: &[OsString]) -> ExitCode {
    use crate::goflag::Spec;
    let parsed = match crate::goflag::parse_or_exit(
        args,
        &[Spec::string("name"), Spec::string("identity")],
        FLAG_USAGE,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let mut identity_name = parsed.string("identity").to_owned();
    let name = if parsed.is_set("name") {
        parsed.string("name").to_owned()
    } else {
        "Default Room".to_owned()
    };
    let directory = parsed.positionals.first().cloned();

    if identity_name.is_empty() {
        identity_name = match member_cli::default_identity() {
            Ok(name) => name,
            Err(error) => {
                return stderr(
                    &format!("Error loading configuration: {error}\n"),
                    CoreExitCode::NoInput,
                );
            }
        };
    }
    if identity_name.is_empty() {
        if directory.is_none() {
            return stdout(USAGE, CoreExitCode::Ok);
        }
        return stderr(
            "Error: --identity is required when default_identity is not configured\n",
            CoreExitCode::NoInput,
        );
    }
    let directory = directory
        .unwrap_or_else(|| std::env::var("SYMROOM_ROOM_DIR").unwrap_or_else(|_| ".".to_owned()));
    let owner = match identity::load(&identity_name) {
        Ok(owner) => owner,
        Err(error) => {
            return stderr(
                &format!("Error loading identity {identity_name}: {error}\n"),
                CoreExitCode::NotFound,
            );
        }
    };
    let directory = PathBuf::from(&directory);
    let (room_id, event_id) = match generate_ids() {
        Ok(ids) => ids,
        Err(error) => {
            return stderr(
                &format!("Error initializing room: generate random id: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    let config = match room_init::init(
        &directory,
        &name,
        &owner,
        &room_id,
        &event_id,
        time::OffsetDateTime::now_utc(),
    ) {
        Ok(config) => config,
        Err(error) => {
            return stderr(
                &format!("Error initializing room: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    stdout(
        &format!(
            "Initialized room {} in {} (owner: {})\n",
            config.id,
            directory.display(),
            owner.name
        ),
        CoreExitCode::Ok,
    )
}

fn generate_ids() -> Result<(String, String), getrandom::Error> {
    let mut bytes = [0_u8; 18];
    getrandom::fill(&mut bytes)?;
    Ok((
        format!("rm_{}", hex::encode(&bytes[..8])),
        format!("ev_{}", hex::encode(&bytes[8..])),
    ))
}

fn stdout(value: &str, code: CoreExitCode) -> ExitCode {
    write(io::stdout(), value, code)
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    write(io::stderr(), value, code)
}

fn write(mut output: impl Write, value: &str, code: CoreExitCode) -> ExitCode {
    if output.write_all(value.as_bytes()).is_err() {
        return ExitCode::from(CoreExitCode::Generic.as_u8());
    }
    ExitCode::from(code.as_u8())
}
