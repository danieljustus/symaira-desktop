#![deny(unsafe_code)]

//! Frozen replay of the former `index-status-cli-differential` gate.
//!
//! The native gate (`scripts/rust-port/index-status/verify.py`) built the
//! canonical Go oracle and this candidate and compared 126 `symdesk index
//! status` runs live (aggregate and document output, state filters, text and
//! JSON diagnostics, duration parsing, SQLite timestamp spellings, provider
//! failures and the bounded timeout controls) in disposable worlds with local
//! fake providers. `scripts/rust-port/index-status/frozen.py record` ran the Go
//! oracle through the same world builder and case runner once and committed the
//! observed exit codes and output streams in
//! `testdata/port/cli/index-status-recording.json`. This test drives
//! `frozen.py replay`, which runs that case inventory with the Rust binary
//! (seeding the populated world with the Rust `index`) and compares every case
//! with the recording. It never starts Go.

use std::{path::Path, process::Command};

const EXPECTED_CASES: usize = 126;

#[test]
fn index_status_cases_match_the_go_recording() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let output = Command::new("python3")
        .arg("-I")
        .arg(root.join("scripts/rust-port/index-status/frozen.py"))
        .arg("replay")
        .arg("--rust-bin")
        .arg(env!("CARGO_BIN_EXE_symdesk"))
        .arg("--recording")
        .arg(root.join("testdata/port/cli/index-status-recording.json"))
        .env("PYTHONDONTWRITEBYTECODE", "1")
        .output()
        .expect("start the index status replay harness (python3 is required)");
    let stdout = String::from_utf8_lossy(&output.stdout);
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(
        output.status.success(),
        "index status replay failed ({}):\nstdout:\n{stdout}\nstderr:\n{stderr}",
        output.status
    );
    assert!(
        stdout.contains(&format!(
            "PASS index status replay: {EXPECTED_CASES} cases match the Go recording"
        )),
        "unexpected replay report:\n{stdout}"
    );
}
