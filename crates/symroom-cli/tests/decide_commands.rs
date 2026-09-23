#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const ORACLE_REVISION: &str = "b68f7bccb1e636a0b2c1e1093e5473c3709683b3";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    identity_member: String,
    room_toml: String,
    journal_files: Vec<JournalFile>,
    observer_journal_files: Vec<JournalFile>,
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
    #[serde(default)]
    observer: bool,
    #[serde(default)]
    mutates: bool,
    #[serde(default)]
    json_output: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_journal_files: Vec<JournalFile>,
}

#[test]
fn decide_cli_matches_go_process_and_journal_contract() {
    let fixture_path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/decide-cli.json");
    let data = fs::read(fixture_path).expect("read Go-generated decide fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse decide fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 6);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert!(fixture.cases.iter().any(|case| case.mutates));
    assert!(fixture.cases.iter().any(|case| case.json_output));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 1));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 2));

    let temp = TempDir::new();
    for case in &fixture.cases {
        let room = temp.path.join(format!("room-{}", case.name));
        let journal = room.join("journal");
        fs::create_dir_all(&journal).expect("create fixture journal");
        fs::write(room.join("room.toml"), &fixture.room_toml).expect("write fixture config");
        let files = if case.observer {
            &fixture.observer_journal_files
        } else {
            &fixture.journal_files
        };
        for file in files {
            fs::write(journal.join(&file.name), file.content.as_bytes())
                .expect("write Go-signed journal segment");
        }

        let isolated = temp.path.join(format!("env-{}", case.name));
        let home = isolated.join("home");
        let data_home = isolated.join("data");
        let temp_dir = isolated.join("tmp");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&data_home).expect("create isolated XDG data home");
        fs::create_dir_all(&temp_dir).expect("create isolated TMPDIR");
        let output = run_symroom(
            &case.args,
            &room,
            &home,
            &data_home,
            &temp_dir,
            &fixture.identity_key,
        );
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "case {}",
            case.name
        );
        assert_eq!(
            normalize_stdout(&output.stdout, case.json_output),
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

        let actual = read_journal(&journal, &fixture.identity_member, case.mutates);
        let expected: Vec<_> = case
            .final_journal_files
            .iter()
            .map(|file| (file.name.clone(), file.content.clone()))
            .collect();
        assert_eq!(actual, expected, "journal effects case {}", case.name);
    }
}

fn run_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    temp: &Path,
    identity_key: &str,
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
        .env("SYMROOM_IDENTITY_KEY", identity_key);
    command.output().expect("run Rust symroom decide CLI")
}

fn normalize_stdout(output: &[u8], json_output: bool) -> Vec<u8> {
    if json_output {
        let mut event: serde_json::Value =
            serde_json::from_slice(output).expect("parse decision JSON");
        let object = event.as_object_mut().expect("decision event object");
        object.insert("id".into(), "<event-id>".into());
        object.insert("ts".into(), "<dynamic-clock>".into());
        object.insert("sig".into(), "<signature-of-dynamic-event>".into());
        return go_json(&event)
            .into_bytes()
            .into_iter()
            .chain(*b"\n")
            .collect();
    }
    normalize_event_id(output)
}

fn normalize_event_id(output: &[u8]) -> Vec<u8> {
    let mut normalized = Vec::with_capacity(output.len());
    let mut cursor = 0;
    while cursor < output.len() {
        if output.get(cursor..cursor + 3) == Some(b"ev_")
            && output
                .get(cursor + 3..cursor + 23)
                .is_some_and(|suffix| suffix.iter().all(u8::is_ascii_hexdigit))
        {
            normalized.extend_from_slice(b"<event-id>");
            cursor += 23;
        } else {
            normalized.push(output[cursor]);
            cursor += 1;
        }
    }
    normalized
}

fn read_journal(journal: &Path, author: &str, normalize_last: bool) -> Vec<(String, String)> {
    let mut paths = fs::read_dir(journal)
        .expect("read journal directory")
        .map(|entry| entry.expect("read journal entry").path())
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
            let contents = fs::read_to_string(&path).expect("read journal segment");
            let contents = if normalize_last && name == format!("{author}.jsonl") {
                let mut lines = contents.lines().map(str::to_owned).collect::<Vec<_>>();
                let last = lines.last_mut().expect("generated decision line");
                let mut event: serde_json::Value =
                    serde_json::from_str(last).expect("parse generated decision event");
                let object = event.as_object_mut().expect("generated event object");
                object.insert("id".into(), "<event-id>".into());
                object.insert("ts".into(), "<dynamic-clock>".into());
                object.insert("sig".into(), "<signature-of-dynamic-event>".into());
                *last = go_json(&event);
                lines.join("\n") + "\n"
            } else {
                contents
            };
            (name, contents)
        })
        .collect()
}

fn go_json(value: &serde_json::Value) -> String {
    serde_json::to_string(value)
        .expect("serialize Go-comparable event")
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
            "symroom-decide-contract-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create isolated test directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated test directory");
    }
}
