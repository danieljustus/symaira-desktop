#![deny(unsafe_code)]

use std::{fs, io::BufReader, path::Path};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::desk_watch::{EventStreamItem, watch_stream};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hash: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    input: String,
    #[serde(default)]
    repeat_bytes: usize,
    #[serde(default)]
    stop_after: usize,
    #[serde(default)]
    cancel: bool,
    events: Vec<EventStreamItem>,
    #[serde(default)]
    error: String,
}

#[test]
fn watch_stream_replays_go_scanner_and_handler_contract() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/room/watch-stream.json")).expect("Go watch fixture"),
    )
    .expect("parse Go watch fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.source_hash,
        hex::encode(Sha256::digest(
            fs::read(root.join("internal/room/desk/watch.go")).expect("Go watch source")
        ))
    );
    for case in fixture.cases {
        let mut input = case.input.into_bytes();
        if case.repeat_bytes > 0 {
            input.extend(std::iter::repeat_n(b'x', case.repeat_bytes));
            input.push(b'\n');
        }
        let mut reader = BufReader::new(input.as_slice());
        let mut events = Vec::new();
        let result = watch_stream(
            &mut reader,
            || case.cancel,
            |item| {
                events.push(item.clone());
                if case.stop_after > 0 && events.len() == case.stop_after {
                    return Err("handler stopped".to_owned());
                }
                Ok(())
            },
        );
        assert_eq!(events, case.events, "{} events", case.name);
        assert_eq!(
            result.err().unwrap_or_default(),
            case.error,
            "{} error",
            case.name
        );
    }
}
