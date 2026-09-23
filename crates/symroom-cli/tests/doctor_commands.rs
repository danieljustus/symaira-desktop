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
            install_tools(&tools_dir, &work);
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
            .env("DOCTOR_IDENTITY_KEY", &fixture.identity_key)
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

        let expected_exit = if cfg!(windows) && !case.identity_files.is_empty() {
            1
        } else {
            case.exit_code
        };
        assert_eq!(
            output.status.code(),
            Some(expected_exit),
            "{} exit",
            case.name
        );
        assert_eq!(
            normalize(
                &output.stdout,
                &home,
                &data_home,
                &tools_dir,
                !case.identity_files.is_empty()
            ),
            case.stdout.as_bytes(),
            "{} stdout",
            case.name
        );
        assert_eq!(
            normalize(
                &output.stderr,
                &home,
                &data_home,
                &tools_dir,
                !case.identity_files.is_empty()
            ),
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
        // FAT timestamps have two-second granularity; keep the index older
        // even on filesystems with coarse modified-time resolution.
        std::thread::sleep(std::time::Duration::from_millis(2100));
    }
    fs::write(&journal_path, event_bytes).expect("write signed event");
    if index == "current" {
        let path = room.join(".symroom/index.sqlite");
        fs::write(&path, b"derived index fixture").expect("write derived index");
    }
}

fn install_tools(dir: &Path, work: &Path) {
    #[cfg(windows)]
    let helper = build_windows_tool_helper(work);
    for name in ["symdesk", "symbrain", "symvault"] {
        #[cfg(windows)]
        {
            let path = dir.join(format!("{name}.exe"));
            fs::copy(&helper, &path).expect("copy Windows integration executable");
        }
        #[cfg(not(windows))]
        {
            let script = format!(
                "#!/bin/sh\ncase \"$1\" in\n  get) printf '%s %s\\n' '{name}' \"$*\" >> \"$DOCTOR_TOOL_LOG\"; printf '%s\\n' \"$DOCTOR_IDENTITY_KEY\" ;;\n  version) printf '%s %s\\n' '{name}' \"$*\" >> \"$DOCTOR_TOOL_LOG\"; printf '{{\\\"version\\\":\\\"{name}-1.2.3\\\"}}\\n' ;;\nesac\n"
            );
            let path = dir.join(name);
            fs::write(&path, script).expect("write integration stub");
            set_mode(&path, "0755");
        }
    }
}

