// Shared process harness for the RUST-014 MCP replays: starts the native
// `symdesk mcp` in a disposable HOME/XDG root with a controlled PATH and
// returns the raw stdout/stderr/exit code. Input is written from a thread so a
// server that exits before reading everything (oversized frames) cannot block
// the test.
use std::{
    ffi::OsString,
    fs,
    io::Write,
    path::Path,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

use crate::process_env::IsolatedEnvironment;

pub struct Outcome {
    pub code: Option<i32>,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

pub struct Spec<'a> {
    pub root: &'a Path,
    pub args: Vec<OsString>,
    pub input: Vec<u8>,
    pub path_env: OsString,
    pub extra_env: Vec<(OsString, OsString)>,
    pub deadline: Duration,
}

pub fn make_dirs(root: &Path) {
    for dir in ["home", "config", "cache", "data", "tmp", "vault", "cwd"] {
        fs::create_dir_all(root.join(dir)).unwrap();
    }
}

pub fn run(spec: &Spec<'_>, label: &str) -> Outcome {
    let root = spec.root;
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command.args(&spec.args);
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
        .env("PATH", &spec.path_env)
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC");
    for (key, value) in &spec.extra_env {
        command.env(key, value);
    }
    let mut child = command
        .current_dir(root.join("cwd"))
        .stdin(Stdio::piped())
        .stdout(fs::File::create(root.join("stdout")).unwrap())
        .stderr(fs::File::create(root.join("stderr")).unwrap())
        .spawn()
        .expect("start native MCP without Go fallback");
    let mut stdin = child.stdin.take().unwrap();
    let input = spec.input.clone();
    let writer = thread::spawn(move || {
        // A broken pipe is the expected outcome for inputs the server rejects early.
        let _ = stdin.write_all(&input);
    });
    let deadline = Instant::now() + spec.deadline;
    let status = loop {
        if let Some(status) = child.try_wait().unwrap() {
            break status;
        }
        if Instant::now() >= deadline {
            child.kill().unwrap();
            child.wait().unwrap();
            panic!("MCP process deadline: {label}");
        }
        thread::sleep(Duration::from_millis(5));
    };
    writer.join().unwrap();
    Outcome {
        code: status.code(),
        stdout: fs::read(root.join("stdout")).unwrap(),
        stderr: fs::read(root.join("stderr")).unwrap(),
    }
}
