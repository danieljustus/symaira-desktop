#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::journal::verify_chain;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    author: String,
    file: bool,
    content: String,
    #[serde(default)]
    content_hex: String,
    #[serde(default)]
    repeat_line: usize,
    code: String,
    error: String,
}

#[test]
fn replays_go_verify_chain_cases() {
    let repository = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(repository.join("testdata/port/room/verify-chain.json"))
            .expect("read Go verify-chain fixture"),
    )
    .expect("parse Go verify-chain fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 15, "Go case inventory");
    assert_eq!(fixture.source_hashes.len(), 4, "Go source inventory");
    for (relative, expected) in &fixture.source_hashes {
        let source = fs::read(repository.join(relative)).expect("read Go source");
        assert_eq!(hex::encode(Sha256::digest(source)), *expected, "{relative}");
    }
    let mut names = std::collections::BTreeSet::new();
    for case in &fixture.cases {
        assert!(names.insert(case.id.as_str()), "duplicate case {}", case.id);
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("after epoch")
            .as_nanos();
        let room = std::env::temp_dir().join(format!(
            "symroom-verify-chain-{}-{stamp}-{}",
            std::process::id(),
            case.id
        ));
        fs::create_dir(&room).expect("create scratch room");
        if case.file {
            fs::create_dir(room.join("journal")).expect("create scratch journal");
            let content = if case.repeat_line > 0 {
                format!("{}\n", " ".repeat(case.repeat_line)).into_bytes()
            } else if !case.content_hex.is_empty() {
                hex::decode(&case.content_hex).expect("decode Go segment hex bytes")
            } else {
                case.content.as_bytes().to_vec()
            };
            fs::write(
                room.join("journal").join(format!("{}.jsonl", case.author)),
                content,
            )
            .expect("write Go segment bytes");
        }
        let result = verify_chain(&room, &case.author);
        let (code, message) = match result {
            Ok(()) => ("ok", String::new()),
            Err(error) => {
                let code = match error {
                    symroom_core::journal::VerifyChainError::Sequence { .. } => "seq_mismatch",
                    symroom_core::journal::VerifyChainError::Previous { .. } => "chain_broken",
                    symroom_core::journal::VerifyChainError::ScannerTooLong => "scanner_error",
                    _ => panic!("{}: unexpected error: {error}", case.id),
                };
                (code, error.to_string())
            }
        };
        assert_eq!(code, case.code, "code in {}", case.id);
        assert_eq!(message, case.error, "message in {}", case.id);
        fs::remove_dir_all(&room).expect("remove scratch room");
    }
    for name in [
        "missing",
        "valid-two",
        "valid-crlf-and-blank",
        "wrong-seq",
        "wrong-prev",
        "seq-before-prev",
        "omitted-fields",
        "mixed-case-duplicates",
        "unicode-folded-seq",
        "null-after-value",
        "unknown-field-invalid-utf8",
        "scanner-boundary",
        "scanner-too-long",
    ] {
        assert!(names.contains(name), "required Go case {name}");
    }
}
