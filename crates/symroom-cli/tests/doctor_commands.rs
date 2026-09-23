#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};
use symroom_core::{event::Event, identity};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    identity_member: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct IdentityFile {
    name: String,
    content: String,
    mode: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    room: String,
    #[serde(default)]
    config: String,
    #[serde(default)]
    default_env: String,
    #[serde(default)]
    identity_key: bool,
    #[serde(default)]
    identity_files: Vec<IdentityFile>,
    #[serde(default)]
    tools: bool,
    #[serde(default)]
    index: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    tool_calls: String,
    room_files: Vec<String>,
}

#[test]
fn doctor_cli_matches_go_process_output_and_read_only_side_effects() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/doctor-cli.json")).expect("Go oracle fixture"),
    )
    .expect("parse doctor fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle_revision,
        "7d9d60bab2742e10f238ead245f967cce1adc4ea"
    );
    assert_eq!(fixture.source_hashes.len(), 5);
    for (path, expected) in &fixture.source_hashes {
        let source = fs::read(root.join(path)).expect("read Go oracle source");
        assert_eq!(
            hex::encode(Sha256::digest(source)),
            *expected,
            "Go source: {path}"
        );
    }

    let seed = hex::decode(&fixture.identity_key).expect("identity seed");
    let owner = identity::identity_from_private_key("oracle", &seed).expect("oracle identity");
    assert_eq!(owner.member_id, fixture.identity_member);
    let scratch = TempDir::new();
    for case in &fixture.cases {
        let work = scratch.path.join(&case.name);
        let home = work.join("home");
        let data_home = work.join("data");
        let temp_dir = work.join("tmp");
        let tools_dir = work.join("tools");
        for path in [&home, &data_home, &temp_dir, &tools_dir] {
            fs::create_dir_all(path).expect("create isolated home and tool paths");
        }
        if !case.config.is_empty() {
            let config = home.join(".config/symroom/config.toml");
            fs::create_dir_all(config.parent().unwrap()).expect("create config directory");
            fs::write(config, &case.config).expect("write fixture config");
        }
        let identities = data_home.join("symroom/identities");
        for file in &case.identity_files {
            fs::create_dir_all(&identities).expect("create identity store");
            let path = identities.join(&file.name);
            fs::write(&path, &file.content).expect("write Go identity fixture");
            set_mode(&path, &file.mode);
        }
        let room = work.join("room");
        if case.room == "valid" {
            make_valid_room(&room, &owner, &case.index);
        } else {
            fs::create_dir_all(&room).expect("create empty room");
        }
        if case.tools {
            install_tools(&tools_dir, &fixture.identity_key);
        }
        let before = room_snapshot(&room);
        let identities_before = room_snapshot(&data_home);
        let calls_path = work.join("tool-calls");
        let output = Command::new(env!("CARGO_BIN_EXE_symroom"))
            .args(&case.args)
            .current_dir(&work)
            .env_clear()
            .env("HOME", &home)
            .env("USERPROFILE", &home)
            .env("XDG_DATA_HOME", &data_home)
            .env("XDG_CONFIG_HOME", work.join("xdg-config"))
            .env("TMPDIR", &temp_dir)
            .env("PATH", &tools_dir)
            .env("TZ", "UTC")
            .env("LC_ALL", "C")
            .env("LANG", "C")
            .env("SYMROOM_ROOM_DIR", &room)
            .env("DOCTOR_TOOL_LOG", &calls_path)
            .env("SYMROOM_DEFAULT_IDENTITY", &case.default_env)
            .env(
                "SYMROOM_IDENTITY_KEY",
                if case.identity_key {
                    &fixture.identity_key
                } else {
                    ""
                },
            )
            .output()
            .expect("run Rust doctor process");

        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "{} exit",
            case.name
        );
        assert_eq!(
            normalize(&output.stdout, &home, &data_home, &tools_dir),
            case.stdout.as_bytes(),
            "{} stdout",
            case.name
        );
        assert_eq!(
            normalize(&output.stderr, &home, &data_home, &tools_dir),
            case.stderr.as_bytes(),
            "{} stderr",
            case.name
        );
        assert_eq!(
            read_or_empty(&calls_path),
            case.tool_calls.as_bytes(),
            "{} tool calls",
            case.name
        );
        let after = room_snapshot(&room);
        assert_eq!(
            after.keys().cloned().collect::<Vec<_>>(),
            case.room_files,
            "{} room file list",
            case.name
        );
        assert_eq!(after, before, "doctor modified room state: {}", case.name);
        assert_eq!(
            room_snapshot(&data_home),
            identities_before,
            "doctor modified identity state: {}",
            case.name
        );
        for file in &case.identity_files {
            let path = identities.join(&file.name);
            assert_eq!(
                fs::read_to_string(&path).unwrap(),
                file.content,
                "identity state {}",
                case.name
            );
            assert_eq!(mode_string(&path), file.mode, "identity mode {}", case.name);
        }
    }
}

