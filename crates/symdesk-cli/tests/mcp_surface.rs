#![deny(unsafe_code)]

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

use std::{
    fs,
    io::Write,
    path::PathBuf,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

use serde::Deserialize;

#[path = "support/isolated_root.rs"]
mod isolated_root;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    #[serde(default)]
    allow_write: bool,
    #[serde(default)]
    framed: bool,
    request: String,
    stdout: String,
    stderr: String,
    exit_code: i32,
}

#[test]
fn full_catalog_matches_go_stdio_bytes_in_both_modes_and_transports() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/tools-process.json"),
        )
        .expect("Go process transcript fixture"),
    )
    .expect("decode Go process transcript");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 4);
    for case in fixture.cases {
        replay(&case);
    }
}

fn replay(case: &Case) {
    let root = isolated_root::create(&format!("symdesk-mcp-surface-{}", case.id));
    for dir in ["home", "config", "cache", "data", "tmp", "vault", "cwd"] {
        fs::create_dir_all(root.join(dir)).unwrap();
    }
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command.args(["--vault", root.join("vault").to_str().unwrap(), "mcp"]);
    if case.allow_write {
        command.arg("--allow-write");
    }
    command.isolated_env();
    for (key, dir) in [
        ("HOME", "home"),
        ("USERPROFILE", "home"),
        ("XDG_CONFIG_HOME", "config"),
        ("XDG_CACHE_HOME", "cache"),
        ("XDG_DATA_HOME", "data"),
        ("TMPDIR", "tmp"),
        ("TMP", "tmp"),
        ("TEMP", "tmp"),
    ] {
        command.env(key, root.join(dir));
    }
    if let Some(value) = std::env::var_os("SYSTEMROOT") {
        command.env("SYSTEMROOT", value);
    }
    command
        .env("PATH", "")
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC");
    let mut child = command
        .current_dir(root.join("cwd"))
        .stdin(Stdio::piped())
        .stdout(fs::File::create(root.join("stdout")).unwrap())
        .stderr(fs::File::create(root.join("stderr")).unwrap())
        .spawn()
        .expect("start native MCP without Go fallback");
    let input = if case.framed {
        format!(
            "Content-Length: {}\r\n\r\n{}",
            case.request.len(),
            case.request
        )
    } else {
        format!("{}\n", case.request)
    };
    child
        .stdin
        .take()
        .unwrap()
        .write_all(input.as_bytes())
        .unwrap();
    let deadline = Instant::now() + Duration::from_secs(30);
    let status = loop {
        if let Some(status) = child.try_wait().unwrap() {
            break status;
        }
        if Instant::now() >= deadline {
            child.kill().unwrap();
            child.wait().unwrap();
            panic!("MCP catalog deadline: {}", case.id);
        }
        thread::sleep(Duration::from_millis(5));
    };
    let stdout = fs::read(root.join("stdout")).unwrap();
    let stderr = fs::read(root.join("stderr")).unwrap();
    fs::remove_dir_all(root).unwrap();
    assert_eq!(status.code(), Some(case.exit_code), "{}", case.id);
    assert_eq!(stderr, case.stderr.as_bytes(), "{} stderr", case.id);
    assert_eq!(
        stdout,
        case.stdout.as_bytes(),
        "{} raw protocol bytes",
        case.id
    );
}
