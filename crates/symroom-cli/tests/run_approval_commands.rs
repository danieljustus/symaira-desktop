#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
};

use serde::Deserialize;
use serde_json::Value;
use symroom_core::{event::Event, identity};

const ORACLE_REVISION: &str = "558cac10528b2a03e344640190327a662d5a60e8";
const NORMALIZATION: &str = "dynamic appended event ts and sig; approval ID, event ID, and expires_at for run.approved; no other event fields normalized";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    normalization: String,
    source_hashes: BTreeMap<String, String>,
    identity_keys: BTreeMap<String, String>,
    initial_journal: Vec<RoomFile>,
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
    actor: String,
    #[serde(default)]
    global_config: String,
    #[serde(default)]
    default_identity_env: String,
    #[serde(default)]
    dynamic_approval: bool,
    #[serde(default)]
    dynamic_event: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_journal: Vec<RoomFile>,
}

#[test]
fn run_approval_and_denial_match_go_process_and_signed_journal() {
    let repo = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let data = fs::read(repo.join("testdata/port/room/run-approval-cli.json"))
        .expect("read Go-generated approval CLI fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse approval fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.normalization, NORMALIZATION);
    assert_eq!(fixture.source_hashes.len(), 9);
    assert_eq!(fixture.identity_keys.len(), 6);
    assert!(fixture.cases.iter().any(|case| case.dynamic_approval));
    assert!(fixture.cases.iter().any(|case| case.dynamic_event));

    for case in &fixture.cases {
        let base = TempDir::new(&case.name);
        let room = base.path.join("room");
        let journal = room.join("journal");
        let home = base.path.join("home");
        let data_home = base.path.join("data");
        let tmp = base.path.join("tmp");
        for directory in [&journal, &home, &data_home, &tmp] {
            fs::create_dir_all(directory).expect("create isolated fixture directory");
        }
        for file in &fixture.initial_journal {
            fs::write(journal.join(&file.name), file.content.as_bytes())
                .expect("write Go-signed initial journal");
        }
        if !case.global_config.is_empty() {
            let config = home.join(".config/symroom");
            fs::create_dir_all(&config).expect("create isolated global config");
            fs::write(config.join("config.toml"), case.global_config.as_bytes())
                .expect("write isolated config");
        }

        let key = fixture
            .identity_keys
            .get(&case.actor)
            .expect("fixture actor key");
        let output = run_symroom(
            &case.args,
            &room,
            &home,
            &data_home,
            &tmp,
            key,
            &case.default_identity_env,
        );
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit {}",
            case.name
        );
        let stdout = if case.dynamic_event {
            "<dynamic-event-id>\n".as_bytes().to_vec()
        } else {
            output.stdout.clone()
        };
        assert_eq!(stdout, case.stdout.as_bytes(), "stdout {}", case.name);
        assert_eq!(
            output.stderr,
            case.stderr.as_bytes(),
            "stderr {}",
            case.name
        );

        let actual = read_journal_files(&journal, case.dynamic_event, case.dynamic_approval, key);
        let expected = expected_journal_files(&case.final_journal);
        for (name, expected_content) in &expected {
            let actual_content = actual.get(name).expect("journal file exists");
            assert!(
                actual_content == expected_content,
                "journal {} file {name}: actual last line {:?}, expected last line {:?}",
                case.name,
                actual_content.lines().last(),
                expected_content.lines().last(),
            );
        }
        assert_eq!(
            actual.len(),
            expected.len(),
            "journal file count {}",
            case.name
        );
    }
}

fn run_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    temp: &Path,
    identity_key: &str,
    default_identity: &str,
) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .current_dir(room)
        .env_clear()
        .env("HOME", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", temp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", room)
        .env("SYMROOM_IDENTITY_KEY", identity_key)
        .env("SYMROOM_DEFAULT_IDENTITY", default_identity);
    command.output().expect("run Rust symroom process")
}

fn expected_journal_files(files: &[RoomFile]) -> BTreeMap<String, String> {
    files
        .iter()
        .map(|file| (file.name.clone(), file.content.clone()))
        .collect()
}

