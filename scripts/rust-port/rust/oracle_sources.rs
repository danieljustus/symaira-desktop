//! Recorded Go oracle source hashes. The Go sources were retired from the tree
//! in RUST-019 (last Go commit `075fdf94`, tag `v0.12.2`), so a fixture's
//! `source_hashes` can no longer be recomputed from files next to the tests.
//! The tests keep asserting the hash is a well-formed SHA-256; binding it to
//! the blobs at the recorded oracle commit is done Go-free in
//! `scripts/rust-port/provenance_check.py --history`.
#![allow(dead_code)]

pub fn assert_recorded_source_hash(path: &str, hash: &str) {
    assert!(!path.is_empty(), "recorded oracle source path is empty");
    assert!(
        hash.len() == 64
            && hash
                .bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte)),
        "recorded oracle source hash for {path} is not 64 lowercase hex characters"
    );
}
