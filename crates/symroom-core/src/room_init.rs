#![deny(unsafe_code)]

//! Initialize a SymRoom directory, ported from `internal/room/room.Init`.

use std::{fs, io::Write, path::Path};

use serde_json::value::RawValue;

use crate::{
    event::{self, Event, EventError},
    identity::Identity,
    journal::ZERO_HASH,
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RoomConfig {
    pub schema_version: i64,
    pub id: String,
    pub created: String,
    pub root_pubkey: String,
    pub root_event: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RoomInitError {
    NotEmpty,
    Io(String),
    Event(EventError),
}

impl std::fmt::Display for RoomInitError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::NotEmpty => f.write_str("room directory is not empty"),
            Self::Io(message) => f.write_str(message),
            Self::Event(error) => write!(f, "sign room.created event: {error}"),
        }
    }
}

impl std::error::Error for RoomInitError {}

/// Create a room using explicit IDs and clock so Go oracle replays are stable.
pub fn init(
    dir: &Path,
    name: &str,
    identity: &Identity,
    room_id: &str,
    event_id: &str,
    now: time::OffsetDateTime,
) -> Result<RoomConfig, RoomInitError> {
    let dir = if dir.is_absolute() {
        dir.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|error| RoomInitError::Io(format!("abs dir: {error}")))?
            .join(dir)
    };
    let entries = match fs::read_dir(&dir) {
        Ok(entries) => Some(entries),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => None,
        Err(error) => return Err(RoomInitError::Io(error.to_string())),
    };
    if let Some(entries) = entries {
        if entries.into_iter().next().is_some() {
            return Err(RoomInitError::NotEmpty);
        }
    }
    fs::create_dir_all(&dir)
        .map_err(|error| RoomInitError::Io(format!("mkdir room dir: {error}")))?;

    let created = event::format_timestamp(now);
    let body = serde_json::json!({
        "name": name,
        "public_key": hex::encode(&identity.public_key),
    });
    let body = serde_json::to_string(&body)
        .map_err(|error| RoomInitError::Io(format!("marshal event body: {error}")))?;
    let mut signed_event = Event {
        v: event::CURRENT_VERSION,
        id: event_id.to_owned(),
        room: room_id.to_owned(),
        author: identity.member_id.clone(),
        seq: 1,
        prev: ZERO_HASH.to_owned(),
        lamport: 1,
        ts: created.clone(),
        kind: "room.created".to_owned(),
        body: RawValue::from_string(body)
            .map_err(|error| RoomInitError::Io(format!("marshal event body: {error}")))?,
        sig: None,
    };
    signed_event.sign(identity).map_err(RoomInitError::Event)?;

    let journal_dir = dir.join("journal");
    create_dir(&journal_dir)
        .map_err(|error| RoomInitError::Io(format!("mkdir journal dir: {error}")))?;
    let event_line = signed_event
        .marshal_json_line()
        .map_err(|error| RoomInitError::Io(format!("marshal event line: {error}")))?;
    write_new(
        &journal_dir.join(format!("{}.jsonl", identity.member_id)),
        &event_line,
    )
    .map_err(|error| RoomInitError::Io(format!("write journal file: {error}")))?;

    let config = RoomConfig {
        schema_version: 1,
        id: room_id.to_owned(),
        created,
        root_pubkey: format!("ed25519:{}", hex::encode(&identity.public_key)),
        root_event: event_id.to_owned(),
    };
    let room_toml = format!(
        "schema_version = {}\nid = {}\ncreated = {}\nroot_pubkey = {}\nroot_event = {}\n",
        config.schema_version,
        toml_string(&config.id),
        toml_string(&config.created),
        toml_string(&config.root_pubkey),
        toml_string(&config.root_event),
    );
    write_new(&dir.join("room.toml"), room_toml.as_bytes())
        .map_err(|error| RoomInitError::Io(format!("write room.toml: {error}")))?;

    let local_dir = dir.join(".symroom");
    create_dir(&local_dir)
        .map_err(|error| RoomInitError::Io(format!("mkdir .symroom: {error}")))?;
    let local_toml = format!(
        "identity = {}\nartifact_root = \"\"\n",
        toml_string(&identity.name),
    );
    write_new(&local_dir.join("local.toml"), local_toml.as_bytes())
        .map_err(|error| RoomInitError::Io(format!("write .symroom/local.toml: {error}")))?;

    let gitignore = dir.join(".gitignore");
    if !gitignore.exists() {
        write_new(&gitignore, b".symroom/\n")
            .map_err(|error| RoomInitError::Io(format!("write .gitignore: {error}")))?;
    }
    Ok(config)
}

fn toml_string(value: &str) -> String {
    // These values contain no control characters in the Go contract surface;
    // JSON string escaping is valid for the TOML basic strings used here.
    serde_json::to_string(value).expect("serializing a string cannot fail")
}

fn create_dir(path: &Path) -> std::io::Result<()> {
    fs::create_dir(path)?;
    set_mode(path, 0o755)
}

fn write_new(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)?;
    file.write_all(bytes)?;
    set_mode(path, 0o644)
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode))
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) -> std::io::Result<()> {
    Ok(())
}
