#![deny(unsafe_code)]

//! Replays `testdata/port/room/identity-provider-cli.json`: the Go
//! `identity.Load` resolution chain (`SYMROOM_IDENTITY_KEY`, `symvault get`,
//! macOS `security`, identity file) observed through `symroom identity
//! show|export` with fake provider executables on a scoped `PATH`.

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
struct Stub {
    #[serde(default)]
    stdout: String,
    #[serde(default)]
    stderr: String,
    #[serde(default)]
    exit: i32,
}

/// Bytes as text when valid UTF-8, hex otherwise (Go's `encoding/json` would
/// replace invalid bytes).
#[derive(Deserialize, PartialEq, Eq, Debug, Clone)]
struct File {
    name: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    content_hex: String,
}

impl File {
    fn bytes(&self) -> Vec<u8> {
        if self.content_hex.is_empty() {
            self.content.clone().into_bytes()
        } else {
            hex::decode(&self.content_hex).expect("fixture hex")
        }
    }
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    env_key: Option<String>,
    symvault: Option<Stub>,
    security: Option<Stub>,
    #[serde(default)]
    darwin_only: bool,
    #[serde(default)]
    identity_files: Vec<File>,
    exit_code: i32,
    stdout: String,
    stderr: String,
    tool_calls: String,
    final_files: Vec<File>,
}

