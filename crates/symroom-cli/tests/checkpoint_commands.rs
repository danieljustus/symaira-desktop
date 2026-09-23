#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Child, Command, Output, Stdio},
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::Digest;
use symroom_core::{event::Event, identity};

const ORACLE_REVISION: &str = "def48b15ff6a9392ce4de3a824a0eec530557e8a";
const NORMALIZATION: &str = "normalize dynamic timestamps/signatures, generated checkpoint/event ids, and prev hashes that depend on a generated request event";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    normalization: String,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    agent_key: String,
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
    #[serde(default = "default_actor")]
    actor: String,
    #[serde(default)]
    global_config: String,
    #[serde(default)]
    project_config: String,
    #[serde(default)]
    default_identity_env: String,
    #[serde(default)]
    initial_journal: Vec<RoomFile>,
    #[serde(default)]
    resolve_args: Vec<String>,
    #[serde(default)]
    resolver_exit_code: i32,
    #[serde(default)]
    resolver_stdout: String,
    #[serde(default)]
    resolver_stderr: String,
    #[serde(default)]
    dynamic_event: bool,
    #[serde(default)]
    dynamic_checkpoint: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<RoomFile>,
}

fn default_actor() -> String {
    "owner".to_owned()
}

#[test]
fn checkpoint_cli_matches_go_process_stream_journal_and_authorization() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let fixture_bytes = fs::read(root.join("testdata/port/room/checkpoint-cli.json"))
        .expect("read Go-generated checkpoint fixture");
    let fixture: Fixture = serde_json::from_slice(&fixture_bytes).expect("parse fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.normalization, NORMALIZATION);
    assert_eq!(fixture.source_hashes.len(), 7);
    for source in [
        "cmd/symroom/main.go",
        "cmd/symroom/cmd_checkpoint.go",
        "internal/room/run/checkpoint.go",
        "internal/room/config/config.go",
        "internal/room/identity/identity.go",
        "internal/room/journal/journal.go",
        "internal/room/members/members.go",
    ] {
        let source_bytes =
            fs::read(root.join(source)).unwrap_or_else(|error| panic!("read {source}: {error}"));
        assert_eq!(
            fixture.source_hashes.get(source),
            Some(&hex::encode(sha2::Sha256::digest(source_bytes))),
            "Go source hash {source}"
        );
    }

    let temp = TempDir::new();
    for case in &fixture.cases {
        let isolated = temp.path.join(&case.name);
        let room = isolated.join("room");
        let journal = room.join("journal");
        let home = isolated.join("home");
        let data_home = isolated.join("data");
        let tmp = isolated.join("tmp");
        for directory in [&room, &journal, &home, &data_home, &tmp] {
            fs::create_dir_all(directory).expect("create isolated fixture directory");
        }
        for file in &case.initial_journal {
            fs::write(journal.join(&file.name), file.content.as_bytes())
                .expect("write Go fixture journal bytes");
        }
        if !case.global_config.is_empty() {
            let config = home.join(".config/symroom");
            fs::create_dir_all(&config).expect("create isolated global config");
            fs::write(config.join("config.toml"), case.global_config.as_bytes())
                .expect("write global config");
        }
        if !case.project_config.is_empty() {
            fs::write(room.join(".symroom.toml"), case.project_config.as_bytes())
                .expect("write project config");
        }

        let key = if case.actor == "agent" {
            &fixture.agent_key
        } else {
            &fixture.identity_key
        };
        let output = if case.resolve_args.is_empty() {
            run_symroom(
                &case.args,
                &room,
                &home,
                &data_home,
                &tmp,
                key,
                &case.default_identity_env,
            )
        } else {
            let request = spawn_symroom(
                &case.args,
                &room,
                &home,
                &data_home,
                &tmp,
                key,
                &case.default_identity_env,
            );
            let checkpoint_id = match wait_for_request(&journal, Duration::from_secs(3)) {
                Some(id) => id,
                None => {
                    let mut request = request;
                    let _ = request.kill();
                    let _ = request.wait();
                    panic!(
                        "request process failed to append checkpoint for {}",
                        case.name
                    );
                }
            };
            let resolve_args = case
                .resolve_args
                .iter()
                .map(|argument| argument.replace("<checkpoint-id>", &checkpoint_id))
                .collect::<Vec<_>>();
            let resolver = run_symroom(
                &resolve_args,
                &room,
                &home,
                &data_home,
                &tmp,
                key,
                &case.default_identity_env,
            );
            assert_eq!(
                resolver.status.code(),
                Some(case.resolver_exit_code),
                "resolver exit code {}",
                case.name
            );
            assert_eq!(
                normalize_event_output(&resolver.stdout),
                case.resolver_stdout.as_bytes(),
                "resolver stdout {}",
                case.name
            );
            assert_eq!(
                resolver.stderr,
                case.resolver_stderr.as_bytes(),
                "resolver stderr {}",
                case.name
            );
            request
                .wait_with_output()
                .expect("wait for checkpoint request process")
        };

        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code {}",
            case.name
        );
        let stdout = if case.name.starts_with("resolve-success") {
            normalize_event_output(&output.stdout)
        } else {
            output.stdout.clone()
        };
        assert_eq!(stdout, case.stdout.as_bytes(), "stdout {}", case.name);
        let stderr = if case.dynamic_checkpoint {
            normalize_checkpoint_error(&output.stderr, &journal)
        } else {
            output.stderr.clone()
        };
        assert_eq!(stderr, case.stderr.as_bytes(), "stderr {}", case.name);
        assert_eq!(
            read_room_files(&room),
            expected_files(&case.final_files),
            "room files {}",
            case.name
        );
        if case.dynamic_event {
            let journal_content = case
                .final_files
                .iter()
                .find(|file| file.name.ends_with(".jsonl"))
                .map(|file| file.content.as_str())
                .expect("dynamic event case has a journal file");
            assert!(
                journal_content.lines().any(|line| {
                    serde_json::from_str::<serde_json::Value>(line)
                        .ok()
                        .and_then(|event| {
                            event
                                .get("kind")
                                .and_then(serde_json::Value::as_str)
                                .map(str::to_owned)
                        })
                        .is_some_and(|kind| kind.starts_with("checkpoint."))
                }),
                "dynamic event case has a checkpoint event: {}",
                case.name
            );
        }
        verify_journal_signatures(&journal, &fixture.identity_key, &fixture.agent_key);
    }
}

