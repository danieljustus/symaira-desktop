//! Rebuildable SQLite projection of the signed SymRoom journal.
//!
//! The Markdown/journal files remain authoritative. This database can always
//! be discarded and rebuilt from the event segments.

use std::{fs, path::Path};

use rusqlite::{Connection, params};
use serde::Deserialize;

use crate::{event::Event, journal, members::State};

const SCHEMA: &str = "
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    room TEXT,
    author TEXT,
    seq INTEGER,
    lamport INTEGER,
    ts TEXT,
    kind TEXT,
    body TEXT
);
CREATE TABLE IF NOT EXISTS members (
    id TEXT PRIMARY KEY,
    name TEXT,
    public_key TEXT,
    role TEXT,
    kind TEXT
);
CREATE TABLE IF NOT EXISTS notes (
    event_id TEXT PRIMARY KEY,
    author TEXT,
    ts TEXT,
    text TEXT
);
CREATE TABLE IF NOT EXISTS decisions (
    event_id TEXT PRIMARY KEY,
    author TEXT,
    ts TEXT,
    text TEXT,
    refs TEXT
);
";

#[derive(Debug, thiserror::Error)]
pub enum IndexError {
    #[error("sqlite: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("filesystem: {0}")]
    Io(#[from] std::io::Error),
    #[error("sqlite connection: {0}")]
    Connection(#[from] symaira_core_sqlite::Error),
    #[error("merge all events: {0}")]
    Merge(#[from] journal::ReadSegmentsError),
    #[error("event {event_id} {field} exceeds SQLite INTEGER range")]
    IntegerRange {
        event_id: String,
        field: &'static str,
    },
}

#[derive(Deserialize, Default)]
struct NoteBody {
    #[serde(default)]
    text: String,
}

#[derive(Deserialize, Default)]
struct DecisionBody {
    #[serde(default)]
    text: String,
    #[serde(default)]
    refs: Option<Vec<String>>,
}

/// Rebuilds `db_path` from the authoritative journal in `room_dir`.
///
/// Like the Go indexer, this drops the old derived database before replay. A
/// corrupt journal leaves a new empty-schema database and returns the merge
/// error; journal files are never changed.
pub fn rebuild(db_path: &Path, room_dir: &Path) -> Result<(), IndexError> {
    let _ = fs::remove_file(db_path);
    let mut connection = open_connection(db_path)?;
    connection.execute_batch(SCHEMA)?;

    let events = journal::merge_all(room_dir)?;
    let transaction = connection.transaction()?;
    let mut state = State::default();
    for event in &events {
        let _ = state.apply_event(event);
        insert_event(&transaction, event)?;
    }
    for member in state.members.values() {
        // Go stores the decoded public key as SQLite TEXT bytes. CAST keeps
        // the same declared/storage type even for arbitrary non-UTF8 keys.
        let key = hex::decode(&member.public_key).unwrap_or_default();
        transaction.execute(
            "INSERT OR REPLACE INTO members (id, name, public_key, role, kind) VALUES (?, ?, CAST(? AS TEXT), ?, ?)",
            params![member.id, member.name, key, member.role, member.kind],
        )?;
    }
    transaction.commit()?;
    Ok(())
}

fn insert_event(transaction: &rusqlite::Transaction<'_>, event: &Event) -> Result<(), IndexError> {
    let seq = i64::try_from(event.seq).map_err(|_| IndexError::IntegerRange {
        event_id: event.id.clone(),
        field: "seq",
    })?;
    let lamport = i64::try_from(event.lamport).map_err(|_| IndexError::IntegerRange {
        event_id: event.id.clone(),
        field: "lamport",
    })?;
    transaction.execute(
        "INSERT OR REPLACE INTO events (id, room, author, seq, lamport, ts, kind, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        params![event.id, event.room, event.author, seq, lamport, event.ts, event.kind, event.body.get()],
    )?;

    if event.kind == "note.posted" {
        let body = serde_json::from_str::<NoteBody>(event.body.get()).unwrap_or_default();
        transaction.execute(
            "INSERT OR REPLACE INTO notes (event_id, author, ts, text) VALUES (?, ?, ?, ?)",
            params![event.id, event.author, event.ts, body.text],
        )?;
    }
    if event.kind == "decision.recorded" {
        let body = serde_json::from_str::<DecisionBody>(event.body.get()).unwrap_or_default();
        let refs = serde_json::to_string(&body.refs).expect("optional string list serializes");
        transaction.execute(
            "INSERT OR REPLACE INTO decisions (event_id, author, ts, text, refs) VALUES (?, ?, ?, ?, ?)",
            params![event.id, event.author, event.ts, body.text, refs],
        )?;
    }
    Ok(())
}

fn open_connection(path: &Path) -> Result<Connection, IndexError> {
    Ok(symaira_core_sqlite::open(path)?)
}

/// Query helpers intentionally sort by stable keys so callers never depend on
/// SQLite's unspecified row order.
pub fn event_ids(db_path: &Path) -> Result<Vec<String>, IndexError> {
    let connection = open_connection(db_path)?;
    let mut statement =
        connection.prepare("SELECT id FROM events ORDER BY lamport, ts, author, seq, id")?;
    Ok(statement
        .query_map([], |row| row.get(0))?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn note_texts(db_path: &Path) -> Result<Vec<(String, String)>, IndexError> {
    let connection = open_connection(db_path)?;
    let mut statement =
        connection.prepare("SELECT event_id, text FROM notes ORDER BY ts, event_id")?;
    Ok(statement
        .query_map([], |row| Ok((row.get(0)?, row.get(1)?)))?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn decision_texts(db_path: &Path) -> Result<Vec<(String, String, String)>, IndexError> {
    let connection = open_connection(db_path)?;
    let mut statement =
        connection.prepare("SELECT event_id, text, refs FROM decisions ORDER BY ts, event_id")?;
    Ok(statement
        .query_map([], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)))?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn member_ids(db_path: &Path) -> Result<Vec<String>, IndexError> {
    let connection = open_connection(db_path)?;
    let mut statement = connection.prepare("SELECT id FROM members ORDER BY id")?;
    Ok(statement
        .query_map([], |row| row.get(0))?
        .collect::<Result<Vec<_>, _>>()?)
}