#[test]
fn identity_provider_chain_matches_go_process_contract() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/identity-provider-cli.json"))
            .expect("Go identity provider fixture"),
    )
    .expect("parse Go fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 2);
    for (path, hash) in &fixture.source_hashes {
        assert_eq!(
            hex::encode(Sha256::digest(
                fs::read(root.join(path)).expect("Go source")
            )),
            *hash,
            "{path} changed since the fixture was recorded"
        );
    }
    for needle in [
        "env-seed-wins",
        "symvault-valid-private64",
        "file-valid-stored-name-differs",
        "security-valid-private64",
    ] {
        assert!(fixture.cases.iter().any(|case| case.name == needle));
    }

    let temp = TempDir::new();
    let helper = build_provider_helper(&temp.path);
    let mut replayed = 0_usize;
    let mut skipped = 0_usize;
    let mut failures = Vec::new();
    for case in &fixture.cases {
        if case.darwin_only && !cfg!(target_os = "macos") {
            println!("SKIP {}: macOS Keychain chain", case.name);
            skipped += 1;
            continue;
        }
        replayed += 1;
        if let Err(message) = replay(case, &temp.path, &helper) {
            failures.push(format!("{}: {message}", case.name));
        }
    }
    println!(
        "identity provider cases: {} total, {replayed} replayed, {skipped} SKIPPED",
        fixture.cases.len()
    );
    assert!(replayed > 0, "no identity provider case replayed");
    assert!(
        failures.is_empty(),
        "{} of {replayed} identity provider cases differ from Go:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

fn replay(case: &Case, scratch: &Path, helper: &Path) -> Result<(), String> {
    let work = scratch.join(&case.name);
    let home = work.join("home");
    let data_home = work.join("data");
    let tools = work.join("tools");
    let tmp = work.join("tmp");
    let cwd = work.join("cwd");
    let trace = work.join("trace");
    for dir in [&home, &data_home, &tools, &tmp, &cwd, &trace] {
        fs::create_dir_all(dir).map_err(|error| error.to_string())?;
    }
    let identities = data_home.join("symroom").join("identities");
    fs::create_dir_all(&identities).map_err(|error| error.to_string())?;
    for file in &case.identity_files {
        fs::write(identities.join(&file.name), file.bytes()).map_err(|error| error.to_string())?;
    }
    let log = trace.join("calls.log");
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(&case.args)
        .current_dir(&cwd)
        .env_clear()
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_DATA_HOME", &data_home)
        .env("TMPDIR", &tmp)
        .env("TMP", &tmp)
        .env("TEMP", &tmp)
        .env("PATH", &tools)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("PROVIDER_LOG", &log);
    if cfg!(windows)
        && let Some(system_root) = std::env::var_os("SystemRoot")
    {
        command.env("SystemRoot", system_root);
    }
    if let Some(key) = &case.env_key {
        command.env("SYMROOM_IDENTITY_KEY", key);
    }
    for (tool, stub) in [("symvault", &case.symvault), ("security", &case.security)] {
        let Some(stub) = stub else { continue };
        fs::copy(
            helper,
            tools.join(format!("{tool}{}", std::env::consts::EXE_SUFFIX)),
        )
        .map_err(|error| error.to_string())?;
        let upper = tool.to_uppercase();
        command
            .env(format!("PROVIDER_STDOUT_{upper}"), &stub.stdout)
            .env(format!("PROVIDER_STDERR_{upper}"), &stub.stderr)
            .env(format!("PROVIDER_EXIT_{upper}"), stub.exit.to_string());
    }
    let output = command.output().map_err(|error| error.to_string())?;

    let mut problems = Vec::new();
    if output.status.code() != Some(case.exit_code) {
        problems.push(format!(
            "exit {:?} != {}",
            output.status.code(),
            case.exit_code
        ));
    }
    if output.stdout != case.stdout.as_bytes() {
        problems.push(format!(
            "stdout {:?} != {:?}",
            String::from_utf8_lossy(&output.stdout),
            case.stdout
        ));
    }
    if output.stderr != case.stderr.as_bytes() {
        problems.push(format!(
            "stderr {:?} != {:?}",
            String::from_utf8_lossy(&output.stderr),
            case.stderr
        ));
    }
    let calls = fs::read(&log).unwrap_or_default();
    if calls != case.tool_calls.as_bytes() {
        problems.push(format!(
            "tool calls {:?} != {:?}",
            String::from_utf8_lossy(&calls),
            case.tool_calls
        ));
    }
    let after = snapshot(&work);
    if after != case.final_files {
        problems.push(format!(
            "on-disk state {:?} != {:?}",
            after.iter().map(|file| &file.name).collect::<Vec<_>>(),
            case.final_files
                .iter()
                .map(|file| &file.name)
                .collect::<Vec<_>>()
        ));
    }
    if problems.is_empty() {
        Ok(())
    } else {
        Err(problems.join("; "))
    }
}

/// Every file below the case root except the provider executables and the call
/// trace, as `/`-separated relative names in byte order (Go: `sort.Slice`).
fn snapshot(work: &Path) -> Vec<File> {
    fn visit(dir: &Path, base: &Path, files: &mut Vec<File>) {
        for entry in fs::read_dir(dir).expect("read case directory") {
            let path = entry.expect("case entry").path();
            let rel = path
                .strip_prefix(base)
                .expect("relative path")
                .to_string_lossy()
                .replace('\\', "/");
            if path.is_dir() {
                if rel != "tools" && rel != "trace" {
                    visit(&path, base, files);
                }
            } else {
                let data = fs::read(&path).expect("read case file");
                files.push(match String::from_utf8(data) {
                    Ok(content) => File {
                        name: rel,
                        content,
                        content_hex: String::new(),
                    },
                    Err(error) => File {
                        name: rel,
                        content: String::new(),
                        content_hex: hex::encode(error.into_bytes()),
                    },
                });
            }
        }
    }
    let mut files = Vec::new();
    visit(work, work, &mut files);
    files.sort_by(|left, right| left.name.cmp(&right.name));
    files
}

fn build_provider_helper(work: &Path) -> PathBuf {
    let source = work.join("provider-tool-helper.rs");
    let output = work.join(format!(
        "provider-tool-helper{}",
        std::env::consts::EXE_SUFFIX
    ));
    fs::write(&source, include_str!("support/provider_tool_helper.rs"))
        .expect("write Rust provider helper");
    let built = Command::new("rustc")
        .args(["--edition=2024", "-o"])
        .arg(&output)
        .arg(&source)
        .output()
        .expect("run rustc for provider helper");
    assert!(
        built.status.success(),
        "provider helper build failed: {}",
        String::from_utf8_lossy(&built.stderr)
    );
    output
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symroom-identity-provider-{}-{nonce}-{}",
            std::process::id(),
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
