#![deny(unsafe_code)]

//! Documented deviation from Go (`identity::sign`): a 64-byte `SYMROOM_IDENTITY_KEY`
//! whose public half does not belong to its seed loads like Go (the identity
//! shows the stored half) but cannot sign. Go would journal an event whose
//! signature verifies against neither key; Rust refuses. There is no Go fixture
//! for the refusal, so this pins the Rust behaviour: non-zero exit, a clear
//! message and an untouched journal.

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    sync::atomic::{AtomicU64, Ordering},
};

use serde::Deserialize;
use symroom_core::identity;

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Deserialize)]
struct Fixture {
    identity_key: String,
    room_toml: String,
    journal_files: Vec<JournalFile>,
}

#[derive(Deserialize)]
struct JournalFile {
    name: String,
    content: String,
}

#[test]
fn note_with_a_mismatched_key_pair_fails_and_leaves_the_journal_untouched() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/note-cli.json"),
        )
        .expect("Go-generated note fixture"),
    )
    .expect("parse note fixture");
    let seed = hex::decode(&fixture.identity_key).expect("fixture seed");
    let other = identity::identity_from_private_key("other", &[9_u8; 32]).expect("other identity");
    let mut mismatched = seed.clone();
    mismatched.extend_from_slice(&other.public_key);
    let consistent = identity::identity_from_private_key("oracle", &seed).expect("seed identity");
    assert_ne!(
        consistent.public_key, other.public_key,
        "the test needs two different public halves"
    );

    let temp = TempDir::new();
    let room = temp.path.join("room");
    let journal = room.join("journal");
    fs::create_dir_all(&journal).expect("create journal");
    fs::write(room.join("room.toml"), &fixture.room_toml).expect("write room.toml");
    for file in &fixture.journal_files {
        fs::write(journal.join(&file.name), file.content.as_bytes()).expect("write journal");
    }
    let before = snapshot(&room);
    assert!(!before.is_empty());

    let refused = run_note(&temp.path, &room, &hex::encode(&mismatched));
    let stderr = String::from_utf8_lossy(&refused.stderr);
    assert_eq!(refused.status.code(), Some(1), "stderr: {stderr}");
    assert!(
        refused.stdout.is_empty(),
        "stdout: {}",
        String::from_utf8_lossy(&refused.stdout)
    );
    assert_eq!(
        stderr,
        "Error posting note: sign note event: refusing to sign: \
         the private key's public half does not match its seed\n"
    );
    assert!(
        !stderr.contains(&hex::encode(&mismatched)) && !stderr.contains(&fixture.identity_key),
        "the message must not leak key material"
    );
    assert_eq!(
        snapshot(&room),
        before,
        "a refused sign must not touch the room"
    );

    // Control: the consistent key posts, so the refusal above is the key's doing.
    let posted = run_note(&temp.path, &room, &fixture.identity_key);
    assert_eq!(
        posted.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&posted.stderr)
    );
    assert_ne!(
        snapshot(&room),
        before,
        "the control note must reach the journal"
    );
}

fn run_note(scratch: &Path, room: &Path, key: &str) -> Output {
    let home = scratch.join("home");
    let data_home = scratch.join("data");
    let tmp = scratch.join("tmp");
    for dir in [&home, &data_home, &tmp] {
        fs::create_dir_all(dir).expect("isolated directory");
    }
    Command::new(env!("CARGO_BIN_EXE_symroom"))
        .args(["note", "--identity", "oracle", "hello"])
        .env_clear()
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_DATA_HOME", &data_home)
        .env("TMPDIR", &tmp)
        .env("TMP", &tmp)
        .env("TEMP", &tmp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", room)
        .env("SYMROOM_IDENTITY_KEY", key)
        // Keep coverage profiles out of the test cwd.
        .env("LLVM_PROFILE_FILE", tmp.join("symroom-%p.profraw"))
        .output()
        .expect("run symroom note")
}

/// Every file below the room as `relative path -> bytes`.
fn snapshot(room: &Path) -> BTreeMap<String, Vec<u8>> {
    fn visit(dir: &Path, base: &Path, files: &mut BTreeMap<String, Vec<u8>>) {
        for entry in fs::read_dir(dir).expect("read room directory") {
            let path = entry.expect("room entry").path();
            if path.is_dir() {
                visit(&path, base, files);
            } else {
                let name = path
                    .strip_prefix(base)
                    .expect("relative path")
                    .to_string_lossy()
                    .replace('\\', "/");
                files.insert(name, fs::read(&path).expect("read room file"));
            }
        }
    }
    let mut files = BTreeMap::new();
    visit(room, room, &mut files);
    files
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "symroom-identity-mismatch-{}-{}",
            std::process::id(),
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