fn spawn_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    tmp: &Path,
    identity_key: &str,
    default_identity: &str,
) -> Child {
    let mut command = base_command(
        args,
        room,
        home,
        data_home,
        tmp,
        identity_key,
        default_identity,
    );
    command.stdout(Stdio::piped()).stderr(Stdio::piped());
    command.spawn().expect("start Rust checkpoint process")
}

fn run_symroom(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    tmp: &Path,
    identity_key: &str,
    default_identity: &str,
) -> Output {
    base_command(
        args,
        room,
        home,
        data_home,
        tmp,
        identity_key,
        default_identity,
    )
    .output()
    .expect("run Rust checkpoint process")
}

fn base_command(
    args: &[String],
    room: &Path,
    home: &Path,
    data_home: &Path,
    tmp: &Path,
    identity_key: &str,
    default_identity: &str,
) -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .current_dir(room)
        .env_clear()
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", tmp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("PATH", tmp)
        .env("SYMROOM_ROOM_DIR", room)
        .env("SYMROOM_IDENTITY_KEY", identity_key);
    if !default_identity.is_empty() {
        command.env("SYMROOM_DEFAULT_IDENTITY", default_identity);
    }
    command
}

fn wait_for_request(journal: &Path, timeout: Duration) -> Option<String> {
    let deadline = std::time::Instant::now() + timeout;
    while std::time::Instant::now() < deadline {
        if let Ok(entries) = fs::read_dir(journal) {
            for entry in entries.flatten() {
                if let Ok(content) = fs::read_to_string(entry.path()) {
                    for line in content.lines() {
                        let Ok(event) = Event::unmarshal_json_line(line.as_bytes()) else {
                            continue;
                        };
                        if event.kind == "checkpoint.requested"
                            && let Ok(body) =
                                serde_json::from_str::<serde_json::Value>(event.body.get())
                            && let Some(id) =
                                body.get("checkpoint_id").and_then(|value| value.as_str())
                        {
                            return Some(id.to_owned());
                        }
                    }
                }
            }
        }
        thread::sleep(Duration::from_millis(10));
    }
    None
}

