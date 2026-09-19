#![deny(unsafe_code)]

//! Replays the Go-owned filesystem harness for the vault write stack
//! (`internal/vault/port_writefs_contract_test.go`, fixture
//! `testdata/port/vault/filesystem-writes.json`).
//!
//! Every case is executed against the Rust port and compared byte-for-byte on
//! content, permission bits, content hashes, the resulting Markdown file set,
//! the walker output, the error stage and the temporary-file leftovers.

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use symdesk_vault::{sha256, walk_all, write_atomic};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    temp_file_pattern: TempPattern,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct TempPattern {
    prefix: String,
    suffix: String,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    operation: String,
    platform: String,
    setup: Setup,
    args: Args,
    before: State,
    after: State,
    error_class: String,
    error_prefix: String,
    target: Option<FileRecord>,
    target_kind: String,
    parse: Option<ParseRecord>,
    #[serde(default)]
    walk_paths: Option<Vec<String>>,
    invariant: Option<Invariant>,
}

#[derive(Deserialize)]
struct Setup {
    dir_mode: u32,
}

#[derive(Deserialize)]
struct Args {
    path: String,
    #[serde(default)]
    data_base64: String,
    #[serde(default)]
    data_sha256: String,
    #[serde(default)]
    data_length: usize,
    #[serde(default)]
    data_repeat: String,
    existing_mode: Option<u32>,
}

#[derive(Deserialize)]
struct State {
    markdown: Vec<FileRecord>,
    other_file_count: usize,
    other_dir_count: usize,
    temp_leftovers: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct FileRecord {
    path: String,
    mode: Option<u32>,
    size: i64,
    sha256: String,
    #[serde(default)]
    content_base64: String,
}

#[derive(Debug, Deserialize)]
struct ParseRecord {
    title: String,
    body: String,
    sha256: String,
    error: String,
}

impl ParseRecord {
    fn render(&self) -> String {
        format!(
            "title={} body={} sha={} error={}",
            self.title, self.body, self.sha256, self.error
        )
    }
}

#[derive(Deserialize)]
struct Invariant {
    trials: usize,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/vault/filesystem-writes.json"
    ))
    .expect("decode filesystem write fixture")
}

#[test]
fn generated_go_atomic_writes_match_bytes_modes_hashes_and_file_sets() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert!(!fixture.oracle.commit.is_empty());
    assert!(!fixture.oracle.release.is_empty());
    assert!(!fixture.source_hashes.is_empty());
    assert_eq!(fixture.temp_file_pattern.prefix, ".symdesk-frontmatter-");
    assert_eq!(fixture.temp_file_pattern.suffix, ".tmp");
    assert!(fixture.cases.len() >= 12, "fixture lost cases");

    let mut mismatches = Vec::new();
    let mut executed = 0usize;
    for case in &fixture.cases {
        if case.operation == "kill_writer" {
            if !cfg!(unix) {
                continue;
            }
            mismatches.extend(replay_interruption(case));
            executed += 1;
            continue;
        }
        if case.platform == "unix" && !cfg!(unix) {
            continue;
        }
        if case.operation == "atomic_write_after_crash" {
            mismatches.extend(replay_crash_recovery(case));
            executed += 1;
            continue;
        }
        mismatches.extend(replay_case(case));
        executed += 1;
    }
    // Every case that can run on this platform must run: the fixture pins the
    // count, and silently skipping cases would turn parity into a no-op.
    let runnable = fixture
        .cases
        .iter()
        .filter(|case| case.platform != "unix" || cfg!(unix))
        .filter(|case| case.operation != "kill_writer" || cfg!(unix))
        .count();
    assert_eq!(executed, runnable, "fixture cases were skipped");
    assert!(
        mismatches.is_empty(),
        "vault write filesystem parity mismatches:\n{}",
        mismatches.join("\n")
    );
}

