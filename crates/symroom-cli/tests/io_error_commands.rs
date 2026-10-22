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
    room_toml_variants: BTreeMap<String, String>,
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
    /// Vectors that need Unix errno, chmod or symlink semantics; Windows counts
    /// them as SKIP instead of replaying them.
    #[serde(default)]
    unix_only: bool,
    #[serde(default)]
    rooms: Vec<String>,
}

/// Go's wording of the four OS errors the fixture provokes, per host family.
/// These are literals recorded from Go, not derived from the code under test.
const fn host_errors() -> [(&'static str, &'static str); 4] {
    if cfg!(windows) {
        [
            ("<ENOENT>", "The system cannot find the file specified."),
            ("<ENOTDIR>", "The system cannot find the path specified."),
            ("<EISDIR>", "Incorrect function."),
            ("<EACCES>", "Access is denied."),
        ]
    } else {
        [
            ("<ENOENT>", "no such file or directory"),
            ("<ENOTDIR>", "not a directory"),
            ("<EISDIR>", "is a directory"),
            ("<EACCES>", "permission denied"),
        ]
    }
}

/// Expands the fixture's host-neutral text: `/` becomes the host separator,
/// `<work>` and `<link>` the disposable directories and each OS-error token the
/// host wording.
fn expand(text: &str, work: &Path, link: &Path) -> String {
    const WORK: &str = "\u{1}work\u{1}";
    const LINK: &str = "\u{1}link\u{1}";
    let mut text = text.replace("<work>", WORK).replace("<link>", LINK);
    if cfg!(windows) {
        text = text.replace('/', "\\");
    }
    text = text
        .replace(WORK, &work.to_string_lossy())
        .replace(LINK, &link.to_string_lossy());
    for (token, wording) in host_errors() {
        text = text.replace(token, wording);
    }
    text
}

/// The Go test's `\b(ev|chk)_[0-9a-f]{16,}\b` -> `<ev_id>` / `<chk_id>`: generated
/// ids differ on every run.
fn mask_ids(text: &str) -> String {
    let bytes = text.as_bytes();
    let word = |byte: u8| byte.is_ascii_alphanumeric() || byte == b'_';
    let mut out = String::new();
    let mut index = 0;
    while index < bytes.len() {
        let rest = &text[index..];
        let boundary = index == 0 || !word(bytes[index - 1]);
        let prefix = ["ev_", "chk_"]
            .into_iter()
            .find(|prefix| boundary && rest.starts_with(prefix));
        if let Some(prefix) = prefix {
            let hex = rest[prefix.len()..]
                .bytes()
                .take_while(|byte| matches!(byte, b'0'..=b'9' | b'a'..=b'f'))
                .count();
            let end = index + prefix.len() + hex;
            if hex >= 16 && (end == bytes.len() || !word(bytes[end])) {
                out.push('<');
                out.push_str(prefix);
                out.push_str("id>");
                index = end;
                continue;
            }
        }
        let ch = rest.chars().next().expect("non-empty rest");
        out.push(ch);
        index += ch.len_utf8();
    }
    out
}

