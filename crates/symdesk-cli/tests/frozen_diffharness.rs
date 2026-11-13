#![cfg(unix)]
#![deny(unsafe_code)]

//! Frozen replay of the three diffharness CLI suites that used to compare a
//! live Go and a live Rust binary: `representative-differential` (CLI part),
//! `dataset-cli-differential` and `retention-cli-differential`.
//!
//! The recordings under `testdata/port/**/**-recording.json` hold the inputs and
//! the normalized observations of the real Go `symdesk` binary, written by
//! `go run ./scripts/rust-port/cmd/diffrecord`. This test replays every case
//! against the Rust binary in an equivalent sandbox; it never starts Go.

#[path = "support/diff_replay.rs"]
mod diff_replay;

const BINARY: &str = env!("CARGO_BIN_EXE_symdesk");

#[test]
fn representative_cli_matches_go_recording() {
    let replayed = diff_replay::replay_all(
        include_str!("../../../testdata/port/representative/cli-recording.json"),
        "representative",
        16,
        BINARY,
    );
    assert_eq!(replayed, 16);
}

#[test]
fn dataset_cli_matches_go_recording() {
    let replayed = diff_replay::replay_all(
        include_str!("../../../testdata/port/dataset/cli-recording.json"),
        "dataset-cli",
        16,
        BINARY,
    );
    assert_eq!(replayed, 16);
}

#[test]
fn retention_cli_matches_go_recording() {
    let replayed = diff_replay::replay_all(
        include_str!("../../../testdata/port/cli/retention-recording.json"),
        "retention",
        61,
        BINARY,
    );
    assert_eq!(replayed, 61);
}
