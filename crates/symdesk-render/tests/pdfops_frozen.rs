#![cfg(unix)]
#![deny(unsafe_code)]

//! Frozen replay of the former `render-pdfops-differential` gate, which built
//! the Go PDF oracle drivers and compared them live with the Rust probes
//! (`scripts/rust-port/pdfops/verify.py`).
//!
//! `scripts/rust-port/pdfops/record.py` ran the real Go drivers through the
//! three corpora once and committed the normalized observations in
//! `testdata/port/render/pdfops-recording.json`:
//!
//! * pdfops: split/merge/rotate/parse with the controlled `pdf_tool_stub`
//!   standing in for Poppler and qpdf;
//! * press: the Typst profile and discovery corpus;
//! * safety: process-tree cancellation, output floods and publication races.
//!
//! This test rebuilds each world, runs the Rust example probes
//! (`pdfops_probe`, `press_probe`) and compares the same normalized
//! observation. It never starts Go. The probes are cargo examples, so run it
//! with `--examples` (the Make target does) to have them built.

use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    os::unix::fs::{DirBuilderExt, PermissionsExt},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant},
};

use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};

const RECORDING: &str = include_str!("../../../testdata/port/render/pdfops-recording.json");
const EXPECTED_PDFOPS: usize = 157;
const EXPECTED_PRESS: usize = 13;
const EXPECTED_SAFETY: usize = 11;
const TOOLS: [&str; 4] = ["pdfinfo", "pdfseparate", "pdfunite", "qpdf"];

static NEXT: AtomicU64 = AtomicU64::new(0);

fn recording() -> Value {
    let document: Value = serde_json::from_str(RECORDING).expect("decode Go recording");
    assert_eq!(document["schema_version"], 1);
    assert_eq!(document["compiler"], "go1.26.10");
    assert_eq!(
        document["oracle"]["commit"],
        "5c666cbf6051eba7b8e659d7d3f16d051c3404b9"
    );
    document
}

fn examples_dir() -> PathBuf {
    let exe = std::env::current_exe().expect("test executable");
    let dir = exe
        .parent()
        .and_then(Path::parent)
        .expect("target profile directory")
        .join("examples");
    assert!(
        dir.join("pdfops_probe").is_file(),
        "{} is missing: run this test with `cargo test --examples` so the probes are built",
        dir.display()
    );
    dir
}

