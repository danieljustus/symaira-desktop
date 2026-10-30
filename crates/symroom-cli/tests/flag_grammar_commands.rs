#![deny(unsafe_code)]

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

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

#[path = "support/replay_text.rs"]
mod replay_text;
use replay_text::{HOST_SEPARATOR, expand_separators};

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

/// Expands the fixture's host-neutral text: a path `/` becomes the host
/// separator (a literal `<slash>` stays `/`), `<work>` the disposable directory
/// and each OS-error token the host wording.
fn expand(text: &str, work: &Path) -> String {
    expand_with(text, work, HOST_SEPARATOR)
}

/// [`expand`] with an explicit separator so both hosts are testable anywhere.
fn expand_with(text: &str, work: &Path, separator: &str) -> String {
    const WORK: &str = "\u{1}work\u{1}";
    let mut text = expand_separators(&text.replace("<work>", WORK), separator);
    text = text.replace(WORK, &work.to_string_lossy());
    for (token, wording) in host_errors() {
        text = text.replace(token, wording);
    }
    text
}

/// The Go test's `\b(ev|chk|rm)_[0-9a-f]{16,}\b` -> `<ev_id>` / `<chk_id>` /
/// `<rm_id>`: generated ids differ on every run. `identity create` also mints a
/// random member id, masked as `<mem_id>` for that command only.
fn mask_ids(text: &str, args: &[String]) -> String {
    let identity_create = args.starts_with(&["identity".to_owned(), "create".to_owned()]);
    let bytes = text.as_bytes();
    let word = |byte: u8| byte.is_ascii_alphanumeric() || byte == b'_';
    let mut out = String::new();
    let mut index = 0;
    while index < bytes.len() {
        let rest = &text[index..];
        let boundary = index == 0 || !word(bytes[index - 1]);
        let prefix = ["ev_", "chk_", "rm_", "mem_"]
            .into_iter()
            .filter(|prefix| *prefix != "mem_" || identity_create)
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

// Replays the Go `flag` package grammar across every command path: the
// `--flag=value` forms, a lone `-`, the `--` terminator, malformed names,
// `-h`/`-help`, and every flag type's value forms (ROOMCLI-001). The fixture is
// recorded from the shipped Go binary; exit code, both streams and the files
// and journal rooms left behind must match.
#[test]
fn flag_grammar_matches_go_process_contract() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/room/flag-grammar-cli.json");
    let fixture: Fixture = serde_json::from_slice(&fs::read(&path).expect("read fixture"))
        .expect("parse flag grammar fixture");
    assert_eq!(fixture.schema_version, 1);
    assert!(!fixture.oracle_revision.is_empty());
    assert_eq!(fixture.source_hashes.len(), 17);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert_eq!(
        fixture.cases.len(),
        CASE_COUNT,
        "fixture case count changed"
    );
    let unix_only = fixture.cases.iter().filter(|case| case.unix_only).count();
    assert_eq!(unix_only, UNIX_ONLY_COUNT, "Unix-only marking changed");

    let mut mismatches = Vec::new();
    let mut skipped = Vec::new();
    for case in &fixture.cases {
        if cfg!(windows) && case.unix_only {
            skipped.push(format!("{} (Unix-only vector: errno semantics)", case.name));
            continue;
        }
        let temp = TempDir::new();
        let work = temp.path.join("work");
        for dir in ["work", "home", "data", "tmp"] {
            create_private_dir(&temp.path.join(dir));
        }
        match case.state.as_str() {
            "empty" => {}
            "room" => {
                fs::write(work.join("room.toml"), &fixture.room_toml).expect("write room.toml");
                create_private_dir(&work.join("journal"));
            }
            other => panic!("unknown state {other}"),
        }
        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        command
            .args(&case.args)
            .current_dir(&work)
            .isolated_env()
            .env("HOME", temp.path.join("home"))
            .env("USERPROFILE", temp.path.join("home"))
            .env("XDG_DATA_HOME", temp.path.join("data"))
            .env("TMPDIR", temp.path.join("tmp"))
            // Coverage builds write profiles into the cwd by default, which
            // would show up in the compared file listing.
            .env(
                "LLVM_PROFILE_FILE",
                temp.path.join("tmp/symroom-%p.profraw"),
            )
            .env("PATH", temp.path.join("tmp"))
            .env("TZ", "UTC")
            .env("LC_ALL", "C")
            .env("LANG", "C")
            // Go's exec sets PWD to cmd.Dir; the Go oracle ran the same way.
            .env("PWD", &work)
            .env("SYMROOM_IDENTITY_KEY", &fixture.identity_key);
        #[cfg(windows)]
        command.env(
            "SYSTEMROOT",
            std::env::var_os("SYSTEMROOT").unwrap_or_default(),
        );
        let output = run_with_deadline(&mut command);
        let stdout = mask_ids(
            &String::from_utf8(output.stdout).expect("stdout is UTF-8"),
            &case.args,
        );
        let stderr = mask_ids(
            &String::from_utf8(output.stderr).expect("stderr is UTF-8"),
            &case.args,
        );
        let code = output.status.code();
        let want_stdout = expand(&case.stdout, &work);
        let want_stderr = expand(&case.stderr, &work);
        let files = listing(&work);
        let found_rooms: Vec<String> = rooms(&work)
            .iter()
            .map(|room| mask_ids(room, &case.args))
            .collect();
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
        "flag grammar cases: {} total, {replayed} replayed, {} SKIPPED",
        fixture.cases.len(),
        skipped.len()
    );
    for reason in &skipped {
        eprintln!("SKIP {reason}");
    }
    assert!(replayed > 0, "every flag grammar case was skipped");
    if cfg!(windows) {
        assert_eq!(
            skipped.len(),
            unix_only,
            "Windows skips exactly the Unix-only vectors"
        );
    }
    assert!(
        mismatches.is_empty(),
        "{} of {} flag grammar cases differ from Go:\n{}",
        mismatches.len(),
        fixture.cases.len(),
        mismatches.join("\n")
    );
}

fn load_fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/room/flag-grammar-cli.json");
    serde_json::from_slice(&fs::read(path).expect("read fixture")).expect("parse fixture")
}

