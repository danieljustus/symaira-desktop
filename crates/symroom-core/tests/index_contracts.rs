#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use rusqlite::Connection;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::{index, journal};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hash: String,
    rebuild: Snapshot,
    corruption: Corruption,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Snapshot {
    tables: Vec<String>,
    columns: Vec<Column>,
    events: Vec<IndexedEvent>,
    members: Vec<IndexedMember>,
    notes: Vec<IndexedNote>,
    decisions: Vec<IndexedDecision>,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Column {
    table: String,
    name: String,
    #[serde(rename = "type")]
    kind: String,
    not_null: i64,
    primary_key: i64,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct IndexedEvent {
    id: String,
    room: String,
    author: String,
    seq: i64,
    lamport: i64,
    ts: String,
    kind: String,
    body: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct IndexedMember {
    id: String,
    name: String,
    public_key: String,
    role: String,
    kind: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct IndexedNote {
    event_id: String,
    author: String,
    ts: String,
    text: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct IndexedDecision {
    event_id: String,
    author: String,
    ts: String,
    text: String,
    refs: String,
}

#[derive(Deserialize)]
struct Corruption {
    error_class: String,
    error_prefix: String,
    snapshot: Snapshot,
}

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn scratch(label: &str) -> PathBuf {
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("clock after epoch")
        .as_nanos();
    std::env::temp_dir().join(format!(
        "symroom-index-{}-{label}-{nonce}",
        std::process::id()
    ))
}

#[test]
fn go_room_index_schema_replay_rebuild_queries_and_corruption() {
    let root = root();
    let fixture: Fixture = serde_json::from_str(
        &fs::read_to_string(root.join("testdata/port/room/index.json")).expect("Go index fixture"),
    )
    .expect("fixture JSON");
    assert_eq!(fixture.schema_version, 1);
    let mut source = fs::read(root.join("internal/room/index/index.go")).expect("Go index source");
    source.extend_from_slice(
        &fs::read(root.join("internal/room/members/members.go")).expect("Go members source"),
    );
    assert_eq!(fixture.source_hash, hex::encode(Sha256::digest(source)));

    let room = scratch("valid");
    let journal_dir = room.join("journal");
    fs::create_dir_all(&journal_dir).expect("journal directory");
    let owner = "mem_owner";
    let root_key = "41".repeat(32);
    let guest_key = "42".repeat(32);
    let events = [
        event_json(
            "ev_root",
            owner,
            1,
            1,
            "2026-09-23T10:00:00.000Z",
            "room.created",
            &format!(r#"{{"name":"Oracle room","public_key":"{root_key}"}}"#),
        ),
        event_json(
            "ev_member",
            owner,
            2,
            2,
            "2026-09-23T10:00:01.000Z",
            "member.added",
            &format!(
                r#"{{"id":"mem_guest","name":"Guest","public_key":"{guest_key}","role":"agent","kind":"agent"}}"#
            ),
        ),
        event_json(
            "ev_note_1",
            owner,
            3,
            3,
            "2026-09-23T10:00:02.000Z",
            "note.posted",
            r#"{"text":"café note"}"#,
        ),
        event_json(
            "ev_decision",
            owner,
            4,
            4,
            "2026-09-23T10:00:03.000Z",
            "decision.recorded",
            r#"{"text":"recorded choice","refs":["ev_note_1","doc_2"]}"#,
        ),
        event_json(
            "ev_note_2",
            owner,
            5,
            5,
            "2026-09-23T10:00:04.000Z",
            "note.posted",
            r#"{"text":"second note"}"#,
        ),
    ];
    let journal = events.join("\n") + "\n";
    fs::write(journal_dir.join(format!("{owner}.jsonl")), journal).expect("journal events");
    let db_path = room.join(".symroom/index.sqlite");
    seed_stale_db(&db_path);
    index::rebuild(&db_path, &room).expect("rebuild index");
    insert_stale_row(&db_path);
    index::rebuild(&db_path, &room).expect("second rebuild replaces stale data");
    let rebuilt_snapshot = snapshot(&db_path);
    assert_eq!(
        rebuilt_snapshot, fixture.rebuild,
        "Go/Rust rebuilt schema and rows"
    );
    assert_eq!(
        index::event_ids(&db_path).expect("event query"),
        rebuilt_snapshot
            .events
            .iter()
            .map(|event| event.id.clone())
            .collect::<Vec<_>>(),
    );
    assert_eq!(
        index::note_texts(&db_path).expect("note query"),
        rebuilt_snapshot
            .notes
            .iter()
            .map(|note| (note.event_id.clone(), note.text.clone()))
            .collect::<Vec<_>>(),
    );
    assert_eq!(
        index::decision_texts(&db_path).expect("decision query"),
        rebuilt_snapshot
            .decisions
            .iter()
            .map(|row| (row.event_id.clone(), row.text.clone(), row.refs.clone()))
            .collect::<Vec<_>>(),
    );
    assert_eq!(
        index::member_ids(&db_path).expect("member query"),
        rebuilt_snapshot
            .members
            .iter()
            .map(|member| member.id.clone())
            .collect::<Vec<_>>(),
    );
    fs::remove_dir_all(room).expect("remove valid scratch room");

    let corrupt_room = scratch("corrupt");
    let corrupt_journal = corrupt_room.join("journal");
    fs::create_dir_all(&corrupt_journal).expect("corrupt journal directory");
    fs::write(corrupt_journal.join("broken.jsonl"), b"{\"v\":\n").expect("corrupt line");
    let corrupt_db = corrupt_room.join(".symroom/index.sqlite");
    seed_stale_db(&corrupt_db);
    let error =
        index::rebuild(&corrupt_db, &corrupt_room).expect_err("malformed source fails closed");
    assert_eq!(fixture.corruption.error_class, "malformed_event");
    assert!(
        error
            .to_string()
            .starts_with(&fixture.corruption.error_prefix)
    );
    assert!(
        matches!(error, index::IndexError::Merge(journal::ReadSegmentsError::Parse { author, .. }) if author == "broken")
    );
    assert_eq!(
        snapshot(&corrupt_db),
        fixture.corruption.snapshot,
        "Go/Rust empty schema after corrupt replay"
    );
    fs::remove_dir_all(corrupt_room).expect("remove corrupt scratch room");
}

fn event_json(
    id: &str,
    author: &str,
    seq: u64,
    lamport: u64,
    ts: &str,
    kind: &str,
    body: &str,
) -> String {
    serde_json::json!({
        "v": 1, "id": id, "room": "rm_index", "author": author, "seq": seq,
        "prev": "", "lamport": lamport, "ts": ts, "kind": kind,
        "body": serde_json::from_str::<serde_json::Value>(body).expect("fixture body JSON"),
    })
    .to_string()
}

fn seed_stale_db(path: &std::path::Path) {
    fs::create_dir_all(path.parent().expect("db parent")).expect("db parent directory");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path.parent().unwrap(), fs::Permissions::from_mode(0o700))
            .expect("private db parent");
    }
    let connection = Connection::open(path).expect("old index");
    connection.execute_batch("CREATE TABLE stale_table (value TEXT); INSERT INTO stale_table VALUES ('stale'); CREATE TABLE events (id TEXT PRIMARY KEY); INSERT INTO events VALUES ('stale');").expect("seed old rows");
}

fn insert_stale_row(path: &std::path::Path) {
    let connection = Connection::open(path).expect("open rebuilt index");
    connection
        .execute("INSERT INTO events (id) VALUES ('stale')", [])
        .expect("insert stale row");
}

fn snapshot(path: &std::path::Path) -> Snapshot {
    let connection = Connection::open(path).expect("open index snapshot");
    let mut tables = Vec::new();
    let mut statement = connection.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name").expect("tables statement");
    let rows = statement
        .query_map([], |row| row.get::<_, String>(0))
        .expect("tables query");
    for row in rows {
        tables.push(row.expect("table name"));
    }
    drop(statement);

    let mut columns = Vec::new();
    for table in &tables {
        let mut statement = connection
            .prepare(&format!("PRAGMA table_info({table})"))
            .expect("columns statement");
        let rows = statement
            .query_map([], |row| {
                Ok(Column {
                    table: table.clone(),
                    name: row.get(1)?,
                    kind: row.get(2)?,
                    not_null: row.get(3)?,
                    primary_key: row.get(5)?,
                })
            })
            .expect("columns query");
        for row in rows {
            columns.push(row.expect("column row"));
        }
    }

    let mut events = Vec::new();
    let mut statement = connection.prepare("SELECT id,room,author,seq,lamport,ts,kind,body FROM events ORDER BY lamport,ts,author,seq,id").expect("events statement");
    let rows = statement
        .query_map([], |row| {
            Ok(IndexedEvent {
                id: row.get(0)?,
                room: row.get(1)?,
                author: row.get(2)?,
                seq: row.get(3)?,
                lamport: row.get(4)?,
                ts: row.get(5)?,
                kind: row.get(6)?,
                body: row.get(7)?,
            })
        })
        .expect("events query");
    for row in rows {
        events.push(row.expect("event row"));
    }

    let mut members = Vec::new();
    let mut statement = connection
        .prepare("SELECT id,name,public_key,role,kind FROM members ORDER BY id")
        .expect("members statement");
    let rows = statement
        .query_map([], |row| {
            Ok(IndexedMember {
                id: row.get(0)?,
                name: row.get(1)?,
                public_key: row.get(2)?,
                role: row.get(3)?,
                kind: row.get(4)?,
            })
        })
        .expect("members query");
    for row in rows {
        members.push(row.expect("member row"));
    }

    let mut notes = Vec::new();
    let mut statement = connection
        .prepare("SELECT event_id,author,ts,text FROM notes ORDER BY ts,event_id")
        .expect("notes statement");
    let rows = statement
        .query_map([], |row| {
            Ok(IndexedNote {
                event_id: row.get(0)?,
                author: row.get(1)?,
                ts: row.get(2)?,
                text: row.get(3)?,
            })
        })
        .expect("notes query");
    for row in rows {
        notes.push(row.expect("note row"));
    }

    let mut decisions = Vec::new();
    let mut statement = connection
        .prepare("SELECT event_id,author,ts,text,refs FROM decisions ORDER BY ts,event_id")
        .expect("decisions statement");
    let rows = statement
        .query_map([], |row| {
            Ok(IndexedDecision {
                event_id: row.get(0)?,
                author: row.get(1)?,
                ts: row.get(2)?,
                text: row.get(3)?,
                refs: row.get(4)?,
            })
        })
        .expect("decisions query");
    for row in rows {
        decisions.push(row.expect("decision row"));
    }

    Snapshot {
        tables,
        columns,
        events,
        members,
        notes,
        decisions,
    }
}
