//! Replay harness for the Go-recorded RUST-018 cutover state
//! (`testdata/port/cutover`, recorder: `cmd/symdesk/cutover_state_port_test.go`).
//!
//! The recorder froze a tree that the real Go `symdesk`/`symroom` created at one
//! fixed absolute path, plus the Go outputs of read-only queries. This module
//! unpacks that tree at the same path (the sidecar and retrieval databases embed
//! absolute vault paths), runs commands with the recorded environment and
//! compares trees. No Go is involved. Absence of the fixture for the host OS is
//! a failure, never a skip.
//!
//! Included with `#[path]` by the `symdesk-cli` and `symroom-cli` integration
//! tests, which both use the one fixed root: a lock file beside the root
//! serializes them across processes.
#![allow(dead_code)]

use std::{
    collections::BTreeMap,
    fs::{self, File},
    io::Read,
    path::{Path, PathBuf},
    process::Command,
    time::{Duration, SystemTime},
};

use flate2::read::GzDecoder;
use serde::Deserialize;
use sha2::{Digest, Sha256};

#[path = "process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

pub const FIXED_ROOT: &str = "/tmp/symdesk-cutover-v1";

#[derive(Debug, Deserialize)]
pub struct Manifest {
    pub schema_version: u32,
    pub generated_on: Platform,
    pub root: String,
    pub env: BTreeMap<String, String>,
    pub vars: BTreeMap<String, String>,
    pub states: Vec<State>,
}

#[derive(Debug, Deserialize)]
pub struct Platform {
    pub goos: String,
    pub goarch: String,
    pub go_version: String,
}

#[derive(Debug, Deserialize)]
pub struct State {
    pub name: String,
    pub archive: String,
    pub archive_sha256: String,
    pub files: Vec<FileEntry>,
    pub after_reads: Vec<FileEntry>,
    pub queries: Vec<Query>,
    pub writes: Vec<Write>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
pub struct FileEntry {
    pub path: String,
    pub kind: String,
    pub mode: String,
    pub size: i64,
    #[serde(default)]
    pub sha256: String,
    #[serde(default)]
    pub volatile: String,
}

#[derive(Debug, Deserialize)]
pub struct Query {
    pub name: String,
    pub tool: String,
    pub cwd: String,
    pub args: Vec<String>,
    pub exit_code: i32,
    pub stdout: String,
    pub stderr: String,
}

#[derive(Debug, Deserialize)]
pub struct Write {
    pub name: String,
    pub steps: Vec<Step>,
}

#[derive(Debug, Deserialize)]
pub struct Step {
    pub tool: String,
    pub cwd: String,
    pub args: Vec<String>,
    #[serde(default)]
    pub expect_substr: Vec<String>,
}

pub fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/cutover")
}

/// The Go OS name the recorder used for this host.
pub fn host_goos() -> &'static str {
    match std::env::consts::OS {
        "macos" => "darwin",
        "linux" => "linux",
        other => panic!("no cutover-state fixture is recorded for {other}"),
    }
}

pub fn load() -> Manifest {
    let path = fixture_dir().join(format!("state-v1-{}.json", host_goos()));
    let bytes = fs::read(&path).unwrap_or_else(|error| {
        panic!(
            "cutover-state fixture {} is missing ({error}); this is a failure, not a skip",
            path.display()
        )
    });
    let manifest: Manifest = serde_json::from_slice(&bytes).expect("decode cutover manifest");
    assert_eq!(manifest.schema_version, 1);
    assert_eq!(manifest.generated_on.goos, host_goos());
    assert!(
        !manifest.generated_on.goarch.is_empty() && !manifest.generated_on.go_version.is_empty()
    );
    manifest
}

/// Canonical spelling of the fixed root on this host (`/tmp` is a symlink on
/// macOS), the form the Go recorder stored in every database.
pub fn canonical_root() -> PathBuf {
    let path = Path::new(FIXED_ROOT);
    let parent = fs::canonicalize(path.parent().expect("root parent")).expect("canonical /tmp");
    parent.join(path.file_name().expect("root name"))
}

