#![deny(unsafe_code)]

use serde::Deserialize;
use sha2::{Digest, Sha256};
use symroom_core::{
    event::{self, Event},
    identity,
    members::State,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    line: String,
    canonical: String,
    verified: bool,
    members: usize,
    name: String,
    parse_ok: bool,
}

#[test]
fn go_surrogate_event_boundary() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/room/surrogate.json"))
            .expect("Go-owned surrogate fixture parses");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 6);
    let seed = Sha256::digest(b"symroom-port-surrogate-v1");
    let identity = identity::identity_from_private_key("surrogate", &seed)
        .expect("deterministic Go fixture signer");
    for case in fixture.cases {
        let parsed = Event::unmarshal_json_line(case.line.as_bytes());
        assert_eq!(parsed.is_ok(), case.parse_ok, "{}: parse", case.id);
        if let Ok(event) = parsed {
            assert_eq!(
                event::canonical_bytes(&event).unwrap(),
                case.canonical.as_bytes(),
                "{}: original signed bytes",
                case.id
            );
            assert_eq!(
                event.verify_signature(&identity.public_key).is_ok(),
                case.verified,
                "{}: Go signature",
                case.id
            );
            let mut state = State::default();
            assert_eq!(
                state.apply_event(&event).is_ok(),
                case.verified,
                "{}: projection",
                case.id
            );
            assert_eq!(state.members.len(), case.members, "{}: members", case.id);
            assert_eq!(
                state
                    .members
                    .get(&event.author)
                    .map(|member| member.name.as_str()),
                Some(case.name.as_str()),
                "{}: decoded name",
                case.id
            );
        }
    }
}