/// Replays the crash-recovery case: the previous content is still readable, the
/// leftover temporary file is invisible to the walker, and the next atomic
/// write replaces the target without touching the stale temporary file.
fn replay_crash_recovery(case: &Case) -> Vec<String> {
    let root = OwnedTempDir::new(&case.id);
    let target = root.path.join("note.md");
    let previous = &case.before.markdown[0];
    fs::write(
        &target,
        base64_decode(&previous.content_base64).expect("decode previous bytes"),
    )
    .expect("seed crash target");
    set_mode(&target, previous.mode);
    for leftover in &case.before.temp_leftovers {
        let path = root.path.join(path_from_slash(leftover));
        fs::write(&path, b"---\ntitle: partial").expect("seed crash temporary file");
        set_mode(&path, Some(0o600));
    }

    let mut mismatches = Vec::new();
    compare_state(
        &mut mismatches,
        &case.id,
        "before",
        &case.before,
        &state_of(&root.path),
    );
    if let Some(expected) = &case.parse {
        compare(
            &mut mismatches,
            &case.id,
            "parse before recovery",
            &expected.render(),
            &parse_record(&target).render(),
        );
    }
    if let Some(expected) = &case.walk_paths {
        compare(
            &mut mismatches,
            &case.id,
            "walk paths",
            &format!("{expected:?}"),
            &format!("{:?}", walked_paths(&root.path)),
        );
    }

    let result = write_atomic(&target, &case_bytes(case));
    let (class, prefix) = classify(result.as_ref().err());
    compare(
        &mut mismatches,
        &case.id,
        "error_class",
        &case.error_class,
        class,
    );
    compare(
        &mut mismatches,
        &case.id,
        "error_prefix",
        &case.error_prefix,
        prefix,
    );
    compare_state(
        &mut mismatches,
        &case.id,
        "after",
        &case.after,
        &state_of(&root.path),
    );
    compare(
        &mut mismatches,
        &case.id,
        "target record",
        &render_target(case.target.as_ref()),
        &render_target(file_record(&root.path, "note.md").as_ref()),
    );
    mismatches
}

fn walked_paths(root: &Path) -> Vec<String> {
    match walk_all(root) {
        Ok(entries) => {
            let mut paths: Vec<String> = entries
                .iter()
                .map(|entry| relative(root, &entry.path))
                .collect();
            paths.sort();
            paths
        }
        Err(error) => vec![format!("walk failed: {error}")],
    }
}

fn replay_case(case: &Case) -> Vec<String> {
    let root = OwnedTempDir::new(&case.id);
    prepare(&root.path, case);
    let before = state_of(&root.path);
    let target = root.path.join(path_from_slash(&case.args.path));

    let result = write_atomic(&target, &case_bytes(case));
    let after = state_of(&root.path);

    let mut mismatches = Vec::new();
    let (class, prefix) = classify(result.as_ref().err());
    compare(
        &mut mismatches,
        &case.id,
        "error_class",
        &case.error_class,
        class,
    );
    compare(
        &mut mismatches,
        &case.id,
        "error_prefix",
        &case.error_prefix,
        prefix,
    );
    compare_state(&mut mismatches, &case.id, "before", &case.before, &before);
    compare_state(&mut mismatches, &case.id, "after", &case.after, &after);

    let actual_target = file_record(&root.path, &case.args.path);
    compare(
        &mut mismatches,
        &case.id,
        "target record",
        &render_target(case.target.as_ref()),
        &render_target(actual_target.as_ref()),
    );
    if case.target.is_none() && case.target_kind == "file" && actual_target.is_some() {
        mismatches.push(format!("{}: unexpected target file", case.id));
    }

    if let Some(parse) = &case.parse {
        let actual = parse_record(&target);
        compare(
            &mut mismatches,
            &case.id,
            "parse",
            &parse.render(),
            &actual.render(),
        );
    }

    if let Some(expected) = &case.walk_paths {
        compare(
            &mut mismatches,
            &case.id,
            "walk paths",
            &format!("{expected:?}"),
            &format!("{:?}", walked_paths(&root.path)),
        );
    }
    mismatches
}