fn make_valid_room(room: &Path, owner: &identity::Identity, index: &str) {
    fs::create_dir_all(room.join(".symroom")).expect("create local state");
    fs::create_dir_all(room.join("journal")).expect("create journal");
    fs::write(room.join("room.toml"), "id = \"rm_doctor_fixture\"\n").expect("write room manifest");
    let body = format!(
        "{{\"name\":\"Doctor Room\",\"public_key\":\"{}\"}}",
        hex::encode(&owner.public_key)
    );
    let mut event = Event {
        v: symroom_core::event::CURRENT_VERSION,
        id: "ev_doctor_fixture".to_owned(),
        room: "rm_doctor_fixture".to_owned(),
        author: owner.member_id.clone(),
        seq: 1,
        prev: format!("sha256:{}", "0".repeat(64)),
        lamport: 1,
        ts: "2026-01-02T03:04:05.006Z".to_owned(),
        kind: "room.created".to_owned(),
        body: RawValue::from_string(body).expect("event body JSON"),
        sig: None,
    };
    event.sign(owner).expect("sign Go-compatible event");
    let journal_path = room.join(format!("journal/{}.jsonl", owner.member_id));
    let event_bytes = event.marshal_json_line().expect("serialize event");
    if index == "stale" {
        let path = room.join(".symroom/index.sqlite");
        fs::write(&path, b"derived index fixture").expect("write derived index");
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    fs::write(&journal_path, event_bytes).expect("write signed event");
    if index == "current" {
        let path = room.join(".symroom/index.sqlite");
        fs::write(&path, b"derived index fixture").expect("write derived index");
    }
}

fn install_tools(dir: &Path, key: &str) {
    for name in ["symdesk", "symbrain", "symvault"] {
        let script = format!(
            "#!/bin/sh\ncase \"$1\" in\n  get) printf '%s %s\\n' '{name}' \"$*\" >> \"$DOCTOR_TOOL_LOG\"; printf '%s\\n' '{key}' ;;\n  version) printf '%s %s\\n' '{name}' \"$*\" >> \"$DOCTOR_TOOL_LOG\"; printf '{{\\\"version\\\":\\\"{name}-1.2.3\\\"}}\\n' ;;\nesac\n"
        );
        let path = dir.join(name);
        fs::write(&path, script).expect("write integration stub");
        set_mode(&path, "0755");
    }
}

fn room_snapshot(room: &Path) -> BTreeMap<String, (Vec<u8>, String)> {
    fn visit(root: &Path, base: &Path, files: &mut BTreeMap<String, (Vec<u8>, String)>) {
        for entry in fs::read_dir(root).expect("read room directory") {
            let path = entry.expect("room entry").path();
            if path.is_dir() {
                visit(&path, base, files);
            } else {
                let name = path
                    .strip_prefix(base)
                    .unwrap()
                    .to_string_lossy()
                    .replace('\\', "/");
                files.insert(
                    name,
                    (fs::read(&path).expect("read room file"), mode_string(&path)),
                );
            }
        }
    }
    let mut files = BTreeMap::new();
    visit(room, room, &mut files);
    files
}

fn normalize(bytes: &[u8], home: &Path, data: &Path, tools: &Path) -> Vec<u8> {
    let mut text = String::from_utf8_lossy(bytes).into_owned();
    for (path, token) in [(home, "$HOME"), (data, "$DATA"), (tools, "$TOOLS")] {
        text = text.replace(&path.to_string_lossy().to_string(), token);
    }
    text.into_bytes()
}

fn read_or_empty(path: &Path) -> Vec<u8> {
    fs::read(path).unwrap_or_default()
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: &str) {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(
        path,
        fs::Permissions::from_mode(u32::from_str_radix(mode, 8).unwrap()),
    )
    .expect("set fixture file mode");
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: &str) {}

fn mode_string(path: &Path) -> String {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        format!(
            "{:04o}",
            fs::metadata(path).unwrap().permissions().mode() & 0o777
        )
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        "0000".to_owned()
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
            std::env::temp_dir().join(format!("symroom-doctor-cli-{}-{nonce}", std::process::id()));
        fs::create_dir(&path).expect("create temporary directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove temporary directory");
    }
}
