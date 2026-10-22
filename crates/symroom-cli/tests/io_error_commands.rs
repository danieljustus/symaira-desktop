#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    io::Read,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    room_toml: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    state: String,
    args: Vec<String>,
    stdout: String,
    stderr: String,
    exit_code: i32,
    files: Vec<String>,
}

/// Go's wording of the three OS errors the fixture provokes, per host family.
/// These are literals recorded from Go, not derived from the code under test.
const fn host_errors() -> [(&'static str, &'static str); 3] {
    if cfg!(windows) {
        [
            ("<ENOENT>", "The system cannot find the file specified."),
            ("<ENOTDIR>", "The system cannot find the path specified."),
            ("<EISDIR>", "Incorrect function."),
        ]
    } else {
        [
            ("<ENOENT>", "no such file or directory"),
            ("<ENOTDIR>", "not a directory"),
            ("<EISDIR>", "is a directory"),
        ]
    }
}

/// Expands the fixture's host-neutral text: `/` becomes the host separator,
/// `<work>` the work directory and each OS-error token the host wording.
fn expand(text: &str, work: &Path) -> String {
    const MARK: &str = "\u{1}work\u{1}";
    let mut text = text.replace("<work>", MARK);
    if cfg!(windows) {
        text = text.replace('/', "\\");
    }
    text = text.replace(MARK, &work.to_string_lossy());
    for (token, wording) in host_errors() {
        text = text.replace(token, wording);
    }
    text
}

fn listing(work: &Path) -> Vec<String> {
    fn walk(root: &Path, dir: &Path, out: &mut Vec<String>) {
        let mut entries: Vec<_> = fs::read_dir(dir)
            .expect("read work dir")
            .map(|entry| entry.expect("work entry"))
            .collect();
        entries.sort_by_key(std::fs::DirEntry::file_name);
        for entry in entries {
            let path = entry.path();
            let relative = path
                .strip_prefix(root)
                .expect("under root")
                .to_string_lossy()
                .replace('\\', "/");
            if path.is_dir() {
                out.push(format!("{relative}/"));
                walk(root, &path, out);
            } else {
                out.push(relative);
            }
        }
    }
    let mut files = Vec::new();
    walk(work, work, &mut files);
    files.sort();
    files
}

fn run_with_deadline(command: &mut Command) -> std::process::Output {
    let mut child = command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn Rust symroom");
    let mut stdout = child.stdout.take().expect("stdout pipe");
    let mut stderr = child.stderr.take().expect("stderr pipe");
    let out = std::thread::spawn(move || {
        let mut buffer = Vec::new();
        stdout.read_to_end(&mut buffer).expect("read stdout");
        buffer
    });
    let err = std::thread::spawn(move || {
        let mut buffer = Vec::new();
        stderr.read_to_end(&mut buffer).expect("read stderr");
        buffer
    });
    let started = Instant::now();
    let status = loop {
        if let Some(status) = child.try_wait().expect("poll child") {
            break status;
        }
        assert!(
            started.elapsed() < Duration::from_secs(20),
            "Rust symroom did not exit"
        );
        std::thread::sleep(Duration::from_millis(10));
    };
    std::process::Output {
        status,
        stdout: out.join().expect("stdout thread"),
        stderr: err.join().expect("stderr thread"),
    }
}

