#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::Digest;
use symroom_core::{event::Event, identity};

const ORACLE_REVISION: &str = "138746d3ac4df97dd230ecd0fd67f762dd55f499";

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
    default_identity_env: bool,
    #[serde(default)]
    path_mode: String,
    #[serde(default)]
    files: Vec<RoomFile>,
    #[serde(default)]
    updated_files: Vec<RoomFile>,
    #[serde(default)]
    initial_journal: Vec<RoomFile>,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<RoomFile>,
    #[serde(default)]
    symdesk_args: Vec<String>,
}

#[test]
fn watch_cli_matches_go_process_stream_and_signed_journal_effects() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let data = fs::read(root.join("testdata/port/room/watch-cli.json"))
        .expect("read Go-generated watch fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse watch fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 5);
    for source in [
        "cmd/symroom/main.go",
        "cmd/symroom/cmd_watch.go",
        "internal/room/desk/watch.go",
        "internal/room/artifact/artifact.go",
        "internal/room/event/event.go",
    ] {
        let bytes =
            fs::read(root.join(source)).unwrap_or_else(|error| panic!("read {source}: {error}"));
        assert_eq!(
            fixture.source_hashes.get(source),
            Some(&hex::encode(sha2::Sha256::digest(bytes))),
            "Go oracle source hash {source}"
        );
    }
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "symdesk-not-found")
    );
    assert!(fixture.cases.iter().any(|case| case.name == "watch-cancel"));

    let temp = TempDir::new();
    let signer = identity::identity_from_private_key(
        "owner",
        &hex::decode(&fixture.identity_key).expect("decode owner key"),
    )
    .expect("derive owner identity");
    for case in &fixture.cases {
        if cfg!(windows) && case.name == "watch-cancel" {
            continue;
        }
        let isolated = temp.path.join(&case.name);
        let room = isolated.join("room");
        let journal = room.join("journal");
        let home = isolated.join("home");
        let data = isolated.join("data");
        let tmp = isolated.join("tmp");
        let path = isolated.join("path");
        for directory in [&room, &journal, &home, &data, &tmp, &path] {
            fs::create_dir_all(directory).expect("create isolated fixture directory");
        }
        for file in &case.files {
            fs::write(room.join(&file.name), file.content.as_bytes()).expect("write fixture file");
        }
        for file in &case.initial_journal {
            fs::write(journal.join(&file.name), file.content.as_bytes())
                .expect("write fixture journal");
        }
        for file in &case.updated_files {
            fs::write(room.join(&file.name), file.content.as_bytes()).expect("update fixture file");
        }
        if case.path_mode == "fake" {
            write_fake_symdesk(&path);
        }

        let output = if case.name == "watch-cancel" {
            let child = command(
                &case.args,
                &room,
                &fixture.identity_key,
                case.default_identity_env,
            )
            .spawn()
            .expect("start Rust watch CLI");
            assert!(
                wait_for_lines(
                    &journal.join(format!("{}.jsonl", signer.member_id)),
                    2,
                    Duration::from_secs(3)
                ),
                "Rust watch did not append its artifact event"
            );
            let signal = Command::new("/bin/kill")
                .args(["-TERM", &child.id().to_string()])
                .status()
                .expect("send SIGTERM to Rust watch CLI");
            assert!(signal.success(), "SIGTERM command failed");
            child
                .wait_with_output()
                .expect("wait for canceled Rust watch CLI")
        } else {
            command(
                &case.args,
                &room,
                &fixture.identity_key,
                case.default_identity_env,
            )
            .output()
            .expect("run Rust watch CLI")
        };
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code case {}",
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
        assert_eq!(
            read_room_files(&room),
            expected_files(&case.final_files),
            "room files case {}",
            case.name
        );
        if !case.symdesk_args.is_empty() {
            let got = fs::read_to_string(isolated.join("symdesk-args.txt"));
            // The fake records argv via WATCH_ARGS_FILE, set by the command helper below.
            assert_eq!(
                got.expect("read fake symdesk argv")
                    .lines()
                    .map(str::to_owned)
                    .collect::<Vec<_>>(),
                case.symdesk_args
            );
        }
        if case.name == "watch-cancel" {
            let segment = journal.join(format!("{}.jsonl", signer.member_id));
            let line = fs::read_to_string(segment)
                .expect("read watch journal")
                .lines()
                .last()
                .expect("appended event")
                .to_owned();
            let event = Event::unmarshal_json_line(line.as_bytes()).expect("parse appended event");
            assert_eq!(event.kind, "artifact.changed");
            event
                .verify_signature(&signer.public_key)
                .expect("Rust watch signs artifact change");
        }
    }
}

fn command(
    args: &[String],
    room: &Path,
    identity_key: &str,
    default_identity_env: bool,
) -> Command {
    let isolated = room.parent().expect("fixture case directory");
    let args_file = isolated.join("symdesk-args.txt");
    let event_path = "report.md";
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .current_dir(room)
        .env_clear()
        .env("HOME", isolated.join("home"))
        .env("USERPROFILE", isolated.join("home"))
        .env("XDG_DATA_HOME", isolated.join("data"))
        .env("TMPDIR", isolated.join("tmp"))
        .env("LLVM_PROFILE_FILE", isolated.join("tmp/symroom-%p.profraw"))
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("PATH", isolated.join("path"))
        .env("SYMROOM_ROOM_DIR", room)
        .env("SYMROOM_IDENTITY_KEY", identity_key)
        .env("WATCH_ARGS_FILE", args_file)
        .env("WATCH_EVENT_PATH", event_path);
    if default_identity_env {
        command.env("SYMROOM_DEFAULT_IDENTITY", "owner");
    }
    command
}

fn write_fake_symdesk(path: &Path) {
    #[cfg(not(unix))]
    let _ = path;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let helper = path.join("symdesk");
        fs::write(
            &helper,
            "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WATCH_ARGS_FILE\"\nprintf '{\"event\":\"file_changed\",\"path\":\"%s\"}\\n' \"$WATCH_EVENT_PATH\"\nexec /bin/sleep 60\n",
        )
        .expect("write fake symdesk");
        fs::set_permissions(helper, fs::Permissions::from_mode(0o755))
            .expect("make fake symdesk executable");
    }
}

fn wait_for_lines(path: &Path, count: usize, timeout: Duration) -> bool {
    let deadline = std::time::Instant::now() + timeout;
    while std::time::Instant::now() < deadline {
        if fs::read_to_string(path).is_ok_and(|content| content.lines().count() >= count) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    false
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
        .enumerate()
        .map(|(index, line)| {
            if index == 0 {
                return line.to_owned();
            }
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
        .expect("serialize event")
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
        let path =
            std::env::temp_dir().join(format!("symroom-watch-cli-{}-{nonce}", std::process::id()));
        fs::create_dir(&path).expect("create isolated test root");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated test root");
    }
}