/// Replays the interruption invariant: a writer process is killed after it has
/// completed one full write, and the target must never hold a partial document.
fn replay_interruption(case: &Case) -> Vec<String> {
    let mut mismatches = Vec::new();
    let Some(invariant) = &case.invariant else {
        return vec![format!("{}: missing invariant", case.id)];
    };
    let payload = case_bytes(case);
    let old = base64_decode(
        &case
            .before
            .markdown
            .first()
            .expect("interruption case records the previous bytes")
            .content_base64,
    )
    .expect("decode previous bytes");

    let mut torn = 0usize;
    for trial in 0..invariant.trials {
        let root = OwnedTempDir::new(&format!("{}-{trial}", case.id));
        let target = root.path.join("note.md");
        fs::write(&target, &old).expect("seed interrupted target");
        set_mode(&target, Some(0o640));
        let ready = root.path.join(".symdesk-frontmatter-ready.tmp");
        let payload_file = root.path.join(".symdesk-frontmatter-payload.tmp");
        fs::write(&payload_file, &payload).expect("write interrupt payload");

        let mut child = Command::new(std::env::current_exe().expect("test binary path"))
            .args(["--exact", "atomic_write_loop", "--ignored", "--nocapture"])
            .env("SYMDESK_PORT_INTERRUPT_WRITER", "1")
            .env("SYMDESK_PORT_INTERRUPT_TARGET", &target)
            .env("SYMDESK_PORT_INTERRUPT_READY", &ready)
            .env("SYMDESK_PORT_INTERRUPT_PAYLOAD_FILE", &payload_file)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn interruption writer");

        let deadline = Instant::now() + Duration::from_secs(60);
        while !ready.exists() {
            if Instant::now() > deadline {
                let _ = child.kill();
                let _ = child.wait();
                return vec![format!("{}: writer never completed a first write", case.id)];
            }
            thread::sleep(Duration::from_millis(2));
        }
        let _ = child.kill();
        let _ = child.wait();

        let actual = fs::read(&target).expect("read interrupted target");
        if actual != old && actual != payload {
            torn += 1;
        }
    }
    if torn != 0 {
        mismatches.push(format!(
            "{}: interruption exposed {torn} torn target states",
            case.id
        ));
    }
    mismatches
}

/// Child-process body for [`replay_interruption`]. Inert unless the parent sets
/// `SYMDESK_PORT_INTERRUPT_WRITER=1`.
#[test]
#[ignore = "child process body for the interruption replay"]
fn atomic_write_loop() {
    if std::env::var("SYMDESK_PORT_INTERRUPT_WRITER").as_deref() != Ok("1") {
        return;
    }
    let target = std::env::var("SYMDESK_PORT_INTERRUPT_TARGET").expect("target");
    let ready = std::env::var("SYMDESK_PORT_INTERRUPT_READY").expect("ready marker");
    let payload_file = std::env::var("SYMDESK_PORT_INTERRUPT_PAYLOAD_FILE").expect("payload file");
    let payload = fs::read(&payload_file).expect("read payload");
    let mut first = true;
    loop {
        let result = write_atomic(Path::new(&target), &payload);
        assert!(result.is_ok(), "writer failed: {result:?}");
        if first {
            first = false;
            fs::write(&ready, b"done").expect("write ready marker");
        }
    }
}

fn prepare(root: &Path, case: &Case) {
    if case.id == "atomic-parent-read-only" {
        let dir = root.join("locked");
        fs::create_dir_all(&dir).expect("create locked directory");
        set_mode(&dir, Some(0o500));
        return;
    }
    set_mode(root, Some(case.setup.dir_mode & 0o777));
    if case.target_kind == "directory" {
        fs::create_dir_all(root.join("note.md")).expect("seed directory target");
        set_mode(&root.join("note.md"), Some(0o750));
    } else if let Some(mode) = case.args.existing_mode {
        fs::write(
            root.join("note.md"),
            base64_decode(&case.before.markdown[0].content_base64).expect("decode previous bytes"),
        )
        .expect("seed existing target");
        set_mode(&root.join("note.md"), Some(mode));
    }
    for leftover in &case.before.temp_leftovers {
        let path = root.join(path_from_slash(leftover));
        fs::write(&path, b"stale partial bytes").expect("seed stale temporary file");
        set_mode(&path, Some(0o600));
    }
}

fn case_bytes(case: &Case) -> Vec<u8> {
    if !case.args.data_base64.is_empty() {
        return base64_decode(&case.args.data_base64).expect("decode payload");
    }
    if !case.args.data_repeat.is_empty() {
        let unit = case.args.data_repeat.as_bytes();
        let payload = unit.repeat(case.args.data_length / unit.len());
        assert_eq!(
            hex(&sha256::digest(&payload)),
            case.args.data_sha256,
            "{}: rebuilt payload differs from the recorded digest",
            case.id
        );
        return payload;
    }
    Vec::new()
}