pub struct Root {
    pub path: PathBuf,
    _lock: File,
}

/// Takes the cross-process lock for the fixed root; the guard owns it until
/// dropped and removes the tree when it goes away.
pub fn lock_root(manifest: &Manifest) -> Root {
    let path = canonical_root();
    assert_eq!(
        Path::new(&manifest.root),
        path,
        "fixture root must equal the canonical fixed root on this host"
    );
    let lock_path = path.with_file_name("symdesk-cutover-v1.lock");
    let lock = File::options()
        .create(true)
        .truncate(false)
        .write(true)
        .open(&lock_path)
        .expect("open cutover lock");
    lock.lock().expect("lock cutover root");
    Root { path, _lock: lock }
}

impl Drop for Root {
    fn drop(&mut self) {
        make_writable(&self.path);
        let _ = fs::remove_dir_all(&self.path);
    }
}

fn make_writable(path: &Path) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(entries) = fs::read_dir(path) {
            let _ = fs::set_permissions(path, fs::Permissions::from_mode(0o700));
            for entry in entries.flatten() {
                if entry.file_type().is_ok_and(|kind| kind.is_dir()) {
                    make_writable(&entry.path());
                }
            }
        }
    }
    #[cfg(not(unix))]
    let _ = path;
}

fn read_u32(cursor: &mut &[u8]) -> u32 {
    let (head, rest) = cursor.split_at(4);
    *cursor = rest;
    u32::from_le_bytes(head.try_into().expect("u32"))
}

fn read_u64(cursor: &mut &[u8]) -> u64 {
    let (head, rest) = cursor.split_at(8);
    *cursor = rest;
    u64::from_le_bytes(head.try_into().expect("u64"))
}

impl Root {
    /// Replaces the root with the archive's tree (modes and mtimes restored).
    pub fn unpack(&self, state: &State) {
        self.unpack_matching(state, None);
    }

    /// Replaces only the subtree `prefix` (for example `vault`) with the
    /// archive's version of it, leaving the rest of the root alone.
    pub fn unpack_prefix(&self, state: &State, prefix: &str) {
        self.unpack_matching(state, Some(prefix));
    }

    fn unpack_matching(&self, state: &State, prefix: Option<&str>) {
        let archive = fs::read(fixture_dir().join(&state.archive)).expect("read archive");
        assert_eq!(
            hex(&Sha256::digest(&archive)),
            state.archive_sha256,
            "archive digest"
        );
        match prefix {
            None => {
                make_writable(&self.path);
                let _ = fs::remove_dir_all(&self.path);
                fs::create_dir_all(&self.path).expect("create root");
            }
            Some(prefix) => {
                make_writable(&self.path.join(prefix));
                let _ = fs::remove_dir_all(self.path.join(prefix));
            }
        }
        let mut raw = Vec::new();
        GzDecoder::new(archive.as_slice())
            .read_to_end(&mut raw)
            .expect("gunzip archive");
        assert_eq!(&raw[..4], b"CXA1", "archive magic");
        let mut cursor = &raw[4..];
        let mut directories: Vec<(PathBuf, u32)> = Vec::new();
        while !cursor.is_empty() {
            let kind = cursor[0];
            cursor = &cursor[1..];
            let length = read_u32(&mut cursor) as usize;
            let name = std::str::from_utf8(&cursor[..length]).expect("utf-8 entry name");
            cursor = &cursor[length..];
            let mode = read_u32(&mut cursor);
            let mtime = read_u64(&mut cursor);
            let size = usize::try_from(read_u64(&mut cursor)).expect("entry size");
            assert!(
                !name.starts_with('/') && !name.split('/').any(|part| part == ".."),
                "archive entry escapes the root: {name}"
            );
            let target = self.path.join(name);
            let wanted = prefix.is_none_or(|prefix| {
                name == prefix
                    || name
                        .strip_prefix(prefix)
                        .is_some_and(|rest| rest.starts_with('/'))
            });
            if !wanted {
                if kind == b'f' {
                    cursor = &cursor[size..];
                }
                continue;
            }
            match kind {
                b'd' => {
                    fs::create_dir_all(&target).expect("create directory");
                    directories.push((target, mode));
                }
                b'f' => {
                    let data = &cursor[..size];
                    cursor = &cursor[size..];
                    fs::create_dir_all(target.parent().expect("parent")).expect("create parent");
                    fs::write(&target, data).expect("write file");
                    set_mode(&target, mode);
                    let file = File::options()
                        .write(true)
                        .open(&target)
                        .expect("reopen file");
                    file.set_modified(
                        SystemTime::UNIX_EPOCH
                            + Duration::from_nanos(i64::cast_unsigned(mtime.cast_signed())),
                    )
                    .expect("restore mtime");
                }
                other => panic!("unknown archive record kind {other}"),
            }
        }
        for (path, mode) in directories.into_iter().rev() {
            set_mode(&path, mode);
        }
    }

