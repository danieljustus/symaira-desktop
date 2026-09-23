#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};

const ORACLE_REVISION: &str = "97280a946316682fc3ce3d7650597655ff0e46ae";
const MUTATION_ORACLE_REVISION: &str = "a9f42980e4695e20b2c948d7f17fe67734eff901";

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

#[derive(Deserialize)]
struct WaitFixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: std::collections::BTreeMap<String, String>,
    journal_files: Vec<JournalFile>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct MutationFixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: std::collections::BTreeMap<String, String>,
    identity_key: String,
    identity_member: String,
    journal_files: Vec<JournalFile>,
    cases: Vec<MutationCase>,
}

#[derive(Deserialize)]
struct MutationCase {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    config: String,
    #[serde(default)]
    default_env: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_journal_files: Vec<JournalFile>,
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

#[test]
fn run_wait_matches_go_process_contract() {
    let fixture_path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/run-wait-cli.json");
    let data = fs::read(&fixture_path).expect("read Go-generated run wait fixture");
    let fixture: WaitFixture = serde_json::from_slice(&data).expect("parse Go wait fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 5);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 4));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 10));

    let temp = TempDir::new();
    let main_room = temp.path.join("main");
    let journal = main_room.join("journal");
    fs::create_dir_all(&journal).expect("create wait fixture journal");
    for file in &fixture.journal_files {
        fs::write(journal.join(&file.name), file.content.as_bytes())
            .expect("write Go-signed wait fixture segment");
    }
    let bad_room = temp.path.join("bad-journal");
    fs::create_dir(&bad_room).expect("create bad-journal room");
    fs::write(bad_room.join("journal"), b"not a directory").expect("write bad-journal marker");

    for case in &fixture.cases {
        let room = match case.room.as_str() {
            "main" => &main_room,
            "bad-journal" => &bad_room,
            other => panic!("unknown wait fixture room {other}"),
        };
        let isolated = temp.path.join(format!("wait-env-{}", case.name));
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

#[test]
fn run_request_start_cancel_match_go_process_contract() {
    let fixture_path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/room/run-mutations-cli.json");
    let data = fs::read(&fixture_path).expect("read Go-generated run mutation fixture");
    let fixture: MutationFixture =
        serde_json::from_slice(&data).expect("parse Go mutation fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, MUTATION_ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 7);
    for (path, expected) in &fixture.source_hashes {
        let source = fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../..")
                .join(path),
        )
        .expect("read Go oracle source");
        assert_eq!(hex::encode(Sha256::digest(source)), *expected, "{path}");
    }
    assert!(fixture.cases.iter().any(|case| case.stdout.ends_with('\n')));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 2));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 1));

    let temp = TempDir::new();
    for case in &fixture.cases {
        let room = temp.path.join(format!("room-{}", case.name));
        let journal = room.join("journal");
        fs::create_dir_all(&journal).expect("create mutation fixture journal");
        for file in &fixture.journal_files {
            fs::write(journal.join(&file.name), file.content.as_bytes())
                .expect("write Go-signed mutation fixture segment");
        }

        let isolated = temp.path.join(format!("mutation-env-{}", case.name));
        let home = isolated.join("home");
        let data_home = isolated.join("data");
        let temp_dir = isolated.join("tmp");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&data_home).expect("create isolated XDG data home");
        fs::create_dir_all(&temp_dir).expect("create isolated TMPDIR");
        if !case.config.is_empty() {
            let config = home.join(".config/symroom/config.toml");
            fs::create_dir_all(config.parent().expect("config parent"))
                .expect("create config parent");
            fs::write(config, &case.config).expect("write default identity config");
        }
        let output = run_mutation_symroom(
            &case.args,
            &room,
            &home,
            &data_home,
            &temp_dir,
            &fixture.identity_key,
            &case.default_env,
        );
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

        let actual = read_mutation_journal(&journal, &fixture.identity_member);
        let expected: Vec<_> = case
            .final_journal_files
            .iter()
            .map(|file| (file.name.clone(), file.content.clone()))
            .collect();
        assert_eq!(actual, expected, "journal effects case {}", case.name);
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
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", room);
    command.output().expect("run Rust symroom CLI")
}

fn run_mutation_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    temp: &Path,
    identity_key: &str,
    default_env: &str,
) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .env_clear()
        .env("HOME", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", temp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", room)
        .env("SYMROOM_IDENTITY_KEY", identity_key)
        .env("SYMROOM_DEFAULT_IDENTITY", default_env);
    command.output().expect("run Rust symroom mutation CLI")
}

fn read_mutation_journal(journal: &Path, identity_member: &str) -> Vec<(String, String)> {
    let mut files = fs::read_dir(journal)
        .expect("read mutation journal")
        .map(|entry| entry.expect("read journal entry").path())
        .collect::<Vec<_>>();
    files.sort();
    files
        .into_iter()
        .map(|path| {
            let name = path
                .file_name()
                .expect("journal filename")
                .to_string_lossy()
                .to_string();
            let data = fs::read_to_string(&path).expect("read journal file");
            let content = if name == format!("{identity_member}.jsonl") {
                data.lines()
                    .map(normalize_dynamic_event)
                    .collect::<Vec<_>>()
                    .join("\n")
                    + "\n"
            } else {
                data
            };
            (name, content)
        })
        .collect()
}

fn normalize_dynamic_event(line: &str) -> String {
    let mut event: serde_json::Value = serde_json::from_str(line).expect("parse generated event");
    let object = event.as_object_mut().expect("generated event object");
    object.insert(
        "ts".to_owned(),
        serde_json::Value::String("<dynamic-clock>".to_owned()),
    );
    object.insert(
        "sig".to_owned(),
        serde_json::Value::String("<signature-of-dynamic-clock>".to_owned()),
    );
    serde_json::to_string(&event)
        .expect("serialize normalized generated event")
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
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