/// `\` is what Go's `filepath` prints on Windows, `/` everywhere else.
const SEPARATORS: [&str; 2] = ["/", "\\"];

/// Go prints doctor's remediation literals (`create/select`,
/// `rebuild .symroom/index.sqlite`) unchanged on Windows, while a path from
/// `filepath.Join` (the `index rebuild` success line) gets the host separator.
/// Expanding for the Windows separator on any host must keep the former and
/// rewrite only the latter.
#[test]
fn literal_slashes_stay_literal_and_path_separators_follow_the_host() {
    let work = Path::new("W");
    let doctor = "Run `symroom index` to rebuild .symroom<slash>index.sqlite.";
    for separator in SEPARATORS {
        assert_eq!(
            expand_with(doctor, work, separator),
            "Run `symroom index` to rebuild .symroom/index.sqlite.",
            "doctor literal must not follow the host separator ({separator:?})"
        );
        assert_eq!(
            expand_with("create<slash>select an identity", work, separator),
            "create/select an identity"
        );
    }
    // `index rebuild` success: dbPath = filepath.Join(".symroom", "index.sqlite").
    let rebuilt = "Rebuilt derived index at .symroom/index.sqlite\n";
    assert_eq!(
        expand_with(rebuilt, work, "\\"),
        "Rebuilt derived index at .symroom\\index.sqlite\n"
    );
    assert_eq!(expand_with(rebuilt, work, "/"), rebuilt);
    // `<work>` is substituted after the separator pass, so its own separators survive.
    assert_eq!(
        expand_with("open <work>/journal:", Path::new("C:\\w\\work"), "\\"),
        "open C:\\w\\work\\journal:"
    );
}

/// The 38 `doctor` vectors (every state/flag spelling that reaches the doctor
/// report) carry the two literals. Their expectation must be identical for both
/// separators and must equal the plain Go literal text.
#[test]
fn doctor_vectors_expand_identically_on_every_host() {
    let fixture = load_fixture();
    let work = Path::new("W");
    let doctor: Vec<&Case> = fixture
        .cases
        .iter()
        .filter(|case| case.stdout.contains("<slash>"))
        .collect();
    assert_eq!(doctor.len(), 38, "doctor vectors in the fixture changed");
    for case in doctor {
        assert_eq!(case.args.first().map(String::as_str), Some("doctor"));
        let unix = expand_with(&case.stdout, work, "/");
        let windows = expand_with(&case.stdout, work, "\\");
        assert_eq!(unix, windows, "{}: Windows expectation drifted", case.name);
        assert!(
            windows.contains("create/select") && windows.contains(".symroom/index.sqlite"),
            "{}: literals lost",
            case.name
        );
        // Go prints the literals verbatim: the Windows text is the recorded text
        // with only the token resolved (JSON output keeps its own `\n` escapes).
        assert_eq!(
            windows,
            case.stdout.replace("<slash>", "/"),
            "{}: a literal slash became a path separator",
            case.name
        );
    }
}

/// No flag grammar vector prints a host path: a raw `/` in a recorded stream
/// would be an unclassified Go literal that the Windows replay corrupts. The Go
/// generator refuses to record one; this is the replay-side mirror.
#[test]
fn recorded_streams_hold_no_unclassified_slash() {
    for case in load_fixture().cases {
        for (stream, text) in [("stdout", &case.stdout), ("stderr", &case.stderr)] {
            assert!(
                !text.contains('/'),
                "{}: {stream} holds a raw '/': {text:?}",
                case.name
            );
        }
    }
}

/// Number of vectors in `flag-grammar-cli.json`; a change must be deliberate.
const CASE_COUNT: usize = 2447;
/// How many of them name a host OS error and replay on Unix only.
const UNIX_ONLY_COUNT: usize = 47;

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
        let path = std::env::temp_dir().join(format!(
            "symroom-flag-grammar-{}-{nonce}",
            std::process::id()
        ));
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
