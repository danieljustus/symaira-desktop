#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::Digest;
use symroom_core::{event::Event, identity};

const ORACLE_REVISION: &str = "32a4f9739fedb8aadbe282f59f5e7d4d60956b92";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    cases: Vec<Case>,
}

#[derive(Clone, Deserialize)]
struct RoomFile {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    files: Vec<RoomFile>,
    #[serde(default)]
    initial_journal: Vec<RoomFile>,
    #[serde(default)]
    dynamic_event: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<RoomFile>,
}

#[test]
fn artifact_cli_matches_go_process_journal_and_filesystem_contract() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let data = fs::read(root.join("testdata/port/room/artifact-cli.json"))
        .expect("read Go-generated artifact fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse artifact fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 3);
    for source in [
        "cmd/symroom/main.go",
        "cmd/symroom/cmd_artifact.go",
        "internal/room/artifact/artifact.go",
    ] {
        let bytes =
            fs::read(root.join(source)).unwrap_or_else(|error| panic!("read {source}: {error}"));
        assert_eq!(
            fixture.source_hashes.get(source),
            Some(&hex::encode(sha2::Sha256::digest(bytes))),
            "Go oracle source hash {source}"
        );
    }
    assert!(fixture.cases.iter().any(|case| case.dynamic_event));
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "link-outside-root")
    );
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "list-modified")
    );
    assert!(fixture.cases.iter().any(|case| case.name == "list-missing"));

    let temp = TempDir::new();
    for case in &fixture.cases {
        let isolated = temp.path.join(&case.name);
        let room = isolated.join("room");
        fs::create_dir_all(&room).expect("create fixture room");
        for file in &case.files {
            let path = room.join(&file.name);
            if let Some(parent) = path.parent() {
                fs::create_dir_all(parent).expect("create artifact parent");
            }
            fs::write(path, file.content.as_bytes()).expect("write artifact file");
        }
        if !case.initial_journal.is_empty() {
            let journal = room.join("journal");
            fs::create_dir_all(&journal).expect("create initial journal");
            for file in &case.initial_journal {
                fs::write(journal.join(&file.name), file.content.as_bytes())
                    .expect("write initial journal segment");
            }
        }
        let home = isolated.join("home");
        let tmp = isolated.join("tmp");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&tmp).expect("create isolated TMPDIR");
        let output = run_symroom(&case.args, &room, &home, &tmp, &fixture.identity_key);
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
        if case.dynamic_event {
            let signer = identity::identity_from_private_key(
                "owner",
                &hex::decode(&fixture.identity_key).expect("decode oracle identity key"),
            )
            .expect("derive oracle identity");
            let journal = fs::read_dir(room.join("journal")).expect("read mutation journal");
            let segment = journal
                .map(|entry| entry.expect("read journal entry").path())
                .find(|path| {
                    path.extension()
                        .is_some_and(|extension| extension == "jsonl")
                })
                .expect("journal segment");
            let line = fs::read_to_string(segment)
                .expect("read mutation journal")
                .lines()
                .last()
                .expect("appended event")
                .to_owned();
            let event = Event::unmarshal_json_line(line.as_bytes()).expect("parse appended event");
            event
                .verify_signature(&signer.public_key)
                .expect("Rust CLI signs mutation event");
        }
        assert_eq!(
            read_room_files(&room),
            expected_files(&case.final_files),
            "filesystem effects case {}",
            case.name
        );
    }
}

fn run_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    tmp: &Path,
    identity_key: &str,
) -> Output {
    Command::new(env!("CARGO_BIN_EXE_symroom"))
        .args(args)
        .current_dir(room)
        .env_clear()
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("TMPDIR", tmp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", ".")
        .env("SYMROOM_IDENTITY_KEY", identity_key)
        .output()
        .expect("run Rust symroom artifact CLI")
}

fn normalize_stdout(output: &[u8], dynamic_event: bool) -> Vec<u8> {
    if !dynamic_event {
        return output.to_vec();
    }
    let mut normalized = output.to_vec();
    let start = normalized
        .windows(3)
        .position(|window| window == b"ev_")
        .expect("generated event id");
    let end = start + 19;
    assert!(normalized[start + 3..end].iter().all(u8::is_ascii_hexdigit));
    normalized.splice(start..end, b"<event-id>".iter().copied());
    normalized
}

fn expected_files(files: &[RoomFile]) -> BTreeMap<String, String> {
    files
        .iter()
        .map(|file| (file.name.clone(), file.content.clone()))
        .collect()
}

fn read_room_files(room: &Path) -> BTreeMap<String, String> {
    let mut files = BTreeMap::new();
    let mut pending = vec![room.to_path_buf()];
    while let Some(directory) = pending.pop() {
        for entry in fs::read_dir(directory).expect("read room directory") {
            let path = entry.expect("read room entry").path();
            if path.is_dir() {
                pending.push(path);
                continue;
            }
            let relative = path
                .strip_prefix(room)
                .expect("room child path")
                .to_string_lossy()
                .replace('\\', "/");
            let mut content = fs::read_to_string(&path).expect("read room file");
            if relative.ends_with(".jsonl") {
                content = normalize_journal(&content);
            }
            files.insert(relative, content);
        }
    }
    files
}

fn normalize_journal(content: &str) -> String {
    content
        .lines()
        .map(|line| {
            let mut event: serde_json::Value =
                serde_json::from_str(line).expect("parse journal event");
            let object = event.as_object_mut().expect("journal event object");
            object.insert("ts".into(), "<dynamic-clock>".into());
            object.insert("sig".into(), "<signature>".into());
            go_json(&event)
        })
        .collect::<Vec<_>>()
        .join("\n")
        + "\n"
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
            .expect("clock after epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symroom-artifact-cli-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir_all(&path).expect("create temporary test directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
