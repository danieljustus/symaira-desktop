#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
    thread,
    time::{Duration, Instant},
};

use serde_json::value::RawValue;
use sha2::{Digest, Sha256};
use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{
    event::{self, Event},
    identity::{self, Identity},
    journal, members,
    runs::{self, Checkpoint},
};

const USAGE: &str = "Usage: symroom checkpoint <request|resolve> [flags] [args]\n";
const REQUEST_FLAGS: &str = "Usage of checkpoint request:\n  -identity string\n    \tAuthor identity name\n  -question string\n    \tQuestion string\n  -run string\n    \tRun ID\n  -timeout duration\n    \tWait timeout duration (default 15m0s)\n";
const RESOLVE_FLAGS: &str = "Usage of checkpoint resolve:\n  -answer string\n    \tAnswer string\n  -identity string\n    \tAuthor identity name\n";

#[derive(Default)]
struct Parsed {
    values: BTreeMap<String, String>,
    positionals: Vec<String>,
}

pub fn run(args: &[OsString]) -> ExitCode {
    let Some(action) = args.first() else {
        return stdout(USAGE, CoreExitCode::Ok);
    };
    match action.to_string_lossy().as_ref() {
        "request" => request(&args[1..]),
        "resolve" => resolve(&args[1..]),
        other => stderr(
            &format!("Unknown checkpoint action: {other}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn request(args: &[OsString]) -> ExitCode {
    let parsed = match parse(
        args,
        &["run", "question", "timeout", "identity"],
        REQUEST_FLAGS,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let run_id = parsed.values.get("run").map_or("", String::as_str);
    let question = parsed.values.get("question").map_or("", String::as_str);
    if run_id.is_empty() || question.is_empty() {
        return stderr(
            "Usage: symroom checkpoint request --run <id> --question \"...\" [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    }
    let timeout = match parsed.values.get("timeout") {
        Some(value) => match crate::run_cli::parse_go_duration(value) {
            Some(duration) => duration,
            None => {
                return stderr(
                    &format!(
                        "invalid value \"{value}\" for flag -timeout: parse error\n{REQUEST_FLAGS}"
                    ),
                    CoreExitCode::NoInput,
                );
            }
        },
        None => Duration::from_secs(15 * 60),
    };
    let signer = match load_identity(parsed.values.get("identity")) {
        Ok(identity) => identity,
        Err(code) => return code,
    };
    let room = room_dir();
    let event = match request_checkpoint(&room, run_id, question, &signer) {
        Ok(event) => event,
        Err(error) => {
            return stderr(
                &format!("Error requesting checkpoint: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    let checkpoint_id = match serde_json::from_str::<serde_json::Value>(event.body.get())
        .ok()
        .and_then(|body| {
            body.get("checkpoint_id")
                .and_then(|id| id.as_str())
                .map(str::to_owned)
        }) {
        Some(id) => id,
        None => {
            return stderr(
                "Error requesting checkpoint: invalid checkpoint event\n",
                CoreExitCode::Generic,
            );
        }
    };
    match wait_checkpoint(&room, &checkpoint_id, timeout) {
        Ok(checkpoint) => stdout(
            &format!("{}\n", checkpoint.answer.as_deref().unwrap_or("")),
            CoreExitCode::Ok,
        ),
        Err(WaitError::Timeout) => stderr(
            &format!("Error: wait timed out for checkpoint {checkpoint_id}\n"),
            CoreExitCode::Interrupted,
        ),
    }
}

fn resolve(args: &[OsString]) -> ExitCode {
    let parsed = match parse(args, &["answer", "identity"], RESOLVE_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(checkpoint_id) = parsed.positionals.first() else {
        return stderr(
            "Usage: symroom checkpoint resolve <checkpoint_id> --answer \"...\" [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    };
    let answer = parsed.values.get("answer").map_or("", String::as_str);
    if answer.is_empty() {
        return stderr(
            "Usage: symroom checkpoint resolve <checkpoint_id> --answer \"...\" [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    }
    let signer = match load_identity(parsed.values.get("identity")) {
        Ok(identity) => identity,
        Err(code) => return code,
    };
    match resolve_checkpoint(&room_dir(), checkpoint_id, answer, &signer) {
        Ok(event) => stdout(&format!("{}\n", event.id), CoreExitCode::Ok),
        Err(ResolveError::AgentForbidden) => stderr(
            "Error: agent identity is forbidden from resolving checkpoints\n",
            CoreExitCode::NoInput,
        ),
        Err(ResolveError::NotFound) => stderr(
            "Error resolving checkpoint: checkpoint not found\n",
            CoreExitCode::Generic,
        ),
        Err(ResolveError::AlreadyResolved) => stderr(
            "Error resolving checkpoint: checkpoint already resolved\n",
            CoreExitCode::Generic,
        ),
        Err(ResolveError::Message(error)) => stderr(
            &format!("Error resolving checkpoint: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn parse(args: &[OsString], allowed: &[&str], usage: &str) -> Result<Parsed, ExitCode> {
    let mut parsed = Parsed::default();
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if argument == "--" {
            parsed.positionals.extend(
                args[index + 1..]
                    .iter()
                    .map(|arg| arg.to_string_lossy().into_owned()),
            );
            break;
        }
        if !argument.starts_with('-') || argument == "-" {
            parsed.positionals.extend(
                args[index..]
                    .iter()
                    .map(|arg| arg.to_string_lossy().into_owned()),
            );
            break;
        }
        let flag = argument.trim_start_matches('-');
        let (name, inline) = flag
            .split_once('=')
            .map_or((flag, None), |(name, value)| (name, Some(value)));
        if matches!(name, "h" | "help") && inline.is_none() {
            return Err(stderr(usage, CoreExitCode::Ok));
        }
        if !allowed.contains(&name) {
            return Err(stderr(
                &format!("flag provided but not defined: -{name}\n{usage}"),
                CoreExitCode::NoInput,
            ));
        }
        let value = if let Some(value) = inline {
            value.to_owned()
        } else if let Some(value) = args.get(index + 1) {
            index += 1;
            value.to_string_lossy().into_owned()
        } else {
            return Err(stderr(
                &format!("flag needs an argument: -{name}\n{usage}"),
                CoreExitCode::NoInput,
            ));
        };
        parsed.values.insert(name.to_owned(), value);
        index += 1;
    }
    Ok(parsed)
}

fn request_checkpoint(
    room: &std::path::Path,
    run_id: &str,
    question: &str,
    signer: &Identity,
) -> Result<Event, String> {
    let nonce = time::OffsetDateTime::now_utc().unix_timestamp_nanos();
    let digest = Sha256::digest(format!("{run_id}{question}{nonce}").as_bytes());
    let checkpoint_id = format!("chk_{}", &hex::encode(digest)[..16]);
    let event_id = format!("ev_{}", &checkpoint_id[4..]);
    let body = go_json(&BTreeMap::from([
        ("checkpoint_id", checkpoint_id.as_str()),
        ("run_id", run_id),
        ("question", question),
    ]))?;
    append_signed_event(room, signer, event_id, "checkpoint.requested", body)
}

fn resolve_checkpoint(
    room: &std::path::Path,
    checkpoint_id: &str,
    answer: &str,
    signer: &Identity,
) -> Result<Event, ResolveError> {
    let events =
        journal::merge_all(room).map_err(|error| ResolveError::Message(error.to_string()))?;
    let mut state = members::State::default();
    for event in &events {
        state.apply_event(event).map_err(ResolveError::Message)?;
    }
    if state
        .members
        .get(&signer.member_id)
        .is_some_and(|member| member.role == "agent")
    {
        return Err(ResolveError::AgentForbidden);
    }
    let checkpoints = runs::project_checkpoints(&events);
    let Some(checkpoint) = checkpoints.get(checkpoint_id) else {
        return Err(ResolveError::NotFound);
    };
    if checkpoint.state == "resolved" {
        return Err(ResolveError::AlreadyResolved);
    }
    let body = go_json(&BTreeMap::from([
        ("checkpoint_id", checkpoint_id),
        ("answer", answer),
    ]))
    .map_err(ResolveError::Message)?;
    let digest = Sha256::digest(format!("{checkpoint_id}{answer}").as_bytes());
    let event_id = format!("ev_{}", &hex::encode(digest)[..16]);
    append_signed_event(room, signer, event_id, "checkpoint.resolved", body)
        .map_err(ResolveError::Message)
}

fn append_signed_event(
    room: &std::path::Path,
    signer: &Identity,
    id: String,
    kind: &str,
    body: String,
) -> Result<Event, String> {
    let stats = journal::read_journal_stats(room).map_err(|error| error.to_string())?;
    let author =
        journal::author_stats(room, &signer.member_id).map_err(|error| error.to_string())?;
    let body = RawValue::from_string(body).map_err(|error| error.to_string())?;
    let mut event = Event {
        v: event::CURRENT_VERSION,
        id,
        room: "rm_test".to_owned(),
        author: signer.member_id.clone(),
        seq: author.seq.saturating_add(1),
        prev: author.prev,
        lamport: stats.max_lamport.saturating_add(1),
        ts: event::format_timestamp(time::OffsetDateTime::now_utc()),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    event.sign(signer).map_err(|error| error.to_string())?;
    journal::append_event(room, &event).map_err(|error| error.to_string())?;
    Ok(event)
}

fn wait_checkpoint(
    room: &std::path::Path,
    checkpoint_id: &str,
    timeout: Duration,
) -> Result<Checkpoint, WaitError> {
    let started = Instant::now();
    loop {
        if let Ok(events) = journal::merge_all(room)
            && let Some(checkpoint) = runs::project_checkpoints(&events).remove(checkpoint_id)
            && checkpoint.state == "resolved"
        {
            return Ok(checkpoint);
        }
        let elapsed = started.elapsed();
        if elapsed >= timeout {
            return Err(WaitError::Timeout);
        }
        thread::sleep((timeout - elapsed).min(Duration::from_millis(500)));
    }
}

fn load_identity(name: Option<&String>) -> Result<Identity, ExitCode> {
    let identity_name = match name.filter(|value| !value.is_empty()) {
        Some(name) => name.clone(),
        None => match default_identity() {
            Ok(name) if !name.is_empty() => name,
            Ok(_) => {
                return Err(stderr(
                    "Error: --identity is required when default_identity is not configured\n",
                    CoreExitCode::NoInput,
                ));
            }
            Err(error) => {
                return Err(stderr(
                    &format!("Error loading configuration: {error}\n"),
                    CoreExitCode::NoInput,
                ));
            }
        },
    };
    identity::load(&identity_name).map_err(|error| {
        stderr(
            &format!("Error loading identity {identity_name}: {error}\n"),
            CoreExitCode::NotFound,
        )
    })
}

fn default_identity() -> Result<String, String> {
    let home = home_dir()?;
    let mut name = merge_identity_config(
        &home.join(".config/symroom/config.toml"),
        "global config error",
        String::new(),
    )?;
    if let Ok(cwd) = std::env::current_dir() {
        name = merge_identity_config(&cwd.join(".symroom.toml"), "project config error", name)?;
    }
    if let Ok(value) = std::env::var("SYMROOM_DEFAULT_IDENTITY")
        && !value.is_empty()
    {
        name = value;
    }
    Ok(name)
}

fn home_dir() -> Result<PathBuf, String> {
    #[cfg(windows)]
    let home = std::env::var_os("USERPROFILE");
    #[cfg(not(windows))]
    let home = std::env::var_os("HOME");
    home.filter(|path| !path.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| "cannot determine home directory".to_owned())
}

fn merge_identity_config(
    path: &std::path::Path,
    source: &str,
    current: String,
) -> Result<String, String> {
    let contents = match std::fs::read_to_string(path) {
        Ok(contents) => contents,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(current),
        Err(error) => {
            return Err(format!(
                "{source}: failed to parse {}: {error}",
                path.display()
            ));
        }
    };
    let config: toml::Value = toml::from_str(&contents)
        .map_err(|error| format!("{source}: failed to parse {}: {error}", path.display()))?;
    if config.get("adapters").is_some() {
        return Err(format!(
            "{source}: failed to apply {}: field \"adapters\": map fields are not supported from config",
            path.display()
        ));
    }
    match config.get("default_identity") {
        None => Ok(current),
        Some(toml::Value::String(value)) if value.is_empty() => Ok(current),
        Some(toml::Value::String(value)) => Ok(value.clone()),
        Some(value) => Err(format!(
            "{source}: failed to apply {}: field default_identity: expected string, got {}",
            path.display(),
            match value {
                toml::Value::Integer(_) => "int64",
                toml::Value::Float(_) => "float64",
                toml::Value::Boolean(_) => "bool",
                toml::Value::Datetime(_) => "time.Time",
                toml::Value::Array(_) => "[]interface {}",
                toml::Value::Table(_) => "map[string]interface {}",
                toml::Value::String(_) => unreachable!(),
            }
        )),
    }
}

fn go_json(value: &impl serde::Serialize) -> Result<String, String> {
    let rendered = serde_json::to_string(value).map_err(|error| error.to_string())?;
    Ok(rendered
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

enum WaitError {
    Timeout,
}

enum ResolveError {
    AgentForbidden,
    NotFound,
    AlreadyResolved,
    Message(String),
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
