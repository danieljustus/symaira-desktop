#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const ORACLE_REVISION: &str = "b96219bcd39ce85e017626feade557979aefd7c6";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: std::collections::BTreeMap<String, String>,
    owner_key: String,
    stranger_key: String,
    add_public_key: String,
    room_toml: String,
    initial_files: Vec<JournalFile>,
    cases: Vec<Case>,
}

#[derive(Clone, Deserialize)]
struct JournalFile {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    actor: String,
    #[serde(default)]
    empty_room: bool,
    #[serde(default)]
    dynamic_event: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<JournalFile>,
}

#[test]
fn member_cli_matches_go_process_and_journal_contract() {
    let fixture_path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/member-cli.json");
    let data = fs::read(fixture_path).expect("read Go-generated member fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse member fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 4);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert!(fixture.cases.iter().any(|case| case.dynamic_event));
    assert!(fixture.cases.iter().any(|case| case.empty_room));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 0));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 1));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 2));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 4));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 5));
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "member-unknown-show")
    );
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "add-positional-overrides-flags")
    );

    let temp = TempDir::new();
    for case in &fixture.cases {
        let case_root = temp.path.join(&case.name);
        let room = case_root.join("room");
        let journal = room.join("journal");
        fs::create_dir_all(&journal).expect("create fixture journal");
        fs::write(room.join("room.toml"), fixture.room_toml.as_bytes())
            .expect("write fixture room config");
        if !case.empty_room {
            for file in &fixture.initial_files {
                fs::write(journal.join(&file.name), file.content.as_bytes())
                    .expect("write Go-signed fixture journal");
            }
        }
        let home = case_root.join("home");
        let data_home = case_root.join("data");
        let tmp = case_root.join("tmp");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&data_home).expect("create isolated XDG data home");
        fs::create_dir_all(&tmp).expect("create isolated TMPDIR");
        let identity_key = if case.actor == "stranger" {
            &fixture.stranger_key
        } else {
            &fixture.owner_key
        };
        let output = run_symroom(&case.args, &room, &home, &data_home, &tmp, identity_key);
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code case {}",
            case.name
        );
        assert_eq!(
            normalize_stdout(&output.stdout, case.dynamic_event),
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
        let actual = read_journal(&journal, case.dynamic_event);
        let expected = case
            .final_files
            .iter()
            .map(|file| (file.name.clone(), file.content.clone()))
            .collect::<Vec<_>>();
        assert_eq!(actual, expected, "journal effects case {}", case.name);
        assert_eq!(
            fs::read(room.join("room.toml")).expect("read room config after command"),
            fixture.room_toml.as_bytes(),
            "room config case {}",
            case.name
        );
    }
    assert_eq!(
        hex::decode(&fixture.add_public_key)
            .expect("decode candidate key")
            .len(),
        32
    );
}

fn run_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    tmp: &Path,
    identity_key: &str,
) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .env_clear()
        .env("HOME", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", tmp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", room)
        .env("SYMROOM_IDENTITY_KEY", identity_key);
    command.output().expect("run Rust symroom member CLI")
}

fn normalize_stdout(output: &[u8], dynamic_event: bool) -> Vec<u8> {
    if !dynamic_event {
        return output.to_vec();
    }
    let mut normalized = output.to_vec();
    let event_id = normalized
        .windows(3)
        .position(|window| window == b"ev_")
        .expect("event id in mutation output");
    let end = event_id + 23;
    assert!(
        normalized[event_id + 3..end]
            .iter()
            .all(u8::is_ascii_hexdigit)
    );
    normalized.splice(event_id..end, b"<event-id>".iter().copied());
    normalized
}

fn read_journal(dir: &Path, normalize_last: bool) -> Vec<(String, String)> {
    let mut paths = fs::read_dir(dir)
        .expect("read journal directory")
        .map(|entry| entry.expect("read journal entry").path())
        .filter(|path| path.is_file() && path.extension().is_some_and(|ext| ext == "jsonl"))
        .collect::<Vec<_>>();
    paths.sort();
    paths
        .into_iter()
        .map(|path| {
            let name = path
                .file_name()
                .expect("journal filename")
                .to_string_lossy()
                .into_owned();
            let content = fs::read_to_string(&path).expect("read journal segment");
            let content = if normalize_last {
                let mut lines = content.lines().map(str::to_owned).collect::<Vec<_>>();
                let last = lines.last_mut().expect("new member event");
                let mut event: serde_json::Value =
                    serde_json::from_str(last).expect("parse generated member event");
                let object = event.as_object_mut().expect("member event object");
                object.insert("id".into(), "<event-id>".into());
                object.insert("ts".into(), "<dynamic-clock>".into());
                object.insert("sig".into(), "<signature>".into());
                *last = go_json(&event);
                lines.join("\n") + "\n"
            } else {
                content
            };
            (name, content)
        })
        .collect()
}

fn go_json(value: &serde_json::Value) -> String {
    serde_json::to_string(value)
        .expect("serialize normalized event")
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
            .expect("system clock after epoch")
            .as_nanos();
        let path =
            std::env::temp_dir().join(format!("symroom-member-cli-{}-{nonce}", std::process::id()));
        fs::create_dir_all(&path).expect("create temporary test directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
