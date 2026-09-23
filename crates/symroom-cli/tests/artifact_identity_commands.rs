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

const ORACLE_REVISION: &str = "e4773d8ebbabbc7ae66a6bae1ae2548cc26545c9";

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
    global_config: String,
    #[serde(default)]
    project_config: String,
    #[serde(default)]
    default_identity_env: String,
    #[serde(default)]
    symdesk_mode: String,
    #[serde(default)]
    symdesk_args: Vec<String>,
    #[serde(default)]
    dynamic_event: bool,
    #[serde(default)]
    config_error: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<RoomFile>,
}

#[test]
fn artifact_identity_and_symdesk_inspect_match_go_process_contract() {
    if cfg!(windows) {
        return;
    }
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let bytes = fs::read(root.join("testdata/port/room/artifact-identity-cli.json"))
        .expect("read Go-generated artifact identity fixture");
    let fixture: Fixture = serde_json::from_slice(&bytes).expect("parse fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 5);
    for source in [
        "cmd/symroom/main.go",
        "cmd/symroom/cmd_artifact.go",
        "internal/room/artifact/artifact.go",
        "internal/room/desk/desk.go",
        "internal/room/config/config.go",
    ] {
        let source_bytes =
            fs::read(root.join(source)).unwrap_or_else(|error| panic!("read {source}: {error}"));
        assert_eq!(
            fixture.source_hashes.get(source),
            Some(&hex::encode(sha2::Sha256::digest(source_bytes))),
            "Go source hash {source}"
        );
    }
    for name in [
        "link-default-global",
        "link-default-project-over-global",
        "link-default-env-over-global",
        "unlink-default-project",
        "link-default-missing",
        "link-default-invalid-config",
        "link-inspect-success",
        "link-inspect-nonzero-fallback",
        "link-inspect-invalid-json-fallback",
        "link-inspect-timeout-fallback",
        "link-inspect-missing-fallback",
    ] {
        assert!(
            fixture.cases.iter().any(|case| case.name == name),
            "fixture case {name}"
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
        let path = isolated.join("path");
        for directory in [&room, &journal, &home, &data_home, &tmp, &path] {
            fs::create_dir_all(directory).expect("create isolated fixture directory");
        }
        for file in &case.files {
            fs::write(room.join(&file.name), file.content.as_bytes()).expect("write fixture file");
        }
        for file in &case.initial_journal {
            fs::write(journal.join(&file.name), file.content.as_bytes())
                .expect("write initial journal");
        }
        if !case.global_config.is_empty() {
            let config_dir = home.join(".config/symroom");
            fs::create_dir_all(&config_dir).expect("create isolated global config directory");
            fs::write(
                config_dir.join("config.toml"),
                case.global_config.as_bytes(),
            )
            .expect("write isolated global config");
        }
        if !case.project_config.is_empty() {
            fs::write(room.join(".symroom.toml"), case.project_config.as_bytes())
                .expect("write isolated project config");
        }
        if !case.symdesk_mode.is_empty() {
            write_fake_symdesk(&path, &case.symdesk_mode);
        }

        let output = run_symroom(
            case,
            &room,
            &home,
            &data_home,
            &tmp,
            &path,
            &fixture.identity_key,
        );
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code {}",
            case.name
        );
        let stdout = normalize_stdout(&output.stdout, case.dynamic_event);
        assert_eq!(stdout, case.stdout.as_bytes(), "stdout {}", case.name);
        let stderr = normalize_stderr(&output.stderr, &home, case.config_error);
        assert_eq!(stderr, case.stderr.as_bytes(), "stderr {}", case.name);
        assert_eq!(
            read_room_files(&room),
            expected_files(&case.final_files),
            "filesystem effects {}",
            case.name
        );
        if !case.symdesk_mode.is_empty() {
            let args = fs::read_to_string(isolated.join("symdesk-args.txt"))
                .expect("fake symdesk recorded inspect arguments");
            assert_eq!(
                args.lines().map(str::to_owned).collect::<Vec<_>>(),
                case.symdesk_args
            );
        }
        if case.dynamic_event && case.exit_code == 0 {
            let signer = identity::identity_from_private_key(
                "owner",
                &hex::decode(&fixture.identity_key).expect("decode fixture identity"),
            )
            .expect("derive fixture identity");
            let segment = fs::read_dir(&journal)
                .expect("read journal")
                .map(|entry| entry.expect("read journal entry").path())
                .find(|path| {
                    path.extension()
                        .is_some_and(|extension| extension == "jsonl")
                })
                .expect("journal segment");
            let line = fs::read_to_string(segment)
                .expect("read journal segment")
                .lines()
                .last()
                .expect("signed appended event")
                .to_owned();
            let event = Event::unmarshal_json_line(line.as_bytes()).expect("parse event");
            event
                .verify_signature(&signer.public_key)
                .expect("event signature validates");
            if case.name == "link-inspect-success" {
                assert_eq!(event.kind, "artifact.linked");
                assert!(event.body.get().contains("doc-fixture-1"));
            }
        }
    }
}

fn run_symroom(
    case: &Case,
    room: &Path,
    home: &Path,
    data_home: &Path,
    tmp: &Path,
    path: &Path,
    identity_key: &str,
) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(&case.args)
        .current_dir(room)
        .env_clear()
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", tmp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("PATH", path)
        .env("SYMROOM_ROOM_DIR", ".")
        .env("SYMROOM_IDENTITY_KEY", identity_key)
        .env(
            "SYMDESK_ARGS_FILE",
            room.parent()
                .expect("fixture case")
                .join("symdesk-args.txt"),
        );
    if !case.default_identity_env.is_empty() {
        command.env("SYMROOM_DEFAULT_IDENTITY", &case.default_identity_env);
    }
    command.output().expect("run Rust symroom artifact command")
}