#[cfg(windows)]
fn build_windows_tool_helper(work: &Path) -> PathBuf {
    let source = work.join("doctor-tool-helper.go");
    let output = work.join("doctor-tool-helper.exe");
    fs::write(
        &source,
        r#"package main

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

func main() {
    name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
    if len(os.Args) < 2 { return }
    args := strings.Join(os.Args[1:], " ")
    if os.Getenv("DOCTOR_TOOL_LOG") != "" {
        f, err := os.OpenFile(os.Getenv("DOCTOR_TOOL_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
        if err == nil { _, _ = fmt.Fprintf(f, "%s %s\n", name, args); _ = f.Close() }
    }
    switch os.Args[1] {
    case "get": fmt.Println(os.Getenv("DOCTOR_IDENTITY_KEY"))
    case "version": fmt.Printf("{\"version\":\"%s-1.2.3\"}\n", name)
    }
}
"#,
    )
    .expect("write Go Windows tool helper");
    let output_result = Command::new("go")
        .args(["build", "-o"])
        .arg(&output)
        .arg(&source)
        .output()
        .expect("run Go to build Windows tool helper");
    assert!(
        output_result.status.success(),
        "Go Windows tool helper build failed: {}",
        String::from_utf8_lossy(&output_result.stderr)
    );
    output
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

fn normalize(
    bytes: &[u8],
    home: &Path,
    data: &Path,
    tools: &Path,
    has_identity_file: bool,
) -> Vec<u8> {
    let mut text = String::from_utf8_lossy(bytes).into_owned();
    for (path, token) in [(home, "$HOME"), (data, "$DATA"), (tools, "$TOOLS")] {
        text = text.replace(&path.to_string_lossy().to_string(), token);
    }
    if cfg!(windows) {
        for name in ["symdesk", "symbrain", "symvault"] {
            text = text.replace(&format!("$TOOLS\\{name}.exe"), &format!("$TOOLS/{name}"));
            text = text.replace(&format!("$TOOLS/{name}.exe"), &format!("$TOOLS/{name}"));
        }
        if text.starts_with('{') {
            text = text.replace("\\\\", "/");
        } else {
            text = text.replace('\\', "/");
        }
    }
    if has_identity_file {
        text = normalize_identity_mode_output(&text);
    }
    text.into_bytes()
}

fn normalize_identity_mode_output(text: &str) -> String {
    if text.starts_with('{') {
        let mut lines = text.lines().map(str::to_owned).collect::<Vec<_>>();
        for index in 0..lines.len() {
            if lines[index].trim() == "\"name\": \"identity_key_mode\"," && index + 3 < lines.len()
            {
                let indent = lines[index].len() - lines[index].trim_start().len();
                let prefix = " ".repeat(indent);
                lines[index + 1] = format!("{prefix}  \"status\": \"platform-mode\",");
                lines[index + 2] = format!(
                    "{prefix}  \"message\": \"identity key mode depends on the host platform\","
                );
                lines[index + 3] = format!("{prefix}  \"remediation\": \"platform-specific\"");
            }
        }
        return lines
            .join("\n")
            .replace("\"failed\": true", "\"failed\": \"platform-dependent\"")
            .replace("\"failed\": false", "\"failed\": \"platform-dependent\"");
    }
    let mut lines = text.lines().map(str::to_owned).collect::<Vec<_>>();
    for index in 0..lines.len() {
        if lines[index].contains(" identity_key_mode: ") && index + 1 < lines.len() {
            lines[index] = "[MODE] identity_key_mode: host-dependent key file mode".to_owned();
            lines[index + 1] = "  remediation: platform-specific".to_owned();
        }
    }
    lines.join("\n")
}

fn read_or_empty(path: &Path) -> Vec<u8> {
    fs::read(path).unwrap_or_default()
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: &str) {
    use std::os::unix::fs::PermissionsExt;
    let bits = match mode {
        "private" => 0o600,
        "readonly" => 0o444,
        "0755" => 0o755,
        other => u32::from_str_radix(other, 8).unwrap(),
    };
    fs::set_permissions(path, fs::Permissions::from_mode(bits)).expect("set fixture file mode");
}

#[cfg(not(unix))]
fn set_mode(path: &Path, mode: &str) {
    let mut permissions = fs::metadata(path).unwrap().permissions();
    permissions.set_readonly(mode == "readonly");
    fs::set_permissions(path, permissions).expect("set Windows fixture file mode");
}

fn mode_string(path: &Path) -> String {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        match fs::metadata(path).unwrap().permissions().mode() & 0o777 {
            0o600 => "private".to_owned(),
            0o444 => "readonly".to_owned(),
            _ => "other".to_owned(),
        }
    }
    #[cfg(not(unix))]
    {
        if fs::metadata(path).unwrap().permissions().readonly() {
            "readonly".to_owned()
        } else {
            "private".to_owned()
        }
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
        #[cfg(windows)]
        clear_readonly_files(&self.path);
        fs::remove_dir_all(&self.path).expect("remove temporary directory");
    }
}

#[cfg(windows)]
fn clear_readonly_files(root: &Path) {
    for entry in fs::read_dir(root).expect("read temporary directory") {
        let path = entry.expect("temporary entry").path();
        if path.is_dir() {
            clear_readonly_files(&path);
        } else {
            let mut permissions = fs::metadata(&path)
                .expect("temporary metadata")
                .permissions();
            if permissions.readonly() {
                permissions.set_readonly(false);
                fs::set_permissions(path, permissions).expect("make temporary file writable");
            }
        }
    }
}
