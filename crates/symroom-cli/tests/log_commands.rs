#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::PathBuf,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};

#[derive(Deserialize)]
struct JournalFixture {
    cases: Vec<JournalCase>,
}
#[derive(Deserialize)]
struct JournalCase {
    name: String,
    files: BTreeMap<String, String>,
}
#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct Case {
    name: String,
    journal_case: String,
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr_prefix: String,
}

#[test]
fn log_cli_matches_go_process_contract() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/log-cli.json")).expect("Go log CLI fixture"),
    )
    .expect("parse Go log CLI fixture");
    let journal: JournalFixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/verify.json")).expect("Go journal fixture"),
    )
    .expect("parse Go journal fixture");
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
    let journals: BTreeMap<_, _> = journal
        .cases
        .into_iter()
        .map(|case| (case.name, case.files))
        .collect();
    let temp = TempDir::new();
    for case in fixture.cases {
        let room = temp.path.join(&case.name);
        let journal_dir = room.join("journal");
        fs::create_dir_all(&journal_dir).expect("create journal");
        for (name, content) in &journals[&case.journal_case] {
            fs::write(journal_dir.join(name), content).expect("write Go journal");
        }
        let output = Command::new(env!("CARGO_BIN_EXE_symroom"))
            .args(&case.args)
            .env_clear()
            .env("SYMROOM_ROOM_DIR", &room)
            .output()
            .expect("run Rust log CLI");
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
        if case.name == "malformed" {
            assert!(
                output.stderr.starts_with(case.stderr_prefix.as_bytes()),
                "{} stderr: {}",
                case.name,
                String::from_utf8_lossy(&output.stderr)
            );
        } else {
            assert_eq!(
                output.stderr,
                case.stderr_prefix.as_bytes(),
                "{} stderr",
                case.name
            );
        }
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
            std::env::temp_dir().join(format!("symroom-log-cli-{}-{nonce}", std::process::id()));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}
impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove scratch directory");
    }
}
