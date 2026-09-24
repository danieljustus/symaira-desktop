#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::PathBuf,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::journal::{self, VerificationReport};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    files: BTreeMap<String, String>,
    report: Option<VerificationReport>,
    #[serde(default)]
    error: String,
}

#[test]
fn room_verify_matches_go_findings_and_error_class() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/verify.json")).expect("Go verify fixture"),
    )
    .expect("parse Go verify fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 4);
    for (path, hash) in &fixture.source_hashes {
        assert_eq!(
            hex::encode(Sha256::digest(
                fs::read(root.join(path)).expect("Go source")
            )),
            *hash
        );
    }
    let temp = TempDir::new();
    for case in fixture.cases {
        let room = temp.path.join(&case.name);
        let journal_dir = room.join("journal");
        fs::create_dir_all(&journal_dir).expect("create journal");
        for (name, content) in &case.files {
            fs::write(journal_dir.join(name), content).expect("write Go journal");
        }
        match (journal::verify(&room), case.report) {
            (Ok(actual), Some(expected)) => assert_eq!(actual, expected, "{} report", case.name),
            (Err(error), None) if case.error.contains("unmarshal line:") => {
                let expected_prefix = case
                    .error
                    .split_once("unmarshal line:")
                    .expect("parse error class")
                    .0;
                assert!(
                    format!("read all segments: {error}").starts_with(expected_prefix),
                    "{} error: {error}",
                    case.name
                );
            }
            (actual, expected) => {
                panic!("{} result mismatch: {actual:?} vs {expected:?}", case.name)
            }
        }
    }
}

#[cfg(target_os = "linux")]
#[test]
fn verification_rejects_non_utf8_segment_names() {
    use std::os::unix::ffi::OsStringExt;

    let temp = TempDir::new();
    let journal_dir = temp.path.join("journal");
    fs::create_dir(&journal_dir).expect("create journal");
    let name = std::ffi::OsString::from_vec(b"member_\xff.jsonl".to_vec());
    fs::write(journal_dir.join(name), b"{}").expect("write segment");

    let error = journal::verify(&temp.path).expect_err("unverifiable segment must fail closed");
    assert!(format!("{error}").contains("filename is not valid UTF-8"));
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path =
            std::env::temp_dir().join(format!("symroom-verify-{}-{nonce}", std::process::id()));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove scratch directory");
    }
}
