#![deny(unsafe_code)]

//! Replays `testdata/port/room/store-cli.json` (ROOMCLI-002): the identity
//! store commands with arbitrary names, store states and umasks, and `index
//! rebuild` success, as the real Go `symroom` process behaved. Each case runs in
//! a disposable root; the comparison covers exit code, both streams and the
//! whole resulting tree (kind, mode, content, link target, whether a file
//! changed). Random key material of freshly created identities is checked for
//! consistency and masked on both sides.

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    sync::atomic::{AtomicU64, Ordering},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Op {
    op: String,
    path: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    target: String,
}

#[derive(Deserialize, PartialEq, Eq, Debug)]
struct Entry {
    path: String,
    kind: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    target: String,
    #[serde(default)]
    fresh: bool,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    #[serde(default)]
    unix_only: bool,
    args: Vec<String>,
    #[serde(default)]
    ops: Vec<Op>,
    #[serde(default)]
    xdg: String,
    #[serde(default)]
    home: String,
    #[serde(default)]
    umask: String,
    #[serde(default)]
    room_env: String,
    #[serde(default)]
    cwd: String,
    #[serde(default)]
    needs_non_root: bool,
    #[serde(default)]
    host: String,
    #[serde(default)]
    identity_key: bool,
    #[serde(default)]
    key_hex: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    #[serde(default)]
    stderr_is_prefix: bool,
    tree: Vec<Entry>,
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "symroom-store-{}-{}",
            std::process::id(),
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::SeqCst)
        ));
        fs::create_dir_all(&path).expect("create temp dir");
        Self(path)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        restore_dir_modes(&self.0);
        let _ = fs::remove_dir_all(&self.0);
    }
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("chmod");
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) {}

fn parse_mode(text: &str) -> u32 {
    u32::from_str_radix(text, 8).unwrap_or_else(|_| panic!("mode {text:?}"))
}

fn restore_dir_modes(path: &Path) {
    let Ok(meta) = fs::symlink_metadata(path) else {
        return;
    };
    if !meta.is_dir() {
        return;
    }
    set_mode(path, 0o700);
    if let Ok(entries) = fs::read_dir(path) {
        for entry in entries.flatten() {
            restore_dir_modes(&entry.path());
        }
    }
}

#[cfg(unix)]
fn file_mode(meta: &fs::Metadata) -> String {
    use std::os::unix::fs::PermissionsExt;
    format!("{:04o}", meta.permissions().mode() & 0o777)
}

#[cfg(not(unix))]
fn file_mode(_meta: &fs::Metadata) -> String {
    String::new()
}

fn running_as_root(scratch: &Path) -> bool {
    let probe = scratch.join("root-probe");
    fs::write(&probe, b"x").expect("probe file");
    set_mode(&probe, 0);
    let readable = fs::read(&probe).is_ok();
    let _ = fs::remove_file(&probe);
    readable
}

/// Masks `mem_<16 hex>` in `bytes`.
fn member_ids_masked(bytes: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index..].starts_with(b"mem_")
            && bytes.len() >= index + 20
            && bytes[index + 4..index + 20]
                .iter()
                .all(|byte| matches!(byte, b'0'..=b'9' | b'a'..=b'f'))
        {
            out.extend_from_slice(b"<member_id>");
            index += 20;
        } else {
            out.push(bytes[index]);
            index += 1;
        }
    }
    out
}

fn replace_bytes(haystack: &[u8], needle: &[u8], with: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(haystack.len());
    let mut index = 0;
    while index < haystack.len() {
        if haystack[index..].starts_with(needle) {
            out.extend_from_slice(with);
            index += needle.len();
        } else {
            out.push(haystack[index]);
            index += 1;
        }
    }
    out
}

