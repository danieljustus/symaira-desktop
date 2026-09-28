#![deny(unsafe_code)]

use std::{collections::BTreeMap, fs, path::PathBuf};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::{event::Event, identity, room_init};

#[derive(Deserialize)]
struct Fixture {
    schema_version: i64,
    files: BTreeMap<String, String>,
    #[cfg(unix)]
    modes: BTreeMap<String, String>,
    nonempty_error: String,
    preserved: String,
}

#[test]
fn replays_go_room_init_contract() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/init.json")).expect("Go Init fixture"),
    )
    .expect("fixture JSON");
    assert_eq!(fixture.schema_version, 1);

    let temp = TempDir::new();
    let room = temp.path.join("room");
    fs::create_dir(&room).expect("empty room dir");
    let seed = Sha256::digest(b"symroom-port-identity/room-init");
    let owner = identity::identity_from_private_key("room-init", &seed).expect("Go identity");
    let now = time::OffsetDateTime::new_utc(
        time::Date::from_calendar_date(2026, time::Month::January, 2).unwrap(),
        time::Time::from_hms_milli(3, 4, 5, 6).unwrap(),
    );
    let config = room_init::init(
        &room,
        "Parity Room",
        &owner,
        "rm_0123456789abcdef",
        "ev_0123456789abcdef0123",
        now,
    )
    .expect("initialize room");
    assert_eq!(config.id, "rm_0123456789abcdef");
    assert_eq!(config.created, "2026-01-02T03:04:05.006Z");

    for (name, expected) in &fixture.files {
        assert_eq!(
            fs::read(room.join(name)).expect("Go fixture output exists"),
            expected.as_bytes(),
            "Go/Rust bytes for {name}"
        );
    }
    #[cfg(unix)]
    for (name, expected) in &fixture.modes {
        assert_eq!(
            mode(&room.join(name)),
            expected.as_str(),
            "Go/Rust mode for {name}"
        );
    }
    let journal_name = format!("journal/{}.jsonl", owner.member_id);
    let journal = fs::read(room.join(journal_name)).expect("created event");
    let event = Event::unmarshal_json_line(&journal).expect("signed event parses");
    event
        .verify_signature(&owner.public_key)
        .expect("created event signature verifies");

    let nonempty = temp.path.join("nonempty");
    fs::create_dir(&nonempty).expect("nonempty room dir");
    fs::write(nonempty.join("keep"), fixture.preserved.as_bytes()).expect("preserved source");
    let result = room_init::init(
        &nonempty,
        "Parity Room",
        &owner,
        "rm_0123456789abcdef",
        "ev_0123456789abcdef0123",
        now,
    );
    assert_eq!(result.unwrap_err().to_string(), fixture.nonempty_error);
    assert_eq!(
        fs::read(nonempty.join("keep")).unwrap(),
        fixture.preserved.as_bytes()
    );
    assert_eq!(
        fs::read_dir(&nonempty).unwrap().count(),
        1,
        "source state unchanged"
    );
}

#[cfg(unix)]
fn mode(path: &std::path::Path) -> String {
    use std::os::unix::fs::PermissionsExt;
    format!(
        "{:04o}",
        fs::metadata(path).unwrap().permissions().mode() & 0o7777
    )
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "symroom-init-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir(&path).expect("temporary directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
