#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::{event::Event, identity};

const ORACLE_REVISION: &str = "8e11384470ea86b15d1d60f21442a3e0c7287d53";
const ROOM_ID: &str = "rm_0123456789abcdef";
const EVENT_ID: &str = "ev_0123456789abcdef0123";
const CREATED: &str = "2026-01-02T03:04:05.006Z";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    identity_member: String,
    identity_file_name: String,
    identity_file_content: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct File {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    identity: String,
    #[serde(default)]
    default_identity_env: bool,
    #[serde(default)]
    global_config: String,
    #[serde(default)]
    room_dir_env: bool,
    #[serde(default)]
    nonempty: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    files: Vec<File>,
    modes: BTreeMap<String, String>,
    #[serde(default)]
    preserved: String,
}

#[test]
fn init_cli_matches_go_flags_files_modes_and_identity_sources() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture_path = root.join("testdata/port/room/init-cli.json");
    let data = fs::read(fixture_path).expect("read Go-generated init CLI fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse init CLI fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 4);
    for (path, expected) in &fixture.source_hashes {
        let digest = Sha256::digest(fs::read(root.join(path)).expect("read Go oracle source"));
        assert_eq!(hex::encode(digest), *expected, "Go source hash: {path}");
    }

    let temp = TempDir::new();
    let seed = hex::decode(&fixture.identity_key).expect("fixture identity seed");
    let owner = identity::identity_from_private_key("oracle", &seed).expect("Go identity");
    assert_eq!(owner.member_id, fixture.identity_member);
    assert!(fixture.cases.iter().any(|case| case.identity == "env"));
    assert!(fixture.cases.iter().any(|case| case.identity == "file"));
    assert!(fixture.cases.iter().any(|case| case.default_identity_env));
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| !case.global_config.is_empty())
    );
    assert!(fixture.cases.iter().any(|case| case.nonempty));
    for case in &fixture.cases {
        let work = temp.path.join(&case.name);
        let home = work.join("home");
        let data_home = work.join("data");
        let config_home = work.join("config");
        let temp_dir = work.join("tmp");
        for dir in [&home, &data_home, &config_home, &temp_dir] {
            fs::create_dir_all(dir).expect("create isolated CLI environment");
        }
        if !case.global_config.is_empty() {
            let path = home.join(".config/symroom/config.toml");
            fs::create_dir_all(path.parent().unwrap()).expect("create global config directory");
            fs::write(path, &case.global_config).expect("write Go config input");
        }
        let identities = data_home.join("symroom/identities");
        if case.identity == "file" {
            fs::create_dir_all(&identities).expect("create identity store");
            fs::write(
                identities.join(&fixture.identity_file_name),
                fixture.identity_file_content.as_bytes(),
            )
            .expect("write Go identity file");
        }
        let room = work.join("room");
        if case.nonempty {
            fs::create_dir_all(&room).expect("create nonempty target");
            fs::write(room.join("keep"), case.preserved.as_bytes()).expect("seed preserved file");
        }

        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        command
            .args(&case.args)
            .current_dir(&work)
            .env_clear()
            .env("HOME", &home)
            .env("USERPROFILE", &home)
            .env("XDG_DATA_HOME", &data_home)
            .env("XDG_CONFIG_HOME", &config_home)
            .env("TMPDIR", &temp_dir)
            .env("PATH", &temp_dir)
            .env("TZ", "UTC")
            .env("LC_ALL", "C")
            .env("LANG", "C");
        if case.identity == "env" {
            command.env("SYMROOM_IDENTITY_KEY", &fixture.identity_key);
        }
        if case.default_identity_env {
            command.env("SYMROOM_DEFAULT_IDENTITY", "oracle");
        }
        if case.room_dir_env {
            command.env("SYMROOM_ROOM_DIR", "room");
        }
        let output = command.output().expect("run Rust symroom init");
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code {}",
            case.name
        );
        assert_eq!(
            normalize_stdout(&output.stdout),
            case.stdout.as_bytes(),
            "stdout {}",
            case.name
        );
        assert_eq!(
            output.stderr,
            case.stderr.as_bytes(),
            "stderr {}",
            case.name
        );

        if !case.files.is_empty() {
            normalize_rust_room(&room, &owner);
            let actual = read_files(&room);
            let expected = case
                .files
                .iter()
                .map(|file| (file.name.clone(), file.content.clone()))
                .collect::<Vec<_>>();
            assert_eq!(actual, expected, "Go/Rust room bytes {}", case.name);
            for (name, expected) in &case.modes {
                assert_eq!(
                    mode(&room.join(name)),
                    expected.as_str(),
                    "Go/Rust mode {} {name}",
                    case.name
                );
            }
            let segment = fs::read(room.join(format!("journal/{}.jsonl", owner.member_id)))
                .expect("created journal segment");
            let event = Event::unmarshal_json_line(&segment).expect("created event parses");
            event
                .verify_signature(&owner.public_key)
                .expect("created event signature verifies");
        }
        if case.nonempty {
            assert_eq!(
                fs::read(room.join("keep")).unwrap(),
                case.preserved.as_bytes()
            );
            assert_eq!(
                fs::read_dir(&room).unwrap().count(),
                1,
                "source state unchanged"
            );
        }
    }
}