    /// Mirror of the recorder's scan: SQLite `-shm` files and zero-length `-wal`
    /// files are not state, and the
    /// per-vault `metadata.json` carries a `last_used` touch that every command
    /// rewrites, so only that one value is normalized before hashing.
    pub fn scan(&self) -> Vec<FileEntry> {
        let mut files = Vec::new();
        scan_into(&self.path, &self.path, &mut files);
        files.sort_by(|left, right| left.path.cmp(&right.path));
        files
    }

    pub fn environment(&self, manifest: &Manifest) -> Vec<(String, String)> {
        manifest
            .env
            .iter()
            .map(|(key, value)| (key.clone(), self.expand(value, manifest)))
            .collect()
    }

    pub fn expand(&self, text: &str, manifest: &Manifest) -> String {
        let mut text = text.replace("$ROOT", &self.path.to_string_lossy());
        let mut keys: Vec<&String> = manifest.vars.keys().collect();
        keys.sort_by_key(|key| std::cmp::Reverse(key.len()));
        for key in keys {
            text = text.replace(&format!("${key}"), &manifest.vars[key]);
        }
        text
    }

    /// Runs `binary` with the recorded environment (and nothing else).
    pub fn run(
        &self,
        manifest: &Manifest,
        binary: &str,
        cwd: &str,
        args: &[String],
        extra_env: &[(&str, &str)],
        removed_env: &[&str],
    ) -> Run {
        let mut command = Command::new(binary);
        command.current_dir(self.path.join(cwd)).isolated_env();
        for (key, value) in self.environment(manifest) {
            command.env(key, value);
        }
        for (key, value) in extra_env {
            command.env(key, value);
        }
        for key in removed_env {
            command.env_remove(key);
        }
        let expanded: Vec<String> = args.iter().map(|arg| self.expand(arg, manifest)).collect();
        let output = command.args(&expanded).output().expect("run Rust process");
        Run {
            exit: output.status.code().unwrap_or(-1),
            stdout: String::from_utf8(output.stdout).expect("UTF-8 stdout"),
            stderr: String::from_utf8(output.stderr).expect("UTF-8 stderr"),
        }
    }
}

pub struct Run {
    pub exit: i32,
    pub stdout: String,
    pub stderr: String,
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("restore mode");
}

#[cfg(not(unix))]
fn set_mode(_: &Path, _: u32) {}

#[cfg(unix)]
fn mode_of(metadata: &fs::Metadata) -> String {
    use std::os::unix::fs::PermissionsExt;
    format!("{:04o}", metadata.permissions().mode() & 0o777)
}

#[cfg(not(unix))]
fn mode_of(_: &fs::Metadata) -> String {
    String::from("0000")
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write as _;
    let mut text = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        write!(text, "{byte:02x}").expect("write to string");
    }
    text
}

