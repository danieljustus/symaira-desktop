#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Read, Write},
    path::PathBuf,
    process::ExitCode,
    sync::atomic::{AtomicU64, Ordering},
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{
    event::{self, Event},
    identity, journal,
};

const USAGE: &str = "Usage: symroom note <message> [--identity <name>] [--json]\n";
const FLAG_USAGE: &str = "Usage of note:\n  -identity string\n    \tAuthor identity name\n  -json\n    \tOutput event as JSON\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let mut identity_name = String::new();
    let mut json = false;
    let mut message = None;
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
            if let Some(value) = flag.strip_prefix("identity=") {
                identity_name = value.to_owned();
                index += 1;
                continue;
            }
            if flag == "identity" {
                let Some(value) = args.get(index + 1) else {
                    return flag_error("flag needs an argument: -identity\n");
                };
                identity_name = value.to_string_lossy().into_owned();
                index += 2;
                continue;
            }
            if let Some(value) = flag.strip_prefix("json=") {
                match parse_bool(value) {
                    Some(value) => json = value,
                    None => return invalid_bool(value),
                }
                index += 1;
                continue;
            }
            if flag == "json" {
                json = true;
                index += 1;
                continue;
            }
            if matches!(flag, "h" | "help") {
                return stderr(FLAG_USAGE, CoreExitCode::Ok);
            }
            return stderr(
                &format!("flag provided but not defined: -{flag}\n{FLAG_USAGE}"),
                CoreExitCode::NoInput,
            );
        }
        parsing_flags = false;
        if message.is_none() {
            message = Some(argument.into_owned());
        }
        index += 1;
    }

    let Some(message) = message else {
        return stdout(USAGE, CoreExitCode::Ok);
    };
    if identity_name.is_empty() {
        return stderr(
            "Error: --identity is required when default_identity is not configured\n",
            CoreExitCode::NoInput,
        );
    }
    let signer = match identity::load(&identity_name) {
        Ok(signer) => signer,
        Err(error) => {
            return stderr(
                &format!("Error loading identity {identity_name}: {error}\n"),
                CoreExitCode::NotFound,
            );
        }
    };
    match post_note(&room_dir(), &message, &signer) {
        Ok(event) if json => match event.marshal_json_line() {
            Ok(line) => write_stdout(&line),
            Err(_) => process_exit(CoreExitCode::Generic),
        },
        Ok(event) => stdout(&format!("{}\n", event.id), CoreExitCode::Ok),
        Err(error) if error == "observer role has read-only access" => stderr(
            "Error: observer role has read-only access\n",
            CoreExitCode::Generic,
        ),
        Err(error) => stderr(
            &format!("Error posting note: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn post_note(
    room_dir: &std::path::Path,
    text: &str,
    signer: &identity::Identity,
) -> Result<Event, String> {
    let config = std::fs::read_to_string(room_dir.join("room.toml"))
        .map_err(|error| format!("read room.toml: {error}"))?;
    let room_id = config
        .lines()
        .find_map(|line| {
            let (key, value) = line.split_once('=')?;
            (key.trim() == "id").then(|| value.trim().trim_matches('"').to_owned())
        })
        .unwrap_or_default();
    let stats = journal::read_journal_stats(room_dir)
        .map_err(|error| format!("read journal dir: {error}"))?;
    if stats
        .member_state
        .members
        .get(&signer.member_id)
        .is_some_and(|member| member.role == "observer")
    {
        return Err("observer role has read-only access".to_owned());
    }
    let author =
        journal::author_stats(room_dir, &signer.member_id).map_err(|error| format!("{error}"))?;
    let body = serde_json::to_string(&serde_json::json!({ "text": text }))
        .map_err(|error| format!("marshal note body: {error}"))?
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    let body = serde_json::value::RawValue::from_string(body)
        .map_err(|error| format!("marshal note body: {error}"))?;
    let mut event = Event {
        v: event::CURRENT_VERSION,
        id: generate_event_id(),
        room: room_id,
        author: signer.member_id.clone(),
        seq: author.seq + 1,
        prev: author.prev,
        lamport: stats.max_lamport + 1,
        ts: event::current_timestamp(),
        kind: "note.posted".to_owned(),
        body,
        sig: None,
    };
    event
        .sign(signer)
        .map_err(|error| format!("sign note event: {error}"))?;
    journal::append_event(room_dir, &event).map_err(|error| format!("append event: {error}"))?;
    Ok(event)
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
        &format!("invalid boolean value \"{value}\" for -json: parse error\n{FLAG_USAGE}"),
        CoreExitCode::NoInput,
    )
}

fn flag_error(message: &str) -> ExitCode {
    stderr(&format!("{message}{FLAG_USAGE}"), CoreExitCode::NoInput)
}

fn generate_event_id() -> String {
    let mut bytes = [0_u8; 10];
    if std::fs::File::open("/dev/urandom")
        .and_then(|mut random| random.read_exact(&mut bytes))
        .is_err()
    {
        static FALLBACK_SEQUENCE: AtomicU64 = AtomicU64::new(0);
        let timestamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let sequence = FALLBACK_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let process = u128::from(std::process::id());
        for (index, byte) in bytes.iter_mut().enumerate() {
            *byte = (timestamp.rotate_left((index * 7) as u32)
                ^ process.rotate_left((index * 11) as u32)
                ^ u128::from(sequence.rotate_left((index * 5) as u32))) as u8;
        }
    }
    let mut id = String::from("ev_");
    for byte in bytes {
        id.push_str(&format!("{byte:02x}"));
    }
    id
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn write_stdout(bytes: &[u8]) -> ExitCode {
    if io::stdout().write_all(bytes).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(CoreExitCode::Ok)
    }
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