fn normalize_event_output(output: &[u8]) -> Vec<u8> {
    let text = String::from_utf8_lossy(output);
    if let Some(start) = text.find("ev_") {
        let end = start + 19;
        if text
            .as_bytes()
            .get(start + 3..end)
            .is_some_and(|suffix| suffix.iter().all(u8::is_ascii_hexdigit))
        {
            return format!("{}<event-id>{}", &text[..start], &text[end..]).into_bytes();
        }
    }
    output.to_vec()
}

fn normalize_checkpoint_error(output: &[u8], journal: &Path) -> Vec<u8> {
    let checkpoint_id = wait_for_request(journal, Duration::from_millis(10));
    if let Some(id) = checkpoint_id {
        String::from_utf8_lossy(output)
            .replace(&id, "<checkpoint-id>")
            .into_bytes()
    } else {
        output.to_vec()
    }
}

fn verify_journal_signatures(journal: &Path, owner_key: &str, agent_key: &str) {
    let owner = identity::identity_from_private_key("owner", &hex::decode(owner_key).unwrap())
        .expect("derive owner identity");
    let agent = identity::identity_from_private_key("agent", &hex::decode(agent_key).unwrap())
        .expect("derive agent identity");
    let Ok(entries) = fs::read_dir(journal) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        let Ok(content) = fs::read_to_string(path) else {
            continue;
        };
        for line in content.lines() {
            let Ok(event) = Event::unmarshal_json_line(line.as_bytes()) else {
                continue;
            };
            let public_key = if event.author == agent.member_id {
                &agent.public_key
            } else {
                &owner.public_key
            };
            event
                .verify_signature(public_key)
                .expect("journal event signature verifies");
        }
    }
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
        for entry in fs::read_dir(directory).expect("read fixture room") {
            let path = entry.expect("read room entry").path();
            if path.is_dir() {
                pending.push(path);
                continue;
            }
            let relative = path
                .strip_prefix(room)
                .expect("room child")
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
    let mut previous_dynamic = false;
    content
        .lines()
        .enumerate()
        .map(|(index, line)| {
            let mut event: serde_json::Value = serde_json::from_str(line).expect("parse event");
            let object = event.as_object_mut().expect("event object");
            object.insert("ts".into(), "<dynamic-clock>".into());
            object.insert("sig".into(), "<signature>".into());
            if index > 0 && previous_dynamic {
                object.insert("prev".into(), "<dynamic-prev>".into());
            }
            previous_dynamic = false;
            if object
                .get("id")
                .and_then(serde_json::Value::as_str)
                .is_some_and(|id| id.starts_with("ev_") && id.len() == 19)
            {
                object.insert("id".into(), "ev_<event-id>".into());
                previous_dynamic = true;
            }
            if let Some(body) = object
                .get_mut("body")
                .and_then(serde_json::Value::as_object_mut)
                && body
                    .get("checkpoint_id")
                    .and_then(serde_json::Value::as_str)
                    .is_some_and(|id| {
                        id.starts_with("chk_") && id.len() == 20 && id != "chk_existing"
                    })
            {
                body.insert("checkpoint_id".into(), "<checkpoint-id>".into());
                previous_dynamic = true;
            }
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
            "symroom-checkpoint-cli-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create temporary fixture root");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated fixture root");
    }
}
