#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    fs::File,
    io::{self, Read, Write},
    path::PathBuf,
    process::ExitCode,
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{identity, room_init};

const USAGE: &str = "Usage: symroom init <dir> --identity <name> [--name <display_name>]\n";
const FLAG_USAGE: &str = "Usage of init:\n  -identity string\n    \tOwner identity name\n  -name string\n    \tRoom display name (default \"Default Room\")\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let mut identity_name = String::new();
    let mut name = "Default Room".to_owned();
    let mut directory = None;
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
            if matches!(flag, "h" | "help") {
                return stderr(FLAG_USAGE, CoreExitCode::Ok);
            }
            let (key, inline) = flag
                .split_once('=')
                .map_or((flag, None), |(key, value)| (key, Some(value)));
            let value = match inline {
                Some(value) => value.to_owned(),
                None => match args.get(index + 1) {
                    Some(value) => {
                        index += 1;
                        value.to_string_lossy().into_owned()
                    }
                    None => {
                        return flag_error(&format!("flag needs an argument: -{key}\n"));
                    }
                },
            };
            match key {
                "identity" => identity_name = value,
                "name" => name = value,
                _ => {
                    return stderr(
                        &format!("flag provided but not defined: -{key}\n{FLAG_USAGE}"),
                        CoreExitCode::NoInput,
                    );
                }
            }
            index += 1;
            continue;
        }
        parsing_flags = false;
        if directory.is_none() {
            directory = Some(argument.into_owned());
        }
        index += 1;
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
    let (room_id, event_id) = generate_ids();
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

fn generate_ids() -> (String, String) {
    let mut bytes = [0_u8; 18];
    if File::open("/dev/urandom")
        .and_then(|mut random| random.read_exact(&mut bytes))
        .is_err()
    {
        static SEQUENCE: AtomicU64 = AtomicU64::new(0);
        let timestamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let sequence = u128::from(SEQUENCE.fetch_add(1, Ordering::Relaxed));
        let process = u128::from(std::process::id());
        for (index, byte) in bytes.iter_mut().enumerate() {
            *byte = (timestamp.rotate_left((index * 7) as u32)
                ^ process.rotate_left((index * 11) as u32)
                ^ sequence.rotate_left((index * 5) as u32)) as u8;
        }
    }
    (
        format!("rm_{}", hex::encode(&bytes[..8])),
        format!("ev_{}", hex::encode(&bytes[8..])),
    )
}

fn stdout(value: &str, code: CoreExitCode) -> ExitCode {
    write(io::stdout(), value, code)
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    write(io::stderr(), value, code)
}

fn flag_error(message: &str) -> ExitCode {
    stderr(&format!("{message}{FLAG_USAGE}"), CoreExitCode::NoInput)
}

fn write(mut output: impl Write, value: &str, code: CoreExitCode) -> ExitCode {
    if output.write_all(value.as_bytes()).is_err() {
        return ExitCode::from(CoreExitCode::Generic.as_u8());
    }
    ExitCode::from(code.as_u8())
}