fn state_of(root: &Path) -> State {
    let mut state = State {
        markdown: Vec::new(),
        other_file_count: 0,
        other_dir_count: 0,
        temp_leftovers: Vec::new(),
    };
    collect(root, root, &mut state);
    state
        .markdown
        .sort_by(|left, right| left.path.cmp(&right.path));
    state.temp_leftovers.sort();
    state
}

fn collect(root: &Path, current: &Path, state: &mut State) {
    let Ok(entries) = fs::read_dir(current) else {
        return;
    };
    for entry in entries.filter_map(Result::ok) {
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().into_owned();
        let relative = relative(root, &path);
        if entry.file_type().map(|kind| kind.is_dir()).unwrap_or(false) {
            state.other_dir_count += 1;
            collect(root, &path, state);
            continue;
        }
        if name.starts_with(".symdesk-frontmatter-") && name.ends_with(".tmp") {
            state.temp_leftovers.push(relative);
            continue;
        }
        if !relative.ends_with(".md") {
            state.other_file_count += 1;
            continue;
        }
        if let Some(record) = file_record(root, &relative) {
            state.markdown.push(record);
        }
    }
}

fn file_record(root: &Path, relative: &str) -> Option<FileRecord> {
    let path = root.join(path_from_slash(relative));
    let data = fs::read(&path).ok()?;
    let mut record = FileRecord {
        path: relative.to_owned(),
        mode: mode_of(&path),
        size: i64::try_from(data.len()).unwrap_or(i64::MAX),
        sha256: hex(&sha256::digest(&data)),
        content_base64: String::new(),
    };
    if data.len() <= 2048 {
        record.content_base64 = base64_encode(&data);
    }
    Some(record)
}

fn parse_record(path: &Path) -> ParseRecord {
    let data = fs::read(path).expect("read parsed file");
    match symdesk_vault::parse_bytes(&path.to_string_lossy(), &data) {
        Ok(document) => ParseRecord {
            title: document.title.clone(),
            sha256: hex(&sha256::digest(document.body.as_bytes())),
            body: document.body.clone(),
            error: String::new(),
        },
        Err(error) => ParseRecord {
            title: String::new(),
            body: String::new(),
            sha256: String::new(),
            error: error.to_string(),
        },
    }
}

fn compare_state(
    mismatches: &mut Vec<String>,
    id: &str,
    label: &str,
    expected: &State,
    actual: &State,
) {
    compare(
        mismatches,
        id,
        &format!("{label} markdown"),
        &render_markdown(&expected.markdown),
        &render_markdown(&actual.markdown),
    );
    compare(
        mismatches,
        id,
        &format!("{label} other_file_count"),
        &expected.other_file_count.to_string(),
        &actual.other_file_count.to_string(),
    );
    compare(
        mismatches,
        id,
        &format!("{label} other_dir_count"),
        &expected.other_dir_count.to_string(),
        &actual.other_dir_count.to_string(),
    );
    compare(
        mismatches,
        id,
        &format!("{label} temp_leftovers"),
        &format!("{:?}", expected.temp_leftovers),
        &format!("{:?}", actual.temp_leftovers),
    );
}

fn render_target(record: Option<&FileRecord>) -> String {
    match record {
        None => "none".to_owned(),
        Some(record) => format!(
            "{} {} size={} sha={} bytes={}",
            record.path,
            render_mode(record.mode),
            record.size,
            record.sha256,
            record.content_base64
        ),
    }
}

fn render_markdown(records: &[FileRecord]) -> String {
    records
        .iter()
        .map(|record| {
            format!(
                "{} {} size={} sha={} bytes={}",
                record.path,
                render_mode(record.mode),
                record.size,
                record.sha256,
                record.content_base64
            )
        })
        .collect::<Vec<_>>()
        .join(" | ")
}

/// Permission bits are only part of the contract where the platform reports
/// them; on Windows both sides render the same empty string.
fn render_mode(mode: Option<u32>) -> String {
    if cfg!(unix) {
        format!("mode={mode:?}")
    } else {
        String::new()
    }
}

fn compare(mismatches: &mut Vec<String>, id: &str, label: &str, expected: &str, actual: &str) {
    if expected != actual {
        mismatches.push(format!(
            "{id} {label}: expected {expected:?}, got {actual:?}"
        ));
    }
}