/// Masks the scratch root and member ids; output that is not valid UTF-8 is
/// compared as `hex:<digits>`, like the Go generator records it.
fn normalize(bytes: &[u8], root: &Path) -> String {
    let root = root.to_string_lossy().into_owned();
    let masked = replace_bytes(bytes, root.as_bytes(), b"<root>");
    let masked = member_ids_masked(&masked);
    match String::from_utf8(masked) {
        Ok(text) => text,
        Err(error) => format!("hex:{}", hex::encode(error.into_bytes())),
    }
}

/// Replaces every `<hex:digits>` token with the bytes it names.
#[cfg(unix)]
fn decode_arg(arg: &str) -> std::ffi::OsString {
    use std::os::unix::ffi::OsStringExt;
    let mut bytes = Vec::new();
    let mut rest = arg;
    while let Some(start) = rest.find("<hex:") {
        let Some(end) = rest[start..].find('>') else {
            break;
        };
        bytes.extend_from_slice(&rest.as_bytes()[..start]);
        bytes.extend(hex::decode(&rest[start + 5..start + end]).expect("hex token"));
        rest = &rest[start + end + 1..];
    }
    bytes.extend_from_slice(rest.as_bytes());
    std::ffi::OsString::from_vec(bytes)
}

#[cfg(not(unix))]
fn decode_arg(arg: &str) -> std::ffi::OsString {
    std::ffi::OsString::from(arg)
}

/// Mirrors the Go generator: a file holding a consistent stored identity has
/// its key material masked; anything else is compared verbatim.
fn mask_stored_identity(content: &str) -> String {
    #[derive(Deserialize)]
    struct Stored {
        #[serde(default)]
        member_id: String,
        #[serde(default)]
        public_key: String,
        #[serde(default)]
        private_key: String,
    }
    let Ok(stored) = serde_json::from_str::<Stored>(content) else {
        return content.to_owned();
    };
    let (Ok(private), Ok(public)) = (
        hex::decode(&stored.private_key),
        hex::decode(&stored.public_key),
    ) else {
        return content.to_owned();
    };
    if private.len() != 64
        || public.len() != 32
        || private[32..] != public[..]
        || symroom_core::identity::compute_member_id(&public) != stored.member_id
    {
        return content.to_owned();
    }
    let signature =
        symroom_core::identity::sign(&private, b"store-test").expect("a consistent key signs");
    if !symroom_core::identity::verify(&public, b"store-test", &signature.to_bytes()) {
        return content.to_owned();
    }
    content
        .replace(&stored.private_key, "<private_key>")
        .replace(&stored.public_key, "<public_key>")
        .replace(&stored.member_id, "<member_id>")
}

fn apply_ops(
    root: &Path,
    ops: &[Op],
    run_again: &dyn Fn(),
    run_other: &dyn Fn(&str),
) -> BTreeMap<String, String> {
    let mut before = BTreeMap::new();
    for op in ops {
        let path = root.join(&op.path);
        match op.op.as_str() {
            "dir" => {
                fs::create_dir_all(&path).expect("dir");
                set_mode(&path, parse_mode(&op.mode));
            }
            "file" => {
                fs::create_dir_all(path.parent().expect("parent")).expect("parent dir");
                fs::write(&path, op.content.as_bytes()).expect("file");
                set_mode(&path, parse_mode(&op.mode));
                before.insert(op.path.clone(), op.content.clone());
            }
            "symlink" => {
                fs::create_dir_all(path.parent().expect("parent")).expect("parent dir");
                #[cfg(unix)]
                std::os::unix::fs::symlink(&op.target, &path).expect("symlink");
            }
            "chmod" => set_mode(&path, parse_mode(&op.mode)),
            "remove" => {
                restore_dir_modes(&path);
                let _ = fs::remove_dir_all(&path);
                let _ = fs::remove_file(&path);
            }
            "run" => run_again(),
            "cli" => run_other(&op.content),
            other => panic!("unknown op {other}"),
        }
    }
    before
}