fn scratch(label: &str) -> PathBuf {
    let root = std::env::temp_dir().join(format!(
        "symdesk-pdfops-frozen-{label}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&root)
        .expect("scratch root");
    // Go resolved its worlds; the normalizer replaces the exact root text.
    fs::canonicalize(root).expect("canonical scratch root")
}

fn mkdir(path: &Path, mode: u32) {
    fs::DirBuilder::new()
        .recursive(true)
        .mode(mode)
        .create(path)
        .unwrap_or_else(|error| panic!("create {}: {error}", path.display()));
}

fn write_mode(path: &Path, bytes: &[u8], mode: u32) {
    fs::write(path, bytes).expect("write fixture file");
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("fixture mode");
}

/// `verify.normalized` plus the stub/tool directories: variable roots first,
/// then the random output-staging suffix, applied to the JSON text.
fn normalize(value: &Value, replacements: &[(String, &str)]) -> Value {
    let mut text = serde_json::to_string(value).expect("serialize observation");
    for (needle, token) in replacements {
        text = text.replace(needle.as_str(), token);
    }
    const MARK: &str = ".symdesk-pdf-";
    let mut result = String::with_capacity(text.len());
    let mut rest = text.as_str();
    while let Some(index) = rest.find(MARK) {
        result.push_str(&rest[..index + MARK.len()]);
        let tail = &rest[index + MARK.len()..];
        let stop = tail
            .find(['/', '\\', '"', ' '])
            .unwrap_or(tail.len());
        result.push_str("<TEMP>");
        rest = &tail[stop..];
    }
    result.push_str(rest);
    serde_json::from_str(&result).expect("normalized observation")
}

fn snapshot(root: &Path) -> Value {
    fn visit(root: &Path, dir: &Path, out: &mut Map<String, Value>) {
        let mut entries: Vec<_> = fs::read_dir(dir)
            .expect("read world directory")
            .map(|entry| entry.expect("world entry").path())
            .collect();
        entries.sort();
        for path in entries {
            let relative = path
                .strip_prefix(root)
                .expect("below world")
                .to_string_lossy()
                .into_owned();
            let metadata = fs::symlink_metadata(&path).expect("lstat");
            let is_dir = fs::metadata(&path).is_ok_and(|m| m.is_dir());
            if relative != "trace.jsonl" && relative != "ready" {
                let mut entry = Map::new();
                entry.insert("type".into(), json!(if is_dir { "directory" } else { "file" }));
                entry.insert("mode".into(), json!(metadata.permissions().mode() & 0o777));
                if fs::metadata(&path).is_ok_and(|m| m.is_file()) {
                    let digest = Sha256::digest(fs::read(&path).expect("read fixture"));
                    entry.insert("sha256".into(), json!(hex(&digest)));
                }
                out.insert(relative, Value::Object(entry));
            }
            if metadata.is_dir() {
                visit(root, &path, out);
            }
        }
    }
    let mut out = Map::new();
    visit(root, root, &mut out);
    Value::Object(out)
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write as _;
    let mut text = String::new();
    for byte in bytes {
        write!(text, "{byte:02x}").expect("write String");
    }
    text
}

fn trace(world: &Path) -> Value {
    let path = world.join("trace.jsonl");
    if !path.exists() {
        return json!([]);
    }
    Value::Array(
        fs::read(&path)
            .expect("trace")
            .split(|byte| *byte == b'\n')
            .filter(|line| !line.is_empty())
            .map(|line| serde_json::from_slice(line).expect("trace line"))
            .collect(),
    )
}

fn run(binary: &Path, world: &Path, env: &BTreeMap<String, String>, stdin: &str) -> Value {
    let mut child = Command::new(binary)
        .current_dir(world)
        .env_clear()
        .envs(env)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start probe");
    let mut input = child.stdin.take().expect("stdin");
    let bytes = stdin.as_bytes().to_vec();
    let feeder = thread::spawn(move || {
        let _ = input.write_all(&bytes);
    });
    let started = Instant::now();
    let output = child.wait_with_output().expect("probe output");
    feeder.join().expect("stdin feeder");
    assert!(started.elapsed() < Duration::from_secs(40), "probe took too long");
    assert!(
        output.stderr.is_empty(),
        "unexpected probe stderr: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(output.status.success(), "probe exited {:?}", output.status);
    serde_json::from_slice(&output.stdout).expect("probe response")
}

fn private_env(world: &Path, prefix: &str) -> BTreeMap<String, String> {
    let mut env = BTreeMap::from([
        ("TZ".to_owned(), "UTC".to_owned()),
        ("LC_ALL".to_owned(), "C".to_owned()),
    ]);
    for key in [
        "HOME",
        "USERPROFILE",
        "XDG_CONFIG_HOME",
        "XDG_DATA_HOME",
        "XDG_STATE_HOME",
        "XDG_CACHE_HOME",
        "TMPDIR",
        "TMP",
        "TEMP",
    ] {
        let directory = world.join(format!("{prefix}{}", key.to_lowercase()));
        mkdir(&directory, 0o700);
        env.insert(key.to_owned(), directory.to_string_lossy().into_owned());
    }
    env
}

fn path_text(path: &Path) -> String {
    path.to_string_lossy().into_owned()
}

fn assert_same(id: &str, expected: &Value, actual: &Value) {
    assert_eq!(
        actual,
        expected,
        "{id}: observation differs from the Go recording\n  recorded: {expected}\n  actual:   {actual}"
    );
}

#[test]
fn pdf_adapter_corpus_matches_go_recording() {
    let recording = recording();
    let cases = recording["pdfops"].as_array().expect("pdfops cases");
    assert_eq!(cases.len(), EXPECTED_PDFOPS);
    let examples = examples_dir();
    let base = scratch("pdfops");
    let tools = base.join("tools");
    mkdir(&tools, 0o700);
    for name in TOOLS {
        fs::copy(examples.join("pdf_tool_stub"), tools.join(name)).expect("copy stub");
    }
    let replacements = vec![
        (path_text(&tools), "<TOOLS>"),
        (path_text(&examples.join("pdf_tool_stub")), "<STUB>"),
    ];
    for case in cases {
        let id = case["id"].as_str().expect("case id");
        let world = base.join("worlds").join(id);
        mkdir(&world, 0o700);
        mkdir(&world.join("sub"), 0o700);
        for name in ["a.pdf", "b.pdf"] {
            write_mode(&world.join(name), format!("%PDF-fixture-{name}\n").as_bytes(), 0o600);
        }
        for key in ["home", "tmp", "config", "data", "cache", "state"] {
            mkdir(&world.join(key), 0o700);
        }
        if case["existing"] == true {
            mkdir(&world.join("out"), 0o755);
            write_mode(&world.join("out/result.pdf"), b"previous", 0o640);
        }
        let stub = &case["stub"];
        let env = BTreeMap::from([
            ("TZ".to_owned(), "UTC".to_owned()),
            ("LC_ALL".to_owned(), "C".to_owned()),
            ("PATH".to_owned(), path_text(&tools)),
            ("HOME".to_owned(), path_text(&world.join("home"))),
            ("USERPROFILE".to_owned(), path_text(&world.join("home"))),
            ("TMPDIR".to_owned(), path_text(&world.join("tmp"))),
            ("TMP".to_owned(), path_text(&world.join("tmp"))),
            ("TEMP".to_owned(), path_text(&world.join("tmp"))),
            ("XDG_CONFIG_HOME".to_owned(), path_text(&world.join("config"))),
            ("XDG_DATA_HOME".to_owned(), path_text(&world.join("data"))),
            ("XDG_CACHE_HOME".to_owned(), path_text(&world.join("cache"))),
            ("XDG_STATE_HOME".to_owned(), path_text(&world.join("state"))),
            ("PDF_STUB_MODE".to_owned(), stub["mode"].as_str().unwrap_or_default().to_owned()),
            ("PDF_STUB_TOOL".to_owned(), stub["tool"].as_str().unwrap_or_default().to_owned()),
            ("PDF_STUB_INFO".to_owned(), stub["info"].as_str().unwrap_or_default().to_owned()),
        ]);
        let before = snapshot(&world);
        let response = run(&examples.join("pdfops_probe"), &world, &env, &case["request"].to_string());
        let observed_trace = trace(&world);
        let after = snapshot(&world);
        for name in ["a.pdf", "b.pdf"] {
            assert_eq!(before[name], after[name], "{id}: input changed");
        }
        assert_eq!(fs::read_dir(world.join("tmp")).expect("tmp").count(), 0, "{id}: workspace leaked");
        assert!(!has_stage(&world), "{id}: output stage leaked");
        let mut replace = vec![(path_text(&world), "<ROOT>")];
        replace.extend(replacements.iter().cloned());
        let observed = normalize(
            &json!({ "response": response, "trace": observed_trace, "before": before, "after": after }),
            &replace,
        );
        assert_same(id, &case["observation"], &observed);
    }
    fs::remove_dir_all(&base).expect("cleanup");
}

fn has_stage(root: &Path) -> bool {
    fs::read_dir(root).is_ok_and(|entries| {
        entries.flatten().any(|entry| {
            entry.file_name().to_string_lossy().starts_with(".symdesk-pdf-")
                || (entry.path().is_dir() && has_stage(&entry.path()))
        })
    })
}

#[test]
fn press_discovery_corpus_matches_go_recording() {
    let recording = recording();
    let cases = recording["press"].as_array().expect("press cases");
    assert_eq!(cases.len(), EXPECTED_PRESS);
    let examples = examples_dir();
    let stub = examples.join("pdf_tool_stub");
    let base = scratch("press");
    for case in cases {
        let id = case["id"].as_str().expect("case id");
        let world = base.join(id);
        let bindir = world.join("bin");
        mkdir(&bindir, 0o700);
        let tool = bindir.join("typst");
        if case["absent"] != true {
            fs::copy(&stub, &tool).expect("copy stub");
        }
        let mut env = private_env(&world, "env-");
        env.insert("PATH".to_owned(), path_text(&bindir));
        env.insert("PDF_STUB_TOOL".to_owned(), "typst".to_owned());
        env.insert("PDF_STUB_MODE".to_owned(), case["stub"]["mode"].as_str().unwrap_or_default().to_owned());
        env.insert("PDF_STUB_INFO".to_owned(), case["stub"]["info"].as_str().unwrap_or_default().to_owned());
        let steps = case["steps"]
            .to_string()
            .replace("<TOOL>", &path_text(&tool))
            .replace("<STUB>", &path_text(&stub));
        let response = run(&examples.join("press_probe"), &world, &env, &steps);
        assert_eq!(
            response.as_array().map(Vec::len),
            case["steps"].as_array().map(Vec::len),
            "{id}: one response per step"
        );
        assert_eq!(
            fs::read_dir(world.join("env-tmpdir")).expect("tmpdir").count(),
            0,
            "{id}: discovery capture leaked"
        );
        let observed = normalize(
            &json!({ "response": response, "trace": trace(&world) }),
            &[(path_text(&world), "<ROOT>"), (path_text(&stub), "<STUB>")],
        );
        assert_same(id, &case["observation"], &observed);
    }
    fs::remove_dir_all(&base).expect("cleanup");
}

fn python() -> PathBuf {
    std::env::var_os("PATH")
        .into_iter()
        .flat_map(|paths| std::env::split_paths(&paths).collect::<Vec<_>>())
        .map(|dir| dir.join("python3"))
        .find(|candidate| candidate.is_file())
        .expect("python3 on PATH (the controlled safety tool is a Python script)")
}

fn alive(pid: &str) -> bool {
    Command::new("kill")
        .args(["-0", pid])
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|status| status.success())
}

#[test]
fn process_safety_corpus_matches_go_recording() {
    let recording = recording();
    let cases = recording["safety"].as_array().expect("safety cases");
    assert_eq!(cases.len(), EXPECTED_SAFETY);
    let tool_source = recording["safety_tool"].as_str().expect("safety tool");
    let examples = examples_dir();
    let interpreter = python();
    let base = scratch("safety");
    for case in cases {
        let id = case["id"].as_str().expect("case id");
        let operation = case["operation"].as_str().expect("operation");
        let mode = case["mode"].as_str().expect("mode");
        let world = base.join(id);
        mkdir(&world, 0o700);
        let mut env = private_env(&world, "env-");
        env.insert("PATH".to_owned(), String::new());
        env.insert("SAFETY_MODE".to_owned(), mode.to_owned());
        env.insert(
            "SAFETY_OUTPUT".to_owned(),
            if operation == "split" { "parts/part-001.pdf" } else { "out/result.pdf" }.to_owned(),
        );
        let original = b"controlled input; input preservation is byte-exact\n";
        for name in ["a.pdf", "b.pdf"] {
            fs::write(world.join(name), original).expect("input");
        }
        let tool = world.join(if operation == "press" { "typst" } else { "qpdf" });
        write_mode(
            &tool,
            format!("#!{}\n{tool_source}", interpreter.display()).as_bytes(),
            0o700,
        );
        let info = world.join("pdfinfo");
        write_mode(
            &info,
            format!("#!{}\nprint('Pages: 4')\n", interpreter.display()).as_bytes(),
            0o700,
        );
        let request = case["request"]
            .to_string()
            .replace("<TOOL>", &path_text(&tool))
            .replace("<INFO>", &path_text(&info));
        let binary = examples.join(if operation == "press" { "press_probe" } else { "pdfops_probe" });
        let started = Instant::now();
        let response = run(&binary, &world, &env, &request);
        assert!(started.elapsed() < Duration::from_secs(8), "{id}: bounded operation exceeded eight seconds");
        if mode.starts_with("descendant-") {
            let pids: Vec<String> = ["ready", "descendant-ready"]
                .iter()
                .map(|name| fs::read_to_string(world.join(name)).expect("readiness marker"))
                .collect();
            let deadline = Instant::now() + Duration::from_secs(2);
            while pids.iter().any(|pid| alive(pid)) && Instant::now() < deadline {
                thread::sleep(Duration::from_millis(5));
            }
            assert!(!pids.iter().any(|pid| alive(pid)), "{id}: product left an owned process alive");
        }
        for marker in ["leader", "ready", "descendant-ready"] {
            if let Ok(pid) = fs::read_to_string(world.join(marker))
                && alive(&pid)
            {
                let _ = Command::new("kill").args(["-9", &pid]).status();
            }
        }
        for name in ["a.pdf", "b.pdf"] {
            assert_eq!(fs::read(world.join(name)).expect("input"), original, "{id}: input changed");
        }
        assert!(!has_stage(&world), "{id}: output staging leaked");
        let observed = normalize(&response, &[(path_text(&world), "<ROOT>")]);
        assert_same(id, &case["observation"], &observed);
    }
    fs::remove_dir_all(&base).expect("cleanup");
}