fn scan_into(root: &Path, directory: &Path, files: &mut Vec<FileEntry>) {
    let mut entries: Vec<_> = fs::read_dir(directory)
        .expect("read directory")
        .map(|entry| entry.expect("directory entry"))
        .collect();
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        let path = entry.path();
        let metadata = fs::symlink_metadata(&path).expect("entry metadata");
        let relative = path
            .strip_prefix(root)
            .expect("relative path")
            .to_string_lossy()
            .replace('\\', "/");
        let mut item = FileEntry {
            path: relative.clone(),
            kind: String::new(),
            mode: mode_of(&metadata),
            size: 0,
            sha256: String::new(),
            volatile: String::new(),
        };
        if metadata.is_dir() {
            item.kind = "d".to_owned();
            files.push(item);
            scan_into(root, &path, files);
            continue;
        }
        assert!(metadata.is_file(), "unsupported file type at {relative}");
        if relative.ends_with("-shm") || (relative.ends_with("-wal") && metadata.len() == 0) {
            continue;
        }
        item.kind = "f".to_owned();
        let mut data = fs::read(&path).expect("read file");
        if path.file_name().is_some_and(|name| name == "metadata.json") {
            data = normalize_last_used(&data);
            "last_used".clone_into(&mut item.volatile);
        }
        item.size = i64::try_from(data.len()).expect("file size");
        item.sha256 = hex(&Sha256::digest(&data));
        files.push(item);
    }
}

fn normalize_last_used(data: &[u8]) -> Vec<u8> {
    regex_last_used()
        .replace_all(&String::from_utf8_lossy(data), r#""last_used":"<T>""#)
        .into_owned()
        .into_bytes()
}

fn regex_last_used() -> &'static regex::Regex {
    static CELL: std::sync::OnceLock<regex::Regex> = std::sync::OnceLock::new();
    CELL.get_or_init(|| regex::Regex::new(r#""last_used":\s*"[^"]*""#).expect("last_used regex"))
}

/// Problems between two scans; empty means byte-identical state.
pub fn diff(expected: &[FileEntry], actual: &[FileEntry]) -> Vec<String> {
    let want: BTreeMap<&str, &FileEntry> = expected.iter().map(|e| (e.path.as_str(), e)).collect();
    let got: BTreeMap<&str, &FileEntry> = actual.iter().map(|e| (e.path.as_str(), e)).collect();
    let mut problems = Vec::new();
    for (path, item) in &want {
        // Products of the lazy per-vault retrieval-index migration that Go and
        // Rust both perform on the first read (their SQLite snapshot bytes
        // differ; the replay compares the rows). Anything else must be
        // byte-identical.
        if item.volatile == "migration-product" {
            if got.get(path).is_none_or(|other| other.kind != "f") {
                problems.push(format!("missing migration product: {path}"));
            }
            continue;
        }
        match got.get(path) {
            None => problems.push(format!("missing: {path}")),
            Some(other) => {
                if other.kind != item.kind
                    || other.mode != item.mode
                    || other.size != item.size
                    || other.sha256 != item.sha256
                {
                    problems.push(format!(
                        "changed: {path} (want {}/{}/{} got {}/{}/{})",
                        item.mode, item.size, item.sha256, other.mode, other.size, other.sha256
                    ));
                }
            }
        }
    }
    for path in got.keys() {
        if !want.contains_key(path) {
            problems.push(format!("unexpected: {path}"));
        }
    }
    problems.sort();
    problems
}

/// Mirror of the recorder's `cutoverNormalize`: heap addresses printed by Go's
/// `%v`, slog timestamps and the `last_used` touch.
pub fn normalize(text: &str) -> String {
    use std::sync::OnceLock;
    static PATTERNS: OnceLock<[regex::Regex; 3]> = OnceLock::new();
    let [pointer, time, last_used] = PATTERNS.get_or_init(|| {
        [
            regex::Regex::new(r"0x[0-9a-f]+").expect("pointer regex"),
            regex::Regex::new(r"\btime=\d{4}-\d{2}-\d{2}T[0-9:.]+Z").expect("time regex"),
            regex::Regex::new(r#""last_used":\s*"[^"]*""#).expect("last_used regex"),
        ]
    });
    let text = pointer.replace_all(text, regex::NoExpand("$PTR"));
    let text = time.replace_all(&text, regex::NoExpand("time=<T>"));
    last_used
        .replace_all(&text, regex::NoExpand(r#""last_used":"<T>""#))
        .into_owned()
}