fn read_journal_files(
    journal: &Path,
    dynamic_event: bool,
    dynamic_approval: bool,
    identity_key: &str,
) -> BTreeMap<String, String> {
    let signer = identity::identity_from_private_key(
        "fixture",
        &hex::decode(identity_key).expect("hex key"),
    )
    .expect("fixture identity");
    let dynamic_name = format!("{}.jsonl", signer.member_id);
    let mut files = BTreeMap::new();
    let mut pending = vec![journal.to_path_buf()];
    while let Some(directory) = pending.pop() {
        for entry in fs::read_dir(directory).expect("read journal directory") {
            let path = entry.expect("journal entry").path();
            if path.is_dir() {
                pending.push(path);
                continue;
            }
            let name = path
                .file_name()
                .expect("journal file name")
                .to_string_lossy()
                .into_owned();
            let mut content = fs::read_to_string(&path).expect("read journal file");
            if dynamic_event && name == dynamic_name {
                verify_dynamic_signature(&content, identity_key);
                content = normalize_dynamic_event(&content, dynamic_approval);
            }
            files.insert(name, content);
        }
    }
    files
}

fn normalize_dynamic_event(content: &str, approval: bool) -> String {
    let mut lines = content.split_terminator('\n').collect::<Vec<_>>();
    let Some(suffix) = lines.pop() else {
        return content.to_owned();
    };
    let original_body = Event::unmarshal_json_line(suffix.as_bytes())
        .expect("parse appended event")
        .body
        .get()
        .to_owned();
    let mut event: Value = serde_json::from_str(suffix).expect("parse appended event");
    let object = event.as_object_mut().expect("event object");
    object.insert("ts".into(), "<dynamic-clock>".into());
    object.insert("sig".into(), "<signature-of-dynamic-clock>".into());
    if approval && object.get("kind").and_then(Value::as_str) == Some("run.approved") {
        object.insert("id".into(), "<dynamic-event-id>".into());
        if let Some(body) = object.get_mut("body").and_then(Value::as_object_mut) {
            body.insert("approval_id".into(), "<dynamic-approval-id>".into());
            body.insert("expires_at".into(), "<dynamic-expiry>".into());
        }
    }
    let mut normalized = sorted_json(&event);
    if !approval {
        let sorted_body = sorted_json(&event["body"]);
        normalized = normalized.replacen(&sorted_body, &original_body, 1);
    }
    let normalized = normalized.replace('<', "\\u003c").replace('>', "\\u003e");
    lines.push(&normalized);
    format!("{}\n", lines.join("\n"))
}

fn sorted_json(value: &Value) -> String {
    match value {
        Value::Object(object) => {
            let sorted: BTreeMap<_, _> = object.iter().collect();
            format!(
                "{{{}}}",
                sorted
                    .into_iter()
                    .map(|(key, value)| format!(
                        "{}:{}",
                        serde_json::to_string(key).unwrap(),
                        sorted_json(value)
                    ))
                    .collect::<Vec<_>>()
                    .join(",")
            )
        }
        Value::Array(values) => format!(
            "[{}]",
            values.iter().map(sorted_json).collect::<Vec<_>>().join(",")
        ),
        _ => serde_json::to_string(value).expect("serialize JSON value"),
    }
}

fn verify_dynamic_signature(content: &str, key: &str) {
    let identity =
        identity::identity_from_private_key("fixture", &hex::decode(key).expect("hex key"))
            .expect("fixture identity");
    let line = content.lines().last().expect("dynamic event line");
    let event = Event::unmarshal_json_line(line.as_bytes()).expect("parse signed dynamic event");
    event
        .verify_signature(&identity.public_key)
        .expect("verify signed dynamic event");
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new(label: &str) -> Self {
        let path = std::env::temp_dir().join(format!(
            "symroom-run-approval-cli-{label}-{}",
            std::process::id()
        ));
        if path.exists() {
            fs::remove_dir_all(&path).expect("remove prior isolated test directory");
        }
        fs::create_dir(&path).expect("create isolated test directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated test directory");
    }
}
