#![cfg(unix)]
#![deny(unsafe_code)]

//! Frozen replay of the former `render-pdfops-hostile-differential` gate, which
//! ran the Go PDF oracle and the Rust probe side by side against the REAL
//! Poppler (`pdfinfo`, `pdfseparate`, `pdfunite`) and `qpdf` on hostile PDFs
//! (decompression bomb, page-tree cycles, truncated/encrypted/garbage input,
//! hostile page selectors and output destinations).
//!
//! `scripts/rust-port/pdfops/hostile_replay.py --record` ran the Go driver once
//! per OS over the 455-case corpus and committed the normalized observations
//! (response/error text, every filesystem side effect and the real-tool view of
//! each produced PDF) in `testdata/port/render/pdfops-hostile-recording-<os>.json`.
//! This test makes the same Python harness rebuild each world and run the Rust
//! `pdfops_probe` example with the installed real tools, comparing it with the
//! recorded Go observation. It never starts Go. The recording names the tool
//! versions it was made with; other versions fail loudly instead of passing
//! vacuously. The probe is a cargo example: run with `--examples`.

use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
};

const EXPECTED_CASES: usize = 455;

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
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

#[test]
fn hostile_pdf_corpus_matches_the_go_recording_with_real_tools() {
    let os = if cfg!(target_os = "macos") {
        "darwin"
    } else if cfg!(target_os = "linux") {
        "linux"
    } else {
        panic!("hostile PDF recordings exist for macOS and Linux only")
    };
    let root = root();
    let recording = root.join(format!(
        "testdata/port/render/pdfops-hostile-recording-{os}.json"
    ));
    let document: serde_json::Value =
        serde_json::from_slice(&fs::read(&recording).expect("Go recording")).expect("decode");
    assert_eq!(document["schema_version"], 1);
    assert_eq!(document["compiler"], "go1.26.10");
    assert_eq!(document["os"], os);
    let cases = document["cases"].as_array().expect("cases").len();
    assert_eq!(cases, EXPECTED_CASES, "recorded hostile case count");

    let output = Command::new("python3")
        .arg("-I")
        .arg(root.join("scripts/rust-port/pdfops/hostile_replay.py"))
        .arg("--replay")
        .arg(&recording)
        .arg("--rust-binary")
        .arg(examples_dir().join("pdfops_probe"))
        .env("PYTHONDONTWRITEBYTECODE", "1")
        .output()
        .expect("start the hostile replay harness (python3 and the real PDF tools are required)");
    let stdout = String::from_utf8_lossy(&output.stdout);
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(
        output.status.success(),
        "hostile replay failed ({}):\nstdout:\n{stdout}\nstderr:\n{stderr}",
        output.status
    );
    assert!(
        stdout.contains(&format!(
            "PASS hostile PDF replay: {EXPECTED_CASES} cases match the Go recording"
        )),
        "unexpected replay report:\n{stdout}"
    );
}
