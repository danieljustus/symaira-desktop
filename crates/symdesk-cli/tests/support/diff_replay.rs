//! Frozen replay of a Go-recorded diffharness suite.
//!
//! `scripts/rust-port/cmd/diffrecord` runs the real Go `symdesk` binary through
//! the same isolated sandbox as `scripts/rust-port/cmd/diffharness` and records
//! the normalized observation of every case. This module rebuilds that sandbox
//! (HOME/XDG/tmp roots, setup files, environment, working directory, stdin,
//! `prepare_args`), runs the Rust binary, applies the same stream normalization
//! (`internal/diff/compare.go`) and compares with the recording.
//!
//! Mirrored from `internal/diff/exec.go`: sandbox layout, placeholder
//! replacement, reserved-environment rules, setup modes and mtimes, and the
//! prepare step. Not mirrored, and therefore rejected when a recording asks
//! for it: `compare_files` manifests, symlink setup entries, raw base64
//! environment values and `content_format` escaping. No suite in the freeze
//! scope uses them.

use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    os::unix::{
        fs::{DirBuilderExt, OpenOptionsExt},
        process::{CommandExt, ExitStatusExt},
    },
    path::{Component, Path, PathBuf},
    process::{Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Source {
    cases_file: String,
    cases_sha256: String,
    go_version: String,
    binary_version: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct SetupFile {
    path: String,
    #[serde(default)]
    base: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    link_target: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    content_format: String,
    #[serde(default)]
    mode: u32,
    #[serde(default)]
    mtime_ns: Option<i64>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Case {
    id: String,
    #[serde(default)]
    binary: String,
    #[serde(default)]
    stage: String,
    #[serde(default)]
    args: Vec<String>,
    #[serde(default)]
    prepare_args: Vec<String>,
    #[serde(default)]
    stdin: String,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    sandbox_env: BTreeMap<String, String>,
    #[serde(default)]
    sandbox_env_base64: BTreeMap<String, String>,
    #[serde(default)]
    unset_sandbox_env: Vec<String>,
    #[serde(default)]
    working_dir: String,
    #[serde(default)]
    timeout_ms: u64,
    #[serde(default)]
    stdout_mode: String,
    #[serde(default)]
    #[allow(dead_code)] // Windows is out of scope; the field must still decode.
    windows_stdout_mode: String,
    #[serde(default)]
    stderr_mode: String,
    #[serde(default)]
    compare_files: bool,
    #[serde(default)]
    compare_sidecar_layout: bool,
    #[serde(default)]
    setup: Vec<SetupFile>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Observation {
    exit_code: i32,
    #[serde(default)]
    signal: String,
    stdout_mode: String,
    #[serde(default)]
    stdout: String,
    #[serde(default)]
    stdout_base64: String,
    stderr_mode: String,
    #[serde(default)]
    stderr: String,
    #[serde(default)]
    stderr_base64: String,
    #[serde(default)]
    files: Option<serde_json::Value>,
    #[serde(default)]
    sidecar_layout: Vec<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RecordedCase {
    id: String,
    case: Case,
    observation: Observation,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Recording {
    schema_version: u32,
    oracle: Oracle,
    source: Source,
    stage: String,
    cases: Vec<RecordedCase>,
}

/// Replays every recorded case against `binary` and returns the case count.
/// `expected_cases` pins the recording's size so a truncated file fails.
pub fn replay_all(recording: &str, stage: &str, expected_cases: usize, binary: &str) -> usize {
    let recording: Recording = serde_json::from_str(recording).expect("decode Go recording");
    assert_eq!(recording.schema_version, 1);
    assert_eq!(recording.stage, stage);
    assert_eq!(recording.oracle.commit.len(), 40, "oracle commit");
    assert!(!recording.oracle.release.is_empty(), "oracle release");
    assert!(recording.source.cases_file.starts_with("testdata/port/"));
    assert_eq!(recording.source.cases_sha256.len(), 64);
    assert!(recording.source.go_version.starts_with("go1."));
    assert_eq!(
        recording.source.binary_version, "devel",
        "the Rust test binary is built without SYMDESK_VERSION"
    );
    assert_eq!(recording.cases.len(), expected_cases, "recorded case count");
    let mut seen = std::collections::BTreeSet::new();
    for recorded in &recording.cases {
        assert!(
            seen.insert(recorded.id.clone()),
            "duplicate {}",
            recorded.id
        );
        assert_eq!(recorded.id, recorded.case.id);
        assert_eq!(recorded.case.stage, stage, "{}", recorded.id);
        replay_case(binary, recorded);
    }
    recording.cases.len()
}

static NEXT_SANDBOX: AtomicU64 = AtomicU64::new(0);

/// Mirrors `os.MkdirTemp("", "symdesk-port-")`: the root is deliberately not
/// canonicalized, because console normalization replaces the exact root text.
fn create_sandbox() -> PathBuf {
    let tick = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system clock")
        .as_nanos();
    loop {
        let sequence = NEXT_SANDBOX.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "symdesk-port-{}-{tick}-{sequence}",
            std::process::id()
        ));
        match fs::DirBuilder::new().mode(0o700).create(&root) {
            Ok(()) => return root,
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => panic!("create sandbox {}: {error}", root.display()),
        }
    }
}

fn mkdir_all(path: &Path) {
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .unwrap_or_else(|error| panic!("create {}: {error}", path.display()));
}

fn replace(value: &str, replacements: &[(&str, String)]) -> String {
    let mut result = value.to_owned();
    for (key, replacement) in replacements {
        result = result.replace(key, replacement);
    }
    result
}

/// `safeWorkspacePath`: a non-empty relative path that stays below `base`.
fn safe_path(base: &Path, relative: &str) -> PathBuf {
    assert!(
        !relative.is_empty() && !Path::new(relative).is_absolute(),
        "workspace path must be non-empty and relative: {relative:?}"
    );
    let mut depth = 0i32;
    let mut clean = PathBuf::new();
    for component in Path::new(relative).components() {
        match component {
            Component::Normal(part) => {
                depth += 1;
                clean.push(part);
            }
            Component::CurDir => {}
            Component::ParentDir => {
                depth -= 1;
                assert!(depth >= 0, "workspace path escapes sandbox: {relative:?}");
                clean.pop();
            }
            other => panic!("unsupported path component {other:?} in {relative:?}"),
        }
    }
    base.join(clean)
}

fn setup_files(
    case: &Case,
    root: &Path,
    home: &Path,
    workspace: &Path,
    replacements: &[(&str, String)],
) {
    for setup in &case.setup {
        let base = match setup.base.as_str() {
            "" | "workspace" => workspace,
            "home" => home,
            "sandbox" => root,
            other => panic!("{}: unsupported setup base {other:?}", case.id),
        };
        let path = safe_path(base, &replace(&setup.path, replacements));
        let kind = if setup.kind.is_empty() {
            "file"
        } else {
            setup.kind.as_str()
        };
        let mode = if setup.mode == 0 { 0o600 } else { setup.mode };
        match kind {
            "directory" => {
                fs::DirBuilder::new()
                    .recursive(true)
                    .mode(mode)
                    .create(&path)
                    .expect("create setup directory");
            }
            "file" => {
                assert!(
                    setup.content_format.is_empty() && setup.link_target.is_empty(),
                    "{}: setup content_format/link_target are not replayed",
                    case.id
                );
                mkdir_all(path.parent().expect("setup parent"));
                let mut file = fs::OpenOptions::new()
                    .write(true)
                    .create(true)
                    .truncate(true)
                    .mode(mode)
                    .open(&path)
                    .unwrap_or_else(|error| {
                        panic!("{}: create {}: {error}", case.id, path.display())
                    });
                file.write_all(replace(&setup.content, replacements).as_bytes())
                    .expect("write setup file");
                if let Some(nanos) = setup.mtime_ns {
                    let mtime = if nanos >= 0 {
                        UNIX_EPOCH + Duration::from_nanos(nanos as u64)
                    } else {
                        UNIX_EPOCH - Duration::from_nanos(nanos.unsigned_abs())
                    };
                    file.set_modified(mtime).expect("set fixture mtime");
                }
            }
            other => panic!("{}: unsupported setup kind {other:?}", case.id),
        }
    }
}

fn reserved_env(key: &str) -> bool {
    matches!(
        key,
        "HOME"
            | "USERPROFILE"
            | "XDG_CONFIG_HOME"
            | "XDG_DATA_HOME"
            | "XDG_CACHE_HOME"
            | "XDG_STATE_HOME"
            | "XDG_RUNTIME_DIR"
            | "TMPDIR"
            | "TMP"
            | "TEMP"
            | "LANG"
            | "LC_ALL"
            | "TZ"
            | "SYMDESK_VAULT"
            | "SYMDESK_SIDECAR"
            | "SYMROOM_IDENTITY_KEY"
            | "SYMROOM_ROOM_DIR"
    )
}

fn sandbox_path_env(key: &str) -> bool {
    matches!(
        key,
        "HOME"
            | "USERPROFILE"
            | "XDG_CONFIG_HOME"
            | "XDG_DATA_HOME"
            | "XDG_CACHE_HOME"
            | "TMPDIR"
            | "TMP"
            | "TEMP"
            | "SYMDESK_VAULT"
            | "SYMDESK_SIDECAR"
            | "SYMRELATE_CONFIG_HOME"
            | "SYMRELATE_DATA_HOME"
            | "SYMRELATE_CACHE_HOME"
            | "SYMINGEST_VAULT"
            | "SYMINGEST_DB_PATH"
            | "SYMINGEST_ARCHIVE_PATH"
            | "SYMINGEST_INBOX"
    )
}

/// `isolatedEnvForCaseWithBase64` for the keys the frozen suites use.
fn isolated_env(
    case: &Case,
    root: &Path,
    replacements: &[(&str, String)],
) -> BTreeMap<String, String> {
    assert!(
        case.sandbox_env.is_empty()
            && case.sandbox_env_base64.is_empty()
            && case.unset_sandbox_env.is_empty(),
        "{}: sandbox_env variants are not replayed",
        case.id
    );
    let home = root.join("home");
    let display = |path: PathBuf| path.to_string_lossy().into_owned();
    let mut values: BTreeMap<String, String> = [
        ("HOME", display(home.clone())),
        ("USERPROFILE", display(home.clone())),
        ("XDG_CONFIG_HOME", display(home.join(".config"))),
        ("XDG_DATA_HOME", display(home.join(".local/share"))),
        ("XDG_CACHE_HOME", display(home.join(".cache"))),
        ("XDG_STATE_HOME", display(home.join(".local/state"))),
        ("XDG_RUNTIME_DIR", display(root.join("runtime"))),
        ("TMPDIR", display(root.join("tmp"))),
        ("TMP", display(root.join("tmp"))),
        ("TEMP", display(root.join("tmp"))),
        ("LANG", "C".to_owned()),
        ("LC_ALL", "C".to_owned()),
        ("TZ", "UTC".to_owned()),
        ("TERM", "dumb".to_owned()),
        ("NO_COLOR", "1".to_owned()),
        ("SYMDESK_VAULT", String::new()),
        ("SYMDESK_SIDECAR", String::new()),
        ("SYMROOM_IDENTITY_KEY", String::new()),
        ("SYMROOM_ROOM_DIR", String::new()),
    ]
    .into_iter()
    .map(|(key, value)| (key.to_owned(), value))
    .collect();
    if let Some(path) = std::env::var_os("PATH") {
        values.insert("PATH".to_owned(), path.to_string_lossy().into_owned());
    }
    for (key, raw) in &case.env {
        assert!(
            key == &key.to_uppercase(),
            "{}: case environment key must be uppercase: {key:?}",
            case.id
        );
        assert!(
            !reserved_env(key),
            "{}: case environment cannot override {key:?}",
            case.id
        );
        assert!(
            !sandbox_path_env(key),
            "{}: case path environment {key:?} must use sandbox_env",
            case.id
        );
        values.insert(key.clone(), replace(raw, replacements));
    }
    values
}

struct Outcome {
    exit_code: i32,
    signal: String,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
}

/// Go's `syscall.Signal.String` for the signals a CLI can plausibly die from.
fn signal_name(signal: i32) -> String {
    match signal {
        1 => "hangup",
        2 => "interrupt",
        3 => "quit",
        6 => "aborted",
        9 => "killed",
        11 => "segmentation fault",
        13 => "broken pipe",
        14 => "alarm clock",
        15 => "terminated",
        other => panic!("unmapped termination signal {other}"),
    }
    .to_owned()
}

fn run_process(
    binary: &str,
    args: &[String],
    dir: &Path,
    env: &BTreeMap<String, String>,
    stdin: &str,
    timeout: Duration,
    capture: bool,
) -> Outcome {
    let mut command = Command::new(binary);
    command
        .args(args)
        .current_dir(dir)
        .env_clear()
        .envs(env)
        .process_group(0)
        .stdin(Stdio::piped())
        .stdout(if capture {
            Stdio::piped()
        } else {
            Stdio::null()
        })
        .stderr(if capture {
            Stdio::piped()
        } else {
            Stdio::null()
        });
    let mut child = command.spawn().expect("start symdesk");
    let mut input = child.stdin.take().expect("stdin pipe");
    let stdin_bytes = stdin.as_bytes().to_vec();
    let feeder = thread::spawn(move || {
        // A process that exits without reading stdin closes the pipe; that is
        // not a harness failure.
        let _ = input.write_all(&stdin_bytes);
    });
    let reader = |stream: Option<Box<dyn Read + Send>>| {
        thread::spawn(move || {
            let mut bytes = Vec::new();
            if let Some(mut stream) = stream {
                stream.read_to_end(&mut bytes).expect("read process stream");
            }
            bytes
        })
    };
    let out_reader = reader(
        child
            .stdout
            .take()
            .map(|s| Box::new(s) as Box<dyn Read + Send>),
    );
    let err_reader = reader(
        child
            .stderr
            .take()
            .map(|s| Box::new(s) as Box<dyn Read + Send>),
    );
    let deadline = Instant::now() + timeout;
    let status = loop {
        if let Some(status) = child.try_wait().expect("poll symdesk") {
            break status;
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            panic!("symdesk {args:?} timed out after {timeout:?}: a timed-out case is not a pass");
        }
        thread::sleep(Duration::from_millis(5));
    };
    feeder.join().expect("stdin feeder");
    let (exit_code, signal) = match (status.code(), status.signal()) {
        (Some(code), _) => (code, String::new()),
        (None, Some(signal)) => (-1, signal_name(signal)),
        (None, None) => panic!("process ended without exit code or signal"),
    };
    Outcome {
        exit_code,
        signal,
        stdout: out_reader.join().expect("stdout reader"),
        stderr: err_reader.join().expect("stderr reader"),
    }
}

/// `normalizeStream` of `internal/diff/compare.go`.
fn normalize(mode: &str, value: &[u8], sandbox_root: &str) -> Result<Vec<u8>, String> {
    match mode {
        "bytes" => Ok(value.to_vec()),
        "console_text" => Ok(normalize_console(value, sandbox_root)),
        "json_run_id" => normalize_json_run_id(value),
        "text_run_id" => normalize_text_run_id(value),
        other => Err(format!("unsupported comparison mode {other:?}")),
    }
}

fn replace_bytes(value: &[u8], needle: &[u8], with: &[u8]) -> Vec<u8> {
    if needle.is_empty() {
        return value.to_vec();
    }
    let mut result = Vec::with_capacity(value.len());
    let mut index = 0;
    while index < value.len() {
        if value[index..].starts_with(needle) {
            result.extend_from_slice(with);
            index += needle.len();
        } else {
            result.push(value[index]);
            index += 1;
        }
    }
    result
}

fn normalize_console(value: &[u8], sandbox_root: &str) -> Vec<u8> {
    let value = replace_bytes(value, b"\r\n", b"\n");
    if sandbox_root.is_empty() {
        value
    } else {
        replace_bytes(&value, sandbox_root.as_bytes(), b"<SANDBOX>")
    }
}

fn normalize_json_run_id(value: &[u8]) -> Result<Vec<u8>, String> {
    let document: serde_json::Value =
        serde_json::from_slice(value).map_err(|_| "invalid JSON output".to_owned())?;
    let run_id = document
        .get("run_id")
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default();
    let seconds = run_id
        .strip_prefix("ret-")
        .filter(|seconds| !seconds.is_empty())
        .ok_or("missing retention run ID")?;
    seconds
        .parse::<i64>()
        .map_err(|_| "invalid retention run ID".to_owned())?;
    let needle = format!("\"run_id\":\"{run_id}\"");
    let text = String::from_utf8_lossy(value);
    if text.matches(&needle).count() != 1 {
        return Err("ambiguous retention run ID field".to_owned());
    }
    Ok(replace_bytes(
        value,
        needle.as_bytes(),
        b"\"run_id\":\"ret-<clock>\"",
    ))
}

/// Replaces every `run_id:ret-<digits>`; exactly one occurrence is required.
fn normalize_text_run_id(value: &[u8]) -> Result<Vec<u8>, String> {
    const PREFIX: &[u8] = b"run_id:ret-";
    let mut result = Vec::with_capacity(value.len());
    let mut index = 0;
    let mut found = 0;
    while index < value.len() {
        if value[index..].starts_with(PREFIX) {
            let digits = value[index + PREFIX.len()..]
                .iter()
                .take_while(|byte| byte.is_ascii_digit())
                .count();
            if digits > 0 {
                found += 1;
                result.extend_from_slice(b"run_id:ret-<clock>");
                index += PREFIX.len() + digits;
                continue;
            }
        }
        result.push(value[index]);
        index += 1;
    }
    if found != 1 {
        return Err("missing or ambiguous retention run ID".to_owned());
    }
    Ok(result)
}

/// `sidecarLayout` of `internal/diff/compare.go`, over the files of `root`.
fn sidecar_layout(root: &Path) -> Vec<String> {
    fn collect(root: &Path, dir: &Path, files: &mut Vec<String>) {
        for entry in fs::read_dir(dir).expect("read sandbox directory") {
            let entry = entry.expect("sandbox entry");
            let kind = entry.file_type().expect("sandbox entry type");
            let relative = entry
                .path()
                .strip_prefix(root)
                .expect("below sandbox")
                .to_string_lossy()
                .into_owned();
            if kind.is_dir() {
                collect(root, &entry.path(), files);
            } else if kind.is_file() {
                files.push(relative);
            }
        }
    }
    let mut files = Vec::new();
    collect(root, root, &mut files);
    files.sort();
    let is_hex16 = |value: &str| {
        value.len() == 16
            && value
                .bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    };
    let mut layout = Vec::new();
    for path in files {
        if !(path.ends_with("/sidecar.db")
            || path.ends_with("/sidecar.db-wal")
            || path.ends_with("/sidecar.db-shm")
            || path.ends_with("/metadata.json"))
        {
            continue;
        }
        let mut parts: Vec<String> = path.split('/').map(str::to_owned).collect();
        let mut valid = false;
        for index in 0..parts.len().saturating_sub(2) {
            if parts[index] == "vaults" && is_hex16(&parts[index + 1]) {
                parts[index + 1] = "<vault-hash>".to_owned();
                valid = true;
                break;
            }
        }
        if valid {
            layout.push(parts.join("/"));
        }
    }
    layout
}

fn stream_text(text: &str, encoded: &str, id: &str, name: &str) -> Vec<u8> {
    assert!(
        encoded.is_empty(),
        "{id}: base64 {name} recordings are not replayed"
    );
    text.as_bytes().to_vec()
}

fn compare_stream(id: &str, name: &str, mode: &str, recorded: &[u8], actual: &[u8]) {
    if recorded != actual {
        panic!(
            "{id}: {name} ({mode}) differs from the Go recording\n  recorded {} bytes: {:?}\n  actual   {} bytes: {:?}",
            recorded.len(),
            String::from_utf8_lossy(recorded),
            actual.len(),
            String::from_utf8_lossy(actual),
        );
    }
}

fn replay_case(binary: &str, recorded: &RecordedCase) {
    let case = &recorded.case;
    let expected = &recorded.observation;
    assert!(
        case.binary.is_empty() || case.binary == "symdesk",
        "{}: only symdesk cases are replayed",
        case.id
    );
    assert!(
        !case.compare_files && expected.files.is_none(),
        "{}: filesystem manifests are not replayed",
        case.id
    );
    let stdout_mode = if case.stdout_mode.is_empty() {
        "bytes"
    } else {
        case.stdout_mode.as_str()
    };
    let stderr_mode = if case.stderr_mode.is_empty() {
        "bytes"
    } else {
        case.stderr_mode.as_str()
    };
    assert_eq!(expected.stdout_mode, stdout_mode, "{}", case.id);
    assert_eq!(expected.stderr_mode, stderr_mode, "{}", case.id);

    let root = create_sandbox();
    let home = root.join("home");
    let workspace = root.join("workspace");
    for directory in [
        home.clone(),
        workspace.clone(),
        root.join("tmp"),
        root.join("runtime"),
        home.join(".local/state"),
    ] {
        mkdir_all(&directory);
    }
    let root_text = root.to_string_lossy().into_owned();
    let replacements: Vec<(&str, String)> = vec![
        ("${HOME}", home.to_string_lossy().into_owned()),
        ("${SANDBOX}", root_text.clone()),
        ("${TMPDIR}", root.join("tmp").to_string_lossy().into_owned()),
        ("${WORKSPACE}", workspace.to_string_lossy().into_owned()),
    ];
    setup_files(case, &root, &home, &workspace, &replacements);

    let args: Vec<String> = case
        .args
        .iter()
        .map(|arg| replace(arg, &replacements))
        .collect();
    let dir = if case.working_dir.is_empty() {
        workspace.clone()
    } else {
        safe_path(&workspace, &replace(&case.working_dir, &replacements))
    };
    let env = isolated_env(case, &root, &replacements);
    let timeout = Duration::from_millis(if case.timeout_ms == 0 {
        10_000
    } else {
        case.timeout_ms
    });
    if !case.prepare_args.is_empty() {
        let prepare: Vec<String> = case
            .prepare_args
            .iter()
            .map(|arg| replace(arg, &replacements))
            .collect();
        let outcome = run_process(binary, &prepare, &dir, &env, "", timeout, false);
        assert_eq!(
            (outcome.exit_code, outcome.signal.as_str()),
            (0, ""),
            "{}: prepare process failed",
            case.id
        );
    }
    let outcome = run_process(
        binary,
        &args,
        &dir,
        &env,
        &replace(&case.stdin, &replacements),
        timeout,
        true,
    );

    assert_eq!(
        outcome.exit_code, expected.exit_code,
        "{}: exit code",
        case.id
    );
    assert_eq!(outcome.signal, expected.signal, "{}: signal", case.id);
    for (name, mode, recorded_text, recorded_encoded, bytes) in [
        (
            "stdout",
            stdout_mode,
            &expected.stdout,
            &expected.stdout_base64,
            &outcome.stdout,
        ),
        (
            "stderr",
            stderr_mode,
            &expected.stderr,
            &expected.stderr_base64,
            &outcome.stderr,
        ),
    ] {
        if mode == "ignore" {
            continue;
        }
        let normalized = normalize(mode, bytes, &root_text)
            .unwrap_or_else(|error| panic!("{}: {name}: {error}", case.id));
        let recorded = stream_text(recorded_text, recorded_encoded, &case.id, name);
        compare_stream(&case.id, name, mode, &recorded, &normalized);
    }
    if case.compare_sidecar_layout {
        let layout = sidecar_layout(&root);
        assert!(!layout.is_empty(), "{}: sidecar layout is empty", case.id);
        assert_eq!(
            layout, expected.sidecar_layout,
            "{}: sidecar layout",
            case.id
        );
    } else {
        assert!(expected.sidecar_layout.is_empty(), "{}", case.id);
    }
    fs::remove_dir_all(&root).expect("remove sandbox");
}
