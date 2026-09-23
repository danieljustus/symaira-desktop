#![deny(unsafe_code)]

//! Replays the Go-owned room identity/event vectors (contract row ROOM-001) —
//! fixture `testdata/port/room/identity-events.json`, written by
//! `internal/room/room/port_identity_event_contract_test.go`.
//!
//! Every comparison is byte for byte: member ids, canonical signed bytes, the
//! signature envelope, the JSON line and the identity file. A green unit test in
//! one language is not parity, so nothing here is compared structurally except
//! the fixture document itself.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};
use symroom_core::event::{self, Event};
use symroom_core::identity::{self, Identity, StoredIdentity};

#[derive(Deserialize)]
struct Fixture {
    schema_version: i64,
    generated_on: String,
    oracle: Oracle,
    source_hashes: BTreeMap<String, String>,
    identities: Vec<IdentityVector>,
    member_ids: Vec<MemberIdVector>,
    events: Vec<EventVector>,
    verify_cases: Vec<VerifyVector>,
    file_cases: Vec<FileVector>,
    notes: Vec<String>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct IdentityVector {
    label: String,
    seed_hex: String,
    public_key_hex: String,
    private_key_hex: String,
    member_id: String,
    stored_file: String,
    stored_size: usize,
    stored_mode: Option<u32>,
    stored_sha256: String,
}

#[derive(Deserialize)]
struct MemberIdVector {
    public_key_hex: String,
    member_id: String,
}

#[derive(Deserialize)]
struct EventVector {
    id: String,
    event: Box<RawValue>,
    canonical_bytes: String,
    canonical_sha256: String,
    signature: String,
    json_line: String,
    line_sha256: String,
    verified: bool,
    kind_known: bool,
}

#[derive(Deserialize)]
struct VerifyVector {
    id: String,
    event: Box<RawValue>,
    public_key_hex: String,
    error: String,
}

#[derive(Deserialize)]
struct FileVector {
    id: String,
    channel: String,
    names: Vec<String>,
    file_mode: Option<u32>,
    error: String,
    loaded_name: String,
    loaded_member_id: String,
    loaded_public_key_hex: String,
}

fn fixture_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/identity-events.json")
}

fn load_fixture() -> Fixture {
    let path = fixture_path();
    let data =
        fs::read_to_string(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&data).expect("fixture parses")
}

fn sha256_hex(data: &[u8]) -> String {
    hex::encode(Sha256::digest(data))
}

