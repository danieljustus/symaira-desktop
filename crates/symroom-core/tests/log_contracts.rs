#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::PathBuf,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::log::{self, LogFilter};

#[derive(Deserialize)]
struct JournalFixture {
    cases: Vec<JournalCase>,
}
#[derive(Deserialize)]
struct JournalCase {
    name: String,
    files: BTreeMap<String, String>,
}
#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct Case {
    name: String,
    journal_case: String,
    filter: LogFilter,
    events: Option<Vec<String>>,
    human: Option<Vec<String>>,
    invalid_count: usize,
    #[serde(default)]
    error_prefix: String,
}

#[test]
fn room_log_matches_go_filters_order_warnings_and_human_output() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/log.json")).expect("Go log fixture"),
    )
    .expect("parse Go log fixture");
    let journal: JournalFixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/verify.json")).expect("Go journal fixture"),
    )
    .expect("parse Go journal fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 3);
    for (path, hash) in &fixture.source_hashes {
        assert_eq!(
            hex::encode(Sha256::digest(
                fs::read(root.join(path)).expect("Go source")
            )),
            *hash
        );
    }
    let journals: BTreeMap<_, _> = journal
        .cases
        .into_iter()
        .map(|case| (case.name, case.files))
        .collect();
    let temp = TempDir::new();
    for case in fixture.cases {
        let room = temp.path.join(&case.name);
        let journal_dir = room.join("journal");
        fs::create_dir_all(&journal_dir).expect("create journal");
        for (name, content) in &journals[&case.journal_case] {
            fs::write(journal_dir.join(name), content).expect("write Go journal");
        }
        match log::query(&room, &case.filter) {
            Err(error) if !case.error_prefix.is_empty() => {
                assert!(
                    error.to_string().starts_with(&case.error_prefix),
                    "{} error: {error}",
                    case.name
                );
            }
            Ok(result) if case.error_prefix.is_empty() => {
                assert_eq!(
                    result.invalid_count, case.invalid_count,
                    "{} invalid count",
                    case.name
                );
                let lines = result
                    .events
                    .iter()
                    .map(|event| {
                        String::from_utf8(event.marshal_json_line().expect("event line"))
                            .expect("UTF-8 line")
                    })
                    .collect::<Vec<_>>();
                assert_eq!(
                    lines,
                    case.events.unwrap_or_default(),
                    "{} event bytes",
                    case.name
                );
                let human = result
                    .events
                    .iter()
                    .map(log::format_event_human)
                    .collect::<Vec<_>>();
                assert_eq!(
                    human,
                    case.human.unwrap_or_default(),
                    "{} human output",
                    case.name
                );
            }
            other => panic!(
                "{} unexpected result: {}",
                case.name,
                match other {
                    Ok(_) => "success",
                    Err(_) => "error",
                }
            ),
        }
    }
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
        let path = std::env::temp_dir().join(format!("symroom-log-{}-{nonce}", std::process::id()));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}
impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove scratch directory");
    }
}
