//! Replays the Go-generated SymRoom journal fixture (contract row ROOM-002).
//!
//! Every case is byte- or value-exact: the fixture carries the journal files
//! Go wrote, and this suite rebuilds them in a scratch room, runs the ported
//! readers and compares the recorded numbers, hashes and file bytes.

use std::{
    fs,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use symroom_core::{
    event::Event,
    journal::{ZERO_HASH, append_event, author_stats, read_journal_stats},
    members::Member,
};

#[derive(Deserialize)]
struct Fixture {
    zero_hash: String,
    directory_mode: String,
    file_mode: String,
    stats_cases: Vec<StatsCase>,
    append_cases: Vec<AppendCase>,
    chain_cases: Vec<ChainRow>,
}

#[derive(Deserialize)]
struct JournalFile {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct StatsCase {
    name: String,
    #[serde(default)]
    files: Vec<JournalFile>,
    create_dir: bool,
    author: String,
    max_lamport: u64,
    members: Vec<Member>,
    author_seq: u64,
    author_prev: String,
}

#[derive(Deserialize)]
struct AppendCase {
    name: String,
    #[serde(default)]
    existing: Vec<JournalFile>,
    event: Event,
    file: String,
    content: String,
    entries: Vec<String>,
}

#[derive(Deserialize)]
struct ChainRow {
    author: String,
    seq: u64,
    prev: String,
    event: Event,
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/journal.json");
    let raw = fs::read_to_string(&path).expect("read journal fixture");
    serde_json::from_str(&raw).expect("decode journal fixture")
}

fn scratch(label: &str) -> PathBuf {
    let unique = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("after epoch")
        .as_nanos();
    let root = std::env::temp_dir().join(format!("symroom-journal-{label}-{unique}"));
    fs::create_dir_all(&root).expect("create scratch room");
    root
}

fn materialise(room: &Path, files: &[JournalFile]) {
    let journal = room.join("journal");
    fs::create_dir_all(&journal).expect("create journal dir");
    for file in files {
        fs::write(journal.join(&file.name), file.content.as_bytes()).expect("write journal file");
    }
}

#[test]
fn replays_every_recorded_stats_case() {
    let fixture = fixture();
    assert_eq!(fixture.zero_hash, ZERO_HASH);
    assert_eq!(fixture.stats_cases.len(), 12, "Go journal case inventory");
    for required in [
        "membership-across-sorted-author-files",
        "membership-removal-after-undecodable-line",
    ] {
        assert!(fixture.stats_cases.iter().any(|case| case.name == required));
    }
    for case in &fixture.stats_cases {
        let room = scratch(&case.name);
        if case.create_dir {
            materialise(&room, &case.files);
        }
        let stats = read_journal_stats(&room).expect("read journal stats");
        assert_eq!(
            stats.max_lamport, case.max_lamport,
            "lamport in {}",
            case.name
        );
        assert_eq!(
            stats
                .member_state
                .members
                .values()
                .cloned()
                .collect::<Vec<_>>(),
            case.members,
            "member state in {}",
            case.name
        );
        let author = author_stats(&room, &case.author).expect("author stats");
        assert_eq!(author.seq, case.author_seq, "seq in {}", case.name);
        assert_eq!(author.prev, case.author_prev, "prev hash in {}", case.name);
        fs::remove_dir_all(&room).ok();
    }
}

#[test]
fn appends_the_recorded_bytes() {
    let fixture = fixture();
    for case in &fixture.append_cases {
        let room = scratch(&case.name);
        if !case.existing.is_empty() {
            materialise(&room, &case.existing);
        }
        append_event(&room, &case.event).expect("append event");
        let journal = room.join("journal");
        let written = fs::read_to_string(journal.join(&case.file)).expect("read appended journal");
        assert_eq!(written, case.content, "appended bytes in {}", case.name);

        let mut entries: Vec<String> = fs::read_dir(&journal)
            .expect("read journal dir")
            .map(|entry| {
                entry
                    .expect("dir entry")
                    .file_name()
                    .to_string_lossy()
                    .into_owned()
            })
            .collect();
        entries.sort();
        assert_eq!(entries, case.entries, "journal entries in {}", case.name);
        fs::remove_dir_all(&room).ok();
    }
}

#[test]
fn reproduces_the_recorded_hash_chain() {
    let fixture = fixture();
    let room = scratch("chain");
    for row in &fixture.chain_cases {
        let observed = author_stats(&room, &row.author).expect("author stats");
        assert_eq!(
            observed.seq + 1,
            row.seq,
            "chain sequence for {}",
            row.author
        );
        assert_eq!(observed.prev, row.prev, "chain prev for {}", row.author);

        // Append the very event Go appended at this step, so the next recorded
        // `prev` can only match if the port hashes the same stored bytes.
        append_event(&room, &row.event).expect("append chain event");
    }
    fs::remove_dir_all(&room).ok();
}

/// The Go oracle records `0700` for the journal directory and `0600` for the
/// per-author `.jsonl`. The test below compares those strings against the real
/// filesystem, which Windows cannot observe — reading them here keeps the
/// contract exercised (and the struct fields used) on every platform.
#[test]
fn fixture_records_go_private_modes() {
    let fixture = fixture();
    assert_eq!(fixture.directory_mode, "0700", "journal directory mode");
    assert_eq!(fixture.file_mode, "0600", "journal file mode");
}

#[cfg(unix)]
#[test]
fn creates_the_recorded_posix_modes() {
    use std::os::unix::fs::PermissionsExt;

    let fixture = fixture();
    let room = scratch("modes");
    let event = fixture.append_cases[0].event.clone();
    append_event(&room, &event).expect("append event");
    let journal = room.join("journal");
    let dir_mode = fs::metadata(&journal)
        .expect("stat journal dir")
        .permissions()
        .mode()
        & 0o777;
    let file_mode = fs::metadata(journal.join(format!("{}.jsonl", event.author)))
        .expect("stat journal file")
        .permissions()
        .mode()
        & 0o777;
    assert_eq!(format!("{dir_mode:04o}"), fixture.directory_mode);
    assert_eq!(format!("{file_mode:04o}"), fixture.file_mode);
    fs::remove_dir_all(&room).ok();
}
