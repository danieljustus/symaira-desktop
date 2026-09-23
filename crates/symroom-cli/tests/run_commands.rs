#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const ORACLE_REVISION: &str = "a80da93e3ec02801c73aa5b2318dc06de3efd3fa";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: std::collections::BTreeMap<String, String>,
    journal_files: Vec<JournalFile>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct JournalFile {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    room: String,
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[test]
fn run_list_and_show_match_go_process_contract() {
    let fixture_path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/run-cli.json");
    let data = fs::read(&fixture_path).expect("read Go-generated CLI fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse Go-generated fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 4);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert!(!fixture.cases.is_empty());
    assert!(fixture.cases.iter().any(|case| case.exit_code == 5));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 2));

    let temp = TempDir::new();
    let main_room = temp.path.join("main");
    let journal = main_room.join("journal");
    fs::create_dir_all(&journal).expect("create fixture journal");
    for file in &fixture.journal_files {
        fs::write(journal.join(&file.name), file.content.as_bytes())
            .expect("write Go-signed fixture journal segment");
    }
    let empty_room = temp.path.join("empty");
    fs::create_dir(&empty_room).expect("create empty room");

    for case in &fixture.cases {
        let room = match case.room.as_str() {
            "main" => &main_room,
            "empty" => &empty_room,
            other => panic!("unknown fixture room {other}"),
        };
        let isolated = temp.path.join(format!("env-{}", case.name));
        let home = isolated.join("home");
        let data_home = isolated.join("data");
        let temp_dir = isolated.join("tmp");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&data_home).expect("create isolated XDG data home");
        fs::create_dir_all(&temp_dir).expect("create isolated TMPDIR");
        let output = run_symroom(&case.args, room, &home, &data_home, &temp_dir);
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "case {}",
            case.name
        );
        assert_eq!(
            output.stdout,
            case.stdout.as_bytes(),
            "stdout case {}",
            case.name
        );
        assert_eq!(
            output.stderr,
            case.stderr.as_bytes(),
            "stderr case {}",
            case.name
        );
    }
}

fn run_symroom(args: &[String], room: &Path, home: &Path, data_home: &Path, temp: &Path) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .env_clear()
        .env("HOME", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", temp)
        .env("SYMROOM_ROOM_DIR", room);
    command.output().expect("run Rust symroom CLI")
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after Unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symroom-cli-contract-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create isolated CLI fixture directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated CLI fixture directory");
    }
}