fn write_fake_symdesk(path: &Path, mode: &str) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let helper = path.join("symdesk");
        let response = match mode {
            "success" => {
                "printf '{\"document_id\":\"doc-fixture-1\",\"vault_name\":\"fixture\",\"valid\":true}\\n'\n"
            }
            "exit" => "printf '{\"document_id\":\"ignored\"}\\n'\nexit 7\n",
            "invalid" => "printf 'not-json\\n'\n",
            "hang" => "exec /bin/sleep 60\n",
            _ => panic!("unexpected symdesk mode {mode}"),
        };
        let script =
            format!("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SYMDESK_ARGS_FILE\"\n{response}");
        fs::write(&helper, script).expect("write fake symdesk");
        fs::set_permissions(helper, fs::Permissions::from_mode(0o755))
            .expect("make fake symdesk executable");
    }
}

fn normalize_stdout(output: &[u8], dynamic_event: bool) -> Vec<u8> {
    if !dynamic_event {
        return output.to_vec();
    }
    let mut normalized = output.to_vec();
    let start = normalized
        .windows(3)
        .position(|window| window == b"ev_")
        .expect("event id in successful mutation output");
    let end = start + 19;
    assert!(normalized[start + 3..end].iter().all(u8::is_ascii_hexdigit));
    normalized.splice(start..end, b"<event-id>".iter().copied());
    normalized
}

fn normalize_stderr(output: &[u8], home: &Path, config_error: bool) -> Vec<u8> {
    if !config_error {
        return output.to_vec();
    }
    let config_path = home.join(".config/symroom/config.toml");
    let config_path = config_path.to_string_lossy();
    String::from_utf8_lossy(output)
        .replace(config_path.as_ref(), "<config-path>")
        .into_bytes()
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
            let mut event: serde_json::Value = serde_json::from_str(line).expect("parse event");
            let object = event.as_object_mut().expect("event object");
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
            "symroom-artifact-identity-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create temporary fixture directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated fixture directory");
    }
}
