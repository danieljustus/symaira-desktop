#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::{
    event::{self, Event},
    journal::{VerifyChainError, verify_chain},
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    public_key: String,
    cases: Vec<Case>,
    chain_cases: Vec<ChainCase>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    line: String,
    parse_error: String,
    #[serde(default)]
    strings: BTreeMap<String, String>,
    body: String,
    canonical: String,
    marshaled: String,
    verify_error: String,
}

#[derive(Deserialize)]
struct ChainCase {
    id: String,
    author: String,
    content: String,
    code: String,
    error: String,
}

#[test]
fn go_outer_surrogate_event_boundary() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/room/outer-surrogate.json"
    ))
    .expect("Go-owned outer surrogate fixture parses");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 37, "nonempty, complete oracle corpus");
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    assert_eq!(fixture.source_hashes.len(), 5);
    for source in [
        "internal/room/event/event.go",
        "internal/room/identity/identity.go",
        "internal/room/journal/journal.go",
        "internal/room/room/port_identity_event_contract_test.go",
        "internal/room/room/port_outer_surrogate_contract_test.go",
    ] {
        assert_eq!(
            hex::encode(Sha256::digest(fs::read(root.join(source)).unwrap())),
            fixture.source_hashes[source],
            "{source}: Go oracle source drift"
        );
    }
    let public_key = hex::decode(fixture.public_key).unwrap();
    let (mut verified, mut invalid_signature, mut rejected) = (0, 0, 0);
    for case in fixture.cases {
        let parsed = Event::unmarshal_json_line(case.line.as_bytes());
        // Go and serde retain their own parse diagnostics; this slice compares
        // acceptance, decoded strings and exact signed/written bytes.
        assert_eq!(
            parsed.is_ok(),
            case.parse_error.is_empty(),
            "{}: Go parse error {:?}, Rust {parsed:?}",
            case.id,
            case.parse_error
        );
        let Ok(event) = parsed else {
            rejected += 1;
            continue;
        };
        assert_eq!(case.strings.len(), 7, "{}: all outer strings", case.id);
        for (field, actual) in [
            ("id", event.id.as_str()),
            ("room", event.room.as_str()),
            ("author", event.author.as_str()),
            ("prev", event.prev.as_str()),
            ("ts", event.ts.as_str()),
            ("kind", event.kind.as_str()),
            ("sig", event.sig.as_deref().unwrap_or_default()),
        ] {
            assert_eq!(actual, case.strings[field], "{}: {field}", case.id);
        }
        assert_eq!(event.body.get(), case.body, "{}: raw body", case.id);
        assert_eq!(
            event::canonical_bytes(&event).unwrap(),
            case.canonical.as_bytes(),
            "{}: canonical signed bytes",
            case.id
        );
        assert_eq!(
            event.marshal_json_line().unwrap(),
            case.marshaled.as_bytes(),
            "{}: journal line bytes",
            case.id
        );
        let verify_error = event
            .verify_signature(&public_key)
            .err()
            .map(|error| error.to_string())
            .unwrap_or_default();
        assert_eq!(verify_error, case.verify_error, "{}: signature", case.id);
        if verify_error.is_empty() {
            verified += 1;
        } else {
            invalid_signature += 1;
        }
    }
    assert_eq!((verified, invalid_signature, rejected), (24, 6, 7));
    println!(
        "37 Go vectors: {verified} verified, {invalid_signature} invalid signatures, {rejected} rejected inputs"
    );
    assert_eq!(fixture.chain_cases.len(), 2, "Go journal case inventory");
    for case in fixture.chain_cases {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("after epoch")
            .as_nanos();
        let room = std::env::temp_dir().join(format!(
            "symroom-outer-surrogate-{}-{stamp}-{}",
            std::process::id(),
            case.id
        ));
        fs::create_dir_all(room.join("journal")).expect("create scratch journal");
        fs::write(
            room.join("journal").join(format!("{}.jsonl", case.author)),
            case.content,
        )
        .expect("write Go segment bytes");
        let (code, message) = match verify_chain(&room, &case.author) {
            Ok(()) => ("ok", String::new()),
            Err(error) => {
                let code = match error {
                    VerifyChainError::Previous { .. } => "chain_broken",
                    _ => panic!("{}: unexpected chain result: {error}", case.id),
                };
                (code, error.to_string())
            }
        };
        assert_eq!(code, case.code, "{}: chain outcome", case.id);
        assert_eq!(message, case.error, "{}: chain diagnostic", case.id);
        fs::remove_dir_all(&room).expect("remove scratch journal");
    }
}