fn command(case: &Case, root: &Path, args: &[String]) -> Command {
    let mut command = if case.umask.is_empty() {
        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        command.args(args.iter().map(|arg| decode_arg(arg)));
        command
    } else {
        let mut command = Command::new("/bin/sh");
        command
            .args([
                "-c".to_owned(),
                format!("umask {}; exec \"$@\"", case.umask),
                "sh".to_owned(),
                env!("CARGO_BIN_EXE_symroom").to_owned(),
            ])
            .args(args.iter().map(|arg| decode_arg(arg)));
        command
    };
    command.isolated_env();
    let path = |p: PathBuf| p.to_string_lossy().into_owned();
    for (key, value) in [
        ("TMPDIR", path(root.join("tmp"))),
        ("TMP", path(root.join("tmp"))),
        ("TEMP", path(root.join("tmp"))),
        ("TZ", "UTC".to_owned()),
        ("PATH", path(root.join("bin"))),
    ] {
        command.env(key, value);
    }
    match case.home.as_str() {
        "unset" => {}
        "empty" => {
            command.env("HOME", "");
        }
        _ => {
            command.env("HOME", path(root.join("home")));
            command.env("USERPROFILE", path(root.join("home")));
        }
    }
    match case.xdg.as_str() {
        "unset" => {}
        "empty" => {
            command.env("XDG_DATA_HOME", "");
        }
        "relative" => {
            command.env("XDG_DATA_HOME", "rel/xdg");
        }
        "dotdot" => {
            command.env("XDG_DATA_HOME", path(root.join("xdg/../xdg/sub/..")));
        }
        _ => {
            command.env("XDG_DATA_HOME", path(root.join("xdg")));
        }
    }
    if !case.room_env.is_empty() {
        command.env("SYMROOM_ROOM_DIR", path(root.join(&case.room_env)));
    }
    if !case.key_hex.is_empty() {
        command.env("SYMROOM_IDENTITY_KEY", &case.key_hex);
    }
    if case.identity_key {
        command.env(
            "SYMROOM_IDENTITY_KEY",
            "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
        );
    }
    command.current_dir(if case.cwd.is_empty() {
        root.to_path_buf()
    } else {
        root.join(&case.cwd)
    });
    command
}

fn snapshot(root: &Path, before: &BTreeMap<String, String>) -> Vec<Entry> {
    fn walk(root: &Path, dir: &Path, before: &BTreeMap<String, String>, out: &mut Vec<Entry>) {
        let mut names: Vec<_> = fs::read_dir(dir)
            .expect("read dir")
            .map(|entry| entry.expect("entry").path())
            .collect();
        names.sort();
        for path in names {
            let rel = path
                .strip_prefix(root)
                .expect("relative")
                .to_string_lossy()
                .replace(if cfg!(windows) { '\\' } else { '/' }, "/");
            if rel == "tmp" || rel == "bin" || rel.starts_with("tmp/") {
                continue;
            }
            let meta = fs::symlink_metadata(&path).expect("lstat");
            let mut entry = Entry {
                path: rel.clone(),
                kind: String::new(),
                mode: String::new(),
                content: String::new(),
                target: String::new(),
                fresh: false,
            };
            if meta.file_type().is_symlink() {
                entry.kind = "symlink".to_owned();
                entry.target = fs::read_link(&path)
                    .expect("readlink")
                    .to_string_lossy()
                    .into_owned();
            } else if meta.is_dir() {
                entry.kind = "dir".to_owned();
                entry.mode = file_mode(&meta);
                out.push(entry);
                walk(root, &path, before, out);
                continue;
            } else {
                entry.kind = "file".to_owned();
                entry.mode = file_mode(&meta);
                if let Ok(data) = fs::read(&path) {
                    let mut content = String::from_utf8_lossy(&data).into_owned();
                    if rel.ends_with(".sqlite") {
                        if data.starts_with(b"SQLite format 3\0") {
                            content = "<sqlite database>".to_owned();
                        }
                    } else {
                        content = mask_stored_identity(&content);
                    }
                    entry.fresh = before.get(&rel).map(String::as_bytes) != Some(data.as_slice());
                    entry.content = content;
                }
            }
            out.push(entry);
        }
    }
    let mut out = Vec::new();
    walk(root, root, before, &mut out);
    out.sort_by(|a, b| a.path.cmp(&b.path));
    out
}