fn normalize_stdout(output: &[u8]) -> Vec<u8> {
    let text = String::from_utf8_lossy(output);
    if let Some(rest) = text.strip_prefix("Initialized room ")
        && let Some((_, tail)) = rest.split_once(" in ")
    {
        return format!("Initialized room <room-id> in {tail}").into_bytes();
    }
    output.to_vec()
}

fn normalize_rust_room(room: &Path, owner: &identity::Identity) {
    let journal = room.join(format!("journal/{}.jsonl", owner.member_id));
    let bytes = fs::read(&journal).expect("read created journal");
    let mut event = Event::unmarshal_json_line(&bytes).expect("parse created event");
    event
        .verify_signature(&owner.public_key)
        .expect("original created event signature verifies");
    event.id = EVENT_ID.to_owned();
    event.room = ROOM_ID.to_owned();
    event.ts = CREATED.to_owned();
    event.sign(owner).expect("re-sign normalized event");
    fs::write(&journal, event.marshal_json_line().expect("marshal event"))
        .expect("write normalized event");

    let config = room.join("room.toml");
    let content = fs::read_to_string(&config).expect("read room config");
    let content = replace_toml_value(&content, "id", ROOM_ID);
    let content = replace_toml_value(&content, "created", CREATED);
    let content = replace_toml_value(&content, "root_event", EVENT_ID);
    fs::write(config, content).expect("write normalized room config");
}

fn replace_toml_value(content: &str, key: &str, value: &str) -> String {
    content
        .lines()
        .map(|line| {
            if line
                .split_once('=')
                .is_some_and(|(field, _)| field.trim() == key)
            {
                format!("{key} = \"{value}\"")
            } else {
                line.to_owned()
            }
        })
        .collect::<Vec<_>>()
        .join("\n")
        + "\n"
}

fn read_files(room: &Path) -> Vec<(String, String)> {
    let mut paths = fs::read_dir(room)
        .expect("read room directory")
        .map(|entry| entry.expect("room entry").path())
        .flat_map(|path| {
            if path.is_dir() {
                fs::read_dir(&path)
                    .expect("read room subdirectory")
                    .map(|entry| entry.expect("room child").path())
                    .collect::<Vec<_>>()
            } else {
                vec![path]
            }
        })
        .collect::<Vec<_>>();
    paths.sort();
    paths
        .into_iter()
        .map(|path| {
            let name = path
                .strip_prefix(room)
                .expect("room-relative output")
                .to_string_lossy()
                .replace('\\', "/");
            (name, fs::read_to_string(path).expect("read room output"))
        })
        .collect()
}

fn mode(path: &Path) -> String {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        format!(
            "{:04o}",
            fs::metadata(path).unwrap().permissions().mode() & 0o7777
        )
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        "platform".to_owned()
    }
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "symroom-init-cli-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir(&path).expect("temporary directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
