#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::index;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: std::collections::BTreeMap<String, String>,
    journal_line: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    room_override: bool,
    #[serde(default)]
    corrupt: bool,
    exit_code: i32,
    stdout: String,
    stderr_prefix: String,
    db_exists: bool,
    event_ids: Option<Vec<String>>,
}

#[test]
fn index_cli_matches_go_process_and_db_contract() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/index-cli.json")).expect("Go index CLI fixture"),
    )
    .expect("parse Go fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 3);
    for (path, hash) in &fixture.source_hashes {
        assert_eq!(
            hex::encode(Sha256::digest(
                fs::read(root.join(path)).expect("Go source")
            )),
            *hash
        );
    }
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.db_exists && !case.corrupt)
    );
    assert!(fixture.cases.iter().any(|case| case.corrupt));

    let temp = TempDir::new();
    for case in &fixture.cases {
        let case_root = temp.path.join(&case.name);
        let room = case_root.join("room");
        let journal = room.join("journal");
        fs::create_dir_all(&journal).expect("create journal");
        let content = if case.corrupt {
            "{\"v\":\n"
        } else {
            &fixture.journal_line
        };
        fs::write(journal.join("mem_cli.jsonl"), content).expect("write journal");
        #[cfg(unix)]
        for path in [&case_root, &room, &journal] {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(path, fs::Permissions::from_mode(0o700))
                .expect("private directory");
        }

        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        command
            .args(&case.args[1..])
            .current_dir(&case_root)
            .env_clear();
        if case.room_override {
            command.env("SYMROOM_ROOM_DIR", &room);
        }
        let output = command.output().expect("run Rust symroom index CLI");
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "{} exit",
            case.name
        );
        assert_eq!(
            output.stdout,
            case.stdout.as_bytes(),
            "{} stdout",
            case.name
        );
        assert!(
            output.stderr.starts_with(case.stderr_prefix.as_bytes()),
            "{} stderr: {}",
            case.name,
            String::from_utf8_lossy(&output.stderr)
        );
        if case.stderr_prefix.is_empty() {
            assert!(output.stderr.is_empty(), "{} unexpected stderr", case.name);
        }
        let db_path = case_root.join(".symroom/index.sqlite");
        assert_eq!(
            db_path.exists(),
            case.db_exists,
            "{} DB location",
            case.name
        );
        if case.db_exists {
            assert_eq!(
                index::event_ids(&db_path).expect("read indexed events"),
                case.event_ids.clone().unwrap_or_default(),
                "{} indexed events",
                case.name
            );
        }
        assert!(
            !room.join(".symroom/index.sqlite").exists(),
            "{} DB must remain relative to cwd",
            case.name
        );
    }
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path =
            std::env::temp_dir().join(format!("symroom-index-cli-{}-{nonce}", std::process::id()));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove scratch directory");
    }
}