#[test]
fn room_identity_event_vectors_match_the_go_oracle() {
    let fixture = load_fixture();

    assert_eq!(fixture.schema_version, 1, "fixture schema version");
    // File modes are only observable on the platform that generated the fixture;
    // elsewhere the Go drift check clears them on both sides.
    // Go names the platform "darwin" where Rust says "macos".
    let goos = match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    };
    let modes_observable = fixture.generated_on == goos;
    assert!(
        fixture.oracle.commit.len() >= 40 && !fixture.oracle.release.is_empty(),
        "fixture must name the pinned oracle"
    );
    for source in [
        "internal/room/identity/identity.go",
        "internal/room/event/event.go",
    ] {
        assert!(
            fixture.source_hashes.contains_key(source),
            "source digest for {source} is missing"
        );
    }
    assert!(
        !fixture.notes.is_empty(),
        "fixture documents its own limits"
    );

    // A Go vector that cannot be replayed must not be silently skipped.
    let runnable = fixture.identities.len()
        + fixture.member_ids.len()
        + fixture.events.len()
        + fixture.verify_cases.len()
        + fixture.file_cases.len();
    assert!(runnable > 0, "no vectors to replay");
    assert_eq!(fixture.events.len(), 28, "signed Go event vector inventory");
    for required in ["body-html-unicode", "body-escaped-html"] {
        assert!(
            fixture.events.iter().any(|row| row.id == required),
            "missing {required}"
        );
    }

    let mut by_member: BTreeMap<String, Identity> = BTreeMap::new();
    for vector in &fixture.identities {
        let seed = hex::decode(&vector.seed_hex).expect("seed hex");
        let built = identity::identity_from_private_key(&vector.label, &seed)
            .unwrap_or_else(|| panic!("identity {} builds from its seed", vector.label));
        assert_eq!(
            hex::encode(&built.public_key),
            vector.public_key_hex,
            "identity {} public key",
            vector.label
        );
        assert_eq!(
            hex::encode(&built.private_key),
            vector.private_key_hex,
            "identity {} private key",
            vector.label
        );
        assert_eq!(
            built.member_id, vector.member_id,
            "identity {} member id",
            vector.label
        );
        assert_eq!(
            identity::compute_member_id(&built.public_key),
            vector.member_id,
            "identity {} member id is derived from the public key",
            vector.label
        );

        let stored = StoredIdentity {
            name: built.name.clone(),
            member_id: built.member_id.clone(),
            public_key: vector.public_key_hex.clone(),
            private_key: vector.private_key_hex.clone(),
        };
        let encoded = serde_json::to_string_pretty(&stored).expect("identity file encodes");
        assert_eq!(
            encoded, vector.stored_file,
            "identity {} stored file bytes",
            vector.label
        );
        assert_eq!(
            encoded.len(),
            vector.stored_size,
            "identity {} stored file size",
            vector.label
        );
        assert_eq!(
            sha256_hex(encoded.as_bytes()),
            vector.stored_sha256,
            "identity {} stored file digest",
            vector.label
        );
        if modes_observable {
            assert_eq!(
                vector.stored_mode,
                Some(0o600),
                "identity {} records its file mode",
                vector.label
            );
        } else {
            // The recording platform's value is not comparable here; the Go
            // drift check clears it on both sides for exactly that reason.
            let _ = vector.stored_mode;
        }

        by_member.insert(built.member_id.clone(), built);
    }

    for vector in &fixture.member_ids {
        let public_key = hex::decode(&vector.public_key_hex).expect("public key hex");
        assert_eq!(
            identity::compute_member_id(&public_key),
            vector.member_id,
            "member id for {}",
            vector.public_key_hex
        );
    }

    let mut events_replayed = 0usize;
    for vector in &fixture.events {
        let mut parsed: Event =
            serde_json::from_str(vector.event.get()).expect("event parses into the port type");
        let signer = by_member.get(&parsed.author).unwrap_or_else(|| {
            panic!(
                "{}: author {} is a fixture identity",
                vector.id, parsed.author
            )
        });

        let canonical = event::canonical_bytes(&parsed).expect("canonical bytes");
        assert_eq!(
            String::from_utf8_lossy(&canonical),
            vector.canonical_bytes,
            "{}: canonical bytes",
            vector.id
        );
        assert_eq!(
            sha256_hex(&canonical),
            vector.canonical_sha256,
            "{}: canonical digest",
            vector.id
        );

        parsed.sign(signer).expect("sign");
        assert_eq!(
            parsed.sig.as_deref().unwrap_or_default(),
            vector.signature,
            "{}: signature",
            vector.id
        );

        let line = parsed.marshal_json_line().expect("json line");
        assert_eq!(
            String::from_utf8_lossy(&line),
            vector.json_line,
            "{}: json line",
            vector.id
        );
        assert_eq!(
            sha256_hex(&line),
            vector.line_sha256,
            "{}: json line digest",
            vector.id
        );

        let round_trip =
            Event::unmarshal_json_line(vector.json_line.as_bytes()).expect("json line parses");
        assert_eq!(round_trip.id, parsed.id, "{}: round trip id", vector.id);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(round_trip.body.get()).unwrap(),
            serde_json::from_str::<serde_json::Value>(parsed.body.get()).unwrap(),
            "{}: round trip body",
            vector.id
        );
        assert_eq!(
            round_trip.sig, parsed.sig,
            "{}: round trip signature",
            vector.id
        );

        let verification = parsed.verify_signature(&signer.public_key);
        assert_eq!(
            verification.is_ok(),
            vector.verified,
            "{}: verification expectation",
            vector.id
        );
        assert_eq!(
            event::KNOWN_KINDS.contains(&parsed.kind.as_str()),
            vector.kind_known,
            "{}: known kind",
            vector.id
        );
        events_replayed += 1;
    }
    assert_eq!(events_replayed, fixture.events.len());

    for vector in &fixture.verify_cases {
        let parsed: Event =
            serde_json::from_str(vector.event.get()).expect("event parses into the port type");
        let public_key = hex::decode(&vector.public_key_hex).expect("public key hex");
        let message = match parsed.verify_signature(&public_key) {
            Ok(()) => String::new(),
            Err(err) => err.to_string(),
        };
        assert_eq!(message, vector.error, "{}: verification error", vector.id);
    }

    replay_file_cases(&fixture, modes_observable);
}