// Replays Go-owned failing process results in which an OS error reaches the
// user: a missing, non-directory or directory `room.toml`/journal, an unreadable
// artifact, and the doctor's absolute-path diagnostics. Go prints
// `<op> <path>: <strerror>` and fails on the room id before the journal; the
// exit code, both streams and the files left behind must match.
#[test]
fn io_errors_match_go_process_contract() {
    let path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/io-error-cli.json");
    let fixture: Fixture = serde_json::from_slice(&fs::read(&path).expect("read fixture"))
        .expect("parse I/O error fixture");
    assert_eq!(fixture.schema_version, 1);
    assert!(!fixture.oracle_revision.is_empty());
    assert_eq!(fixture.source_hashes.len(), 19);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert_eq!(fixture.cases.len(), 79, "fixture case count changed");

    let mut mismatches = Vec::new();
    for case in &fixture.cases {
        let temp = TempDir::new();
        let work = temp.path.join("work");
        for dir in ["work", "home", "data", "tmp"] {
            create_private_dir(&temp.path.join(dir));
        }
        let mut room_dir_env: Option<PathBuf> = None;
        match case.state.as_str() {
            "empty" => {}
            "nope" => room_dir_env = Some(PathBuf::from("nope")),
            "filedir" => {
                fs::write(work.join("afile"), "x").expect("write afile");
                room_dir_env = Some(PathBuf::from("afile"));
            }
            "tomldir" => create_private_dir(&work.join("room.toml")),
            "journalfile" => {
                fs::write(work.join("room.toml"), &fixture.room_toml).expect("write room.toml");
                fs::write(work.join("journal"), "x").expect("write journal file");
            }
            "artdir" => {
                fs::write(work.join("room.toml"), &fixture.room_toml).expect("write room.toml");
                create_private_dir(&work.join("journal"));
                create_private_dir(&work.join("f.txt"));
            }
            "abs-journalfile" => {
                fs::write(work.join("room.toml"), &fixture.room_toml).expect("write room.toml");
                fs::write(work.join("journal"), "x").expect("write journal file");
                room_dir_env = Some(work.clone());
            }
            other => panic!("unknown state {other}"),
        }
        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        command
            .args(&case.args)
            .current_dir(&work)
            .env_clear()
            .env("HOME", temp.path.join("home"))
            .env("USERPROFILE", temp.path.join("home"))
            .env("XDG_DATA_HOME", temp.path.join("data"))
            .env("TMPDIR", temp.path.join("tmp"))
            .env("PATH", temp.path.join("tmp"))
            .env("TZ", "UTC")
            .env("LC_ALL", "C")
            .env("LANG", "C")
            .env("SYMROOM_IDENTITY_KEY", &fixture.identity_key);
        #[cfg(windows)]
        command.env(
            "SYSTEMROOT",
            std::env::var_os("SYSTEMROOT").unwrap_or_default(),
        );
        if let Some(room) = &room_dir_env {
            command.env("SYMROOM_ROOM_DIR", room);
        }
        let output = run_with_deadline(&mut command);
        let stdout = String::from_utf8(output.stdout).expect("stdout is UTF-8");
        let stderr = String::from_utf8(output.stderr).expect("stderr is UTF-8");
        let code = output.status.code();
        let want_stdout = expand(&case.stdout, &work);
        let want_stderr = expand(&case.stderr, &work);
        let files = listing(&work);
        if code != Some(case.exit_code)
            || stdout != want_stdout
            || stderr != want_stderr
            || files != case.files
        {
            mismatches.push(format!(
                "{}: exit {code:?} (want {}), stdout {stdout:?} (want {want_stdout:?}), \
                 stderr {stderr:?} (want {want_stderr:?}), files {files:?} (want {:?})",
                case.name, case.exit_code, case.files
            ));
        }
    }
    assert!(
        mismatches.is_empty(),
        "{} of {} I/O error cases differ from Go:\n{}",
        mismatches.len(),
        fixture.cases.len(),
        mismatches.join("\n")
    );
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock after epoch")
            .as_nanos();
        let nonce = format!(
            "{nonce}-{}",
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed)
        );
        let path =
            std::env::temp_dir().join(format!("symroom-io-error-{}-{nonce}", std::process::id()));
        create_private_dir(&path);
        Self { path }
    }
}

// The SQLite state check rejects group- or world-accessible ancestors, so every
// disposable root is owner-only, as in the Go oracle run.
fn create_private_dir(path: &Path) {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(path)
        .expect("create temporary test directory");
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