#[test]
fn identity_store_and_index_rebuild_match_go_process_contract() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let data = fs::read(root.join("testdata/port/room/store-cli.json"))
        .expect("read Go-generated store fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse store fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 5);
    for (source, expected) in &fixture.source_hashes {
        let bytes =
            fs::read(root.join(source)).unwrap_or_else(|error| panic!("read {source}: {error}"));
        assert_eq!(
            expected,
            &hex::encode(Sha256::digest(bytes)),
            "Go oracle source hash {source}"
        );
    }
    let temp = TempDir::new();
    let is_root = cfg!(unix) && running_as_root(&temp.0);
    let (mut replayed, mut skipped) = (0_usize, 0_usize);
    let mut failures: Vec<String> = Vec::new();
    for (index, case) in fixture.cases.iter().enumerate() {
        if case.unix_only && !cfg!(unix) {
            eprintln!("SKIP {}: Unix path, mode and errno semantics", case.name);
            skipped += 1;
            continue;
        }
        if !case.host.is_empty()
            && case.host != std::env::consts::OS
            && !(case.host == "darwin" && cfg!(target_os = "macos"))
        {
            eprintln!("SKIP {}: file-name policy of {}", case.name, case.host);
            skipped += 1;
            continue;
        }
        if case.needs_non_root && is_root {
            eprintln!("SKIP {}: root ignores file permissions", case.name);
            skipped += 1;
            continue;
        }
        let root = fs::canonicalize(&temp.0)
            .expect("canonical temp")
            .join(index.to_string());
        for dir in ["home", "tmp", "bin", "xdg"] {
            fs::create_dir_all(root.join(dir)).expect("scratch dir");
            set_mode(&root.join(dir), 0o700);
        }
        let again = || {
            let _ = command(case, &root, &case.args).output();
        };
        let other = |args: &str| {
            let args: Vec<String> = args.split(' ').map(str::to_owned).collect();
            let _ = command(case, &root, &args).output();
        };
        let before = apply_ops(&root, &case.ops, &again, &other);
        let output = command(case, &root, &case.args)
            .output()
            .expect("run symroom");
        restore_dir_modes(&root);
        let mut stdout = normalize(&output.stdout, &root);
        if cfg!(windows) {
            // `index rebuild` prints the host separator; the fixture holds `/`.
            stdout = stdout.replace('\\', "/");
        }
        let stderr = normalize(&output.stderr, &root);
        let tree = snapshot(&root, &before);
        let mut problems = Vec::new();
        if output.status.code() != Some(case.exit_code) {
            problems.push(format!(
                "exit code {:?}, want {}",
                output.status.code(),
                case.exit_code
            ));
        }
        if stdout != case.stdout {
            problems.push(format!("stdout {stdout:?}, want {:?}", case.stdout));
        }
        let stderr_matches = if case.stderr_is_prefix {
            stderr.starts_with(&case.stderr)
        } else {
            stderr == case.stderr
        };
        if !stderr_matches {
            problems.push(format!("stderr {stderr:?}, want {:?}", case.stderr));
        }
        if tree != case.tree {
            let missing: Vec<_> = case.tree.iter().filter(|e| !tree.contains(e)).collect();
            let extra: Vec<_> = tree.iter().filter(|e| !case.tree.contains(e)).collect();
            problems.push(format!(
                "tree differs: want-only {missing:?}, got-only {extra:?}"
            ));
        }
        failures.extend(problems.into_iter().map(|p| format!("{}: {p}", case.name)));
        replayed += 1;
    }
    eprintln!(
        "store cases: {} total, {replayed} replayed, {skipped} SKIPPED",
        fixture.cases.len()
    );
    assert!(replayed > 0, "no store case ran");
    assert!(
        failures.is_empty(),
        "{} differences from the Go process:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