/// `<segment>:<room>` for every event line left in `journal/*.jsonl`.
fn rooms(work: &Path) -> Vec<String> {
    let Ok(entries) = fs::read_dir(work.join("journal")) else {
        return Vec::new();
    };
    let mut paths: Vec<_> = entries
        .filter_map(Result::ok)
        .map(|entry| entry.path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "jsonl"))
        .collect();
    paths.sort();
    let mut rooms = Vec::new();
    for path in paths {
        let Ok(content) = fs::read_to_string(&path) else {
            continue;
        };
        let segment = path.file_stem().expect("segment name").to_string_lossy();
        for line in content.split('\n').filter(|line| !line.trim().is_empty()) {
            let event: serde_json::Value = serde_json::from_str(line).expect("journal line");
            let room = event["room"].as_str().unwrap_or_default();
            rooms.push(format!("{segment}:{room}"));
        }
    }
    rooms
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
    assert_eq!(
        fixture.cases.len(),
        CASE_COUNT,
        "fixture case count changed"
    );
    let unix_only = fixture.cases.iter().filter(|case| case.unix_only).count();
    assert!(
        unix_only > 0,
        "the Unix-only marking is part of the contract"
    );

    let mut mismatches = Vec::new();
    let mut skipped = Vec::new();
    for case in &fixture.cases {
        let temp = TempDir::new();
        let work = temp.path.join("work");
        let link = temp.path.join("link");
        for dir in ["work", "home", "data", "tmp"] {
            create_private_dir(&temp.path.join(dir));
        }
        if cfg!(windows) && case.unix_only {
            skipped.push(format!(
                "{} (Unix-only vector: errno, chmod or symlink semantics)",
                case.name
            ));
            continue;
        }
        let mut room_dir_env: Option<PathBuf> = None;
        let mut cwd = work.clone();
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
            "toml-garbage" | "toml-noid" | "toml-lenient" => {
                let body = fixture
                    .room_toml_variants
                    .get(&case.state)
                    .expect("room.toml variant");
                fs::write(work.join("room.toml"), body).expect("write room.toml");
            }
            "segperm" => {
                fs::write(work.join("room.toml"), &fixture.room_toml).expect("write room.toml");
                create_private_dir(&work.join("journal"));
                let segment = work.join("journal").join("mem_other.jsonl");
                fs::write(&segment, "").expect("write segment");
                if !make_unreadable(&segment) {
                    skipped.push(format!(
                        "{} (mode 000 files are readable here: privileged user)",
                        case.name
                    ));
                    continue;
                }
            }
            "pwd-link" => {
                fs::write(work.join("room.toml"), &fixture.room_toml).expect("write room.toml");
                fs::write(work.join("journal"), "x").expect("write journal file");
                make_symlink(&work, &link);
                cwd = link.clone();
            }
            other => panic!("unknown state {other}"),
        }
        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        command
            .args(&case.args)
            .current_dir(&cwd)
            .env_clear()
            .env("HOME", temp.path.join("home"))
            .env("USERPROFILE", temp.path.join("home"))
            .env("XDG_DATA_HOME", temp.path.join("data"))
            .env("TMPDIR", temp.path.join("tmp"))
            .env("PATH", temp.path.join("tmp"))
            .env("TZ", "UTC")
            .env("LC_ALL", "C")
            .env("LANG", "C")
            // Go's exec sets PWD to cmd.Dir, and `os.Getwd` prefers it over the
            // kernel's name for the directory; the Go oracle ran the same way.
            .env("PWD", &cwd)
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
        let stdout = mask_ids(&String::from_utf8(output.stdout).expect("stdout is UTF-8"));
        let stderr = mask_ids(&String::from_utf8(output.stderr).expect("stderr is UTF-8"));
        let code = output.status.code();
        let want_stdout = expand(&case.stdout, &work, &link);
        let want_stderr = expand(&case.stderr, &work, &link);
        let files = listing(&work);
        let found_rooms = rooms(&work);
        if code != Some(case.exit_code)
            || stdout != want_stdout
            || stderr != want_stderr
            || files != case.files
            || found_rooms != case.rooms
        {
            mismatches.push(format!(
                "{}: exit {code:?} (want {}), stdout {stdout:?} (want {want_stdout:?}), \
                 stderr {stderr:?} (want {want_stderr:?}), files {files:?} (want {:?}), \
                 rooms {found_rooms:?} (want {:?})",
                case.name, case.exit_code, case.files, case.rooms
            ));
        }
    }
    let replayed = fixture.cases.len() - skipped.len();
    eprintln!(
        "I/O error cases: {} total, {replayed} replayed, {} SKIPPED",
        fixture.cases.len(),
        skipped.len()
    );
    for reason in &skipped {
        eprintln!("SKIP {reason}");
    }
    assert!(replayed > 0, "every I/O error case was skipped");
    if cfg!(windows) {
        assert_eq!(
            skipped.len(),
            unix_only,
            "Windows skips exactly the Unix-only vectors"
        );
    }
    assert!(
        mismatches.is_empty(),
        "{} of {} I/O error cases differ from Go:\n{}",
        mismatches.len(),
        fixture.cases.len(),
        mismatches.join("\n")
    );
}

/// Number of vectors in `io-error-cli.json`; a change must be deliberate.
const CASE_COUNT: usize = 143;

/// Makes `path` unreadable (mode 000) and reports whether that holds: a
/// privileged user still reads it, which makes the vector meaningless.
#[cfg(unix)]
fn make_unreadable(path: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o0)).expect("chmod 000 segment");
    fs::File::open(path).is_err()
}

#[cfg(not(unix))]
fn make_unreadable(_path: &Path) -> bool {
    false
}

#[cfg(unix)]
fn make_symlink(target: &Path, link: &Path) {
    std::os::unix::fs::symlink(target, link).expect("symlink working directory");
}

#[cfg(not(unix))]
fn make_symlink(_target: &Path, _link: &Path) {
    unreachable!("the symlink vector is Unix-only");
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