/// The identity file cases run in a private data directory each, so the Go
/// vectors stay reproducible on any machine.
fn replay_file_cases(fixture: &Fixture, modes_observable: bool) {
    let by_public: BTreeMap<String, &IdentityVector> = fixture
        .identities
        .iter()
        .map(|vector| (vector.public_key_hex.clone(), vector))
        .collect();

    for vector in &fixture.file_cases {
        let data_home = temp_data_home(&vector.id);
        set_env("XDG_DATA_HOME", &data_home.to_string_lossy());
        set_env("SYMROOM_IDENTITY_KEY", "");

        match vector.id.as_str() {
            "save-list-load" => {
                for label in ["beta", "alpha"] {
                    let seed =
                        hex::decode(&by_public_identity(fixture, label).seed_hex).expect("seed");
                    let identity =
                        identity::identity_from_private_key(label, &seed).expect("identity");
                    identity::save(&identity).expect("save");
                }
                let names = identity::list().expect("list");
                assert_eq!(names, vector.names, "{}: listed names", vector.id);
                let loaded = identity::load("alpha").expect("load");
                assert_eq!(
                    loaded.name, vector.loaded_name,
                    "{}: loaded name",
                    vector.id
                );
                assert_eq!(
                    loaded.member_id, vector.loaded_member_id,
                    "{}: loaded member id",
                    vector.id
                );
                assert_eq!(
                    hex::encode(&loaded.public_key),
                    vector.loaded_public_key_hex,
                    "{}: loaded public key",
                    vector.id
                );
                if modes_observable {
                    let mode = file_mode(&data_home.join("symroom/identities/alpha.json"));
                    assert_eq!(Some(mode), vector.file_mode, "{}: file mode", vector.id);
                } else {
                    let _ = vector.file_mode;
                }
            }
            "missing-identity" => {
                let message = identity::load("does-not-exist")
                    .expect_err("missing identity fails")
                    .to_string();
                assert_eq!(message, vector.error, "{}: error", vector.id);
            }
            "invalid-key" => {
                let dir = data_home.join("symroom").join("identities");
                fs::create_dir_all(&dir).expect("create identities dir");
                fs::write(
                    dir.join("broken.json"),
                    r#"{"name":"broken","public_key":"nope","private_key":"nope"}"#,
                )
                .expect("write corrupt identity");
                let message = identity::load("broken")
                    .expect_err("corrupt identity fails")
                    .to_string();
                assert_eq!(message, vector.error, "{}: error", vector.id);
            }
            "env-seed" | "env-private-key" => {
                let source = by_public
                    .get(&vector.loaded_public_key_hex)
                    .unwrap_or_else(|| panic!("{}: names a fixture identity", vector.id));
                let value = if vector.id == "env-seed" {
                    source.seed_hex.clone()
                } else {
                    source.private_key_hex.clone()
                };
                set_env("SYMROOM_IDENTITY_KEY", &value);
                let name = if vector.id == "env-seed" {
                    "alpha"
                } else {
                    "beta"
                };
                let loaded = identity::load(name).expect("load from environment");
                assert_eq!(
                    loaded.name, vector.loaded_name,
                    "{}: loaded name",
                    vector.id
                );
                assert_eq!(
                    loaded.member_id, vector.loaded_member_id,
                    "{}: loaded member id",
                    vector.id
                );
                assert_eq!(
                    hex::encode(&loaded.public_key),
                    vector.loaded_public_key_hex,
                    "{}: loaded public key",
                    vector.id
                );
            }
            "env-invalid-falls-through" => {
                set_env("SYMROOM_IDENTITY_KEY", "not-hex");
                let message = identity::load("alpha")
                    .expect_err("unusable environment key falls through")
                    .to_string();
                assert_eq!(message, vector.error, "{}: error", vector.id);
            }
            other => panic!("fixture case {other} is not replayed"),
        }
        assert!(!vector.channel.is_empty(), "{}: channel", vector.id);
    }
}

fn by_public_identity<'a>(fixture: &'a Fixture, label: &str) -> &'a IdentityVector {
    fixture
        .identities
        .iter()
        .find(|vector| vector.label == label)
        .unwrap_or_else(|| panic!("fixture identity {label} is missing"))
}

fn temp_data_home(id: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("symroom-port-{id}-{}", std::process::id()));
    let _ = fs::remove_dir_all(&dir);
    fs::create_dir_all(&dir).expect("create data home");
    dir
}

/// Seeds the environment chains under test. Rust 2024 marks `set_var` unsafe
/// because a concurrently reading thread makes it unsound; this test binary runs
/// one single-threaded `#[test]`, so the mutation is confined to it. The crate
/// itself stays `#![deny(unsafe_code)]`.
#[allow(unsafe_code)]
fn set_env(key: &str, value: &str) {
    unsafe { std::env::set_var(key, value) };
}

#[cfg(unix)]
fn file_mode(path: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    fs::metadata(path).expect("metadata").permissions().mode() & 0o777
}

#[cfg(not(unix))]
fn file_mode(_path: &Path) -> u32 {
    0
}