fn classify(error: Option<&symdesk_vault::MutationError>) -> (&'static str, &'static str) {
    use symdesk_vault::MutationError::{
        Marshal, Read, TempCreate, TempRename, TempSync, TempWrite, Write,
    };
    match error {
        None => ("", ""),
        Some(TempCreate { .. }) => ("create_temp", "create temp file: "),
        Some(TempWrite { .. }) => ("write_temp", "write temp file: "),
        Some(TempSync { .. }) => ("sync_temp", "sync temp file: "),
        Some(TempRename { .. }) => ("rename_temp", "rename temp file: "),
        Some(Read { .. }) => ("filesystem", "read file: "),
        Some(Write { .. }) => ("write", "write file: "),
        Some(Marshal { .. }) => ("marshal", "marshal value: "),
    }
}

fn relative(root: &Path, path: &Path) -> String {
    path.strip_prefix(root)
        .unwrap_or(path)
        .to_string_lossy()
        .replace('\\', "/")
}

fn path_from_slash(value: &str) -> PathBuf {
    PathBuf::from(value.replace('/', std::path::MAIN_SEPARATOR_STR))
}

fn mode_of(path: &Path) -> Option<u32> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::metadata(path)
            .ok()
            .map(|metadata| metadata.permissions().mode() & 0o777)
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        None
    }
}

fn set_mode(path: &Path, mode: Option<u32>) {
    #[cfg(unix)]
    if let Some(mode) = mode {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("set mode");
    }
    #[cfg(not(unix))]
    {
        let _ = (path, mode);
    }
}

fn hex(bytes: &[u8]) -> String {
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        output.push_str(&format!("{byte:02x}"));
    }
    output
}

struct OwnedTempDir {
    path: PathBuf,
}

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

impl OwnedTempDir {
    fn new(id: &str) -> Self {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let counter = TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "symdesk-filesystem-write-{id}-{}-{stamp}-{counter}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create private case directory");
        Self { path }
    }
}

impl Drop for OwnedTempDir {
    fn drop(&mut self) {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = fs::set_permissions(&self.path, fs::Permissions::from_mode(0o750));
            if let Ok(entries) = fs::read_dir(&self.path) {
                for entry in entries.filter_map(Result::ok) {
                    let _ = fs::set_permissions(entry.path(), fs::Permissions::from_mode(0o750));
                }
            }
        }
        let _ = fs::remove_dir_all(&self.path);
    }
}

mod base64 {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

    fn value(byte: u8) -> Option<u8> {
        match byte {
            b'A'..=b'Z' => Some(byte - b'A'),
            b'a'..=b'z' => Some(byte - b'a' + 26),
            b'0'..=b'9' => Some(byte - b'0' + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    }

    pub(super) fn encode(input: &[u8]) -> String {
        let mut output = String::new();
        for chunk in input.chunks(3) {
            let first = chunk[0];
            output.push(TABLE[(first >> 2) as usize] as char);
            let second = chunk.get(1).copied();
            output.push(TABLE[((first & 3) << 4 | second.unwrap_or(0) >> 4) as usize] as char);
            if let Some(second) = second {
                output.push(
                    TABLE[((second & 15) << 2 | chunk.get(2).copied().unwrap_or(0) >> 6) as usize]
                        as char,
                );
            } else {
                output.push('=');
            }
            if let Some(third) = chunk.get(2) {
                output.push(TABLE[(third & 63) as usize] as char);
            } else {
                output.push('=');
            }
        }
        output
    }

    pub(super) fn decode(input: &str) -> Result<Vec<u8>, ()> {
        let bytes = input.as_bytes();
        if bytes.is_empty() {
            return Ok(Vec::new());
        }
        let (chunks, remainder) = bytes.as_chunks::<4>();
        if !remainder.is_empty() {
            return Err(());
        }
        let mut output = Vec::with_capacity(bytes.len() / 4 * 3);
        for chunk in chunks {
            let a = value(chunk[0]).ok_or(())?;
            let b = value(chunk[1]).ok_or(())?;
            output.push(a << 2 | b >> 4);
            if chunk[2] != b'=' {
                let c = value(chunk[2]).ok_or(())?;
                output.push(b << 4 | c >> 2);
                if chunk[3] != b'=' {
                    let d = value(chunk[3]).ok_or(())?;
                    output.push(c << 6 | d);
                }
            }
        }
        Ok(output)
    }
}

use base64::{decode as base64_decode, encode as base64_encode};
