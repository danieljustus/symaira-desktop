#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::{event::Event, journal};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hash: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    files: Vec<JournalFile>,
    ordered_ids: Vec<String>,
    ordered_markers: Vec<String>,
    #[serde(default)]
    error_class: String,
    #[serde(default)]
    error_author: String,
}

#[derive(Deserialize)]
struct JournalFile {
    name: String,
    content: String,
    #[serde(default)]
    repeat_bytes: usize,
    #[serde(default)]
    repeat_byte: String,
    #[serde(default)]
    repeat_newline: bool,
}

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn scratch(label: &str) -> PathBuf {
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("clock after epoch")
        .as_nanos();
    std::env::temp_dir().join(format!(
        "symroom-merge-read-{}-{label}-{nonce}",
        std::process::id()
    ))
}

#[test]
fn go_merge_all_disk_order_corruption_and_scanner_prefix() {
    let root = root();
    let fixture: Fixture = serde_json::from_str(
        &fs::read_to_string(root.join("testdata/port/room/merge-read.json"))
            .expect("Go MergeAll fixture"),
    )
    .expect("fixture JSON");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 3);
    let mut source =
        fs::read(root.join("internal/room/journal/merge.go")).expect("Go merge source");
    source.extend_from_slice(
        &fs::read(root.join("internal/room/journal/journal.go")).expect("Go reader source"),
    );
    assert_eq!(fixture.source_hash, hex::encode(Sha256::digest(source)));

    for case in fixture.cases {
        let room = scratch(&case.id);
        let journal_dir = room.join("journal");
        fs::create_dir_all(&journal_dir).expect("create journal dir");
        for file in &case.files {
            let mut bytes = file.content.as_bytes().to_vec();
            if file.repeat_bytes > 0 {
                let byte = file.repeat_byte.as_bytes();
                assert_eq!(byte.len(), 1, "{} repeat byte", case.id);
                bytes.extend(std::iter::repeat_n(byte[0], file.repeat_bytes));
                if file.repeat_newline {
                    bytes.push(b'\n');
                }
            }
            fs::write(journal_dir.join(&file.name), bytes).expect("write journal fixture");
        }

        match journal::merge_all(&room) {
            Ok(events) => {
                assert!(case.error_class.is_empty(), "{} expected error", case.id);
                let ids = events
                    .iter()
                    .map(|event| event.id.clone())
                    .collect::<Vec<_>>();
                let markers = events.iter().map(marker).collect::<Vec<_>>();
                assert_eq!(ids, case.ordered_ids, "{} event order", case.id);
                assert_eq!(markers, case.ordered_markers, "{} stable markers", case.id);
            }
            Err(journal::ReadSegmentsError::Parse { author, .. }) => {
                assert_eq!(
                    case.error_class, "malformed_event",
                    "{} error class",
                    case.id
                );
                assert_eq!(author, case.error_author, "{} segment context", case.id);
            }
            Err(error) => panic!("{} unexpected Go-compatible error class: {error}", case.id),
        }
        fs::remove_dir_all(room).expect("remove test scratch room");
    }
}

fn marker(event: &Event) -> String {
    serde_json::from_str::<serde_json::Value>(event.body.get()).expect("event body JSON")["marker"]
        .as_str()
        .expect("marker")
        .to_owned()
}
