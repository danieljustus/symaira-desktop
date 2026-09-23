use std::collections::BTreeSet;

use serde::Deserialize;
use serde_json::Value;
use symroom_core::{
    event::Event,
    members::{Member, State},
};

const GO_FIXTURE: &str = include_str!("../../../testdata/port/room/membership.json");

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    permissions: Vec<Permission>,
    transitions: Vec<Transition>,
}

#[derive(Deserialize)]
struct Permission {
    role: String,
    action: String,
    allowed: bool,
}

#[derive(Deserialize)]
struct Transition {
    id: String,
    kind: String,
    author: String,
    body: Value,
    error: String,
    members: Vec<Member>,
}

#[test]
fn go_membership_projection_and_permissions() {
    let fixture: Fixture = serde_json::from_str(GO_FIXTURE).expect("Go-owned membership fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.permissions.len(), 25);
    assert_eq!(fixture.transitions.len(), 16);

    for case in fixture.permissions {
        let member = Member {
            id: String::new(),
            name: String::new(),
            public_key: String::new(),
            role: case.role.clone(),
            kind: String::new(),
        };
        assert_eq!(
            member.can_perform(&case.action),
            case.allowed,
            "role={} action={}",
            case.role,
            case.action
        );
    }

    let mut state = State::default();
    let mut seen = BTreeSet::new();
    for case in fixture.transitions {
        assert!(
            seen.insert(case.id.clone()),
            "duplicate Go case ID: {}",
            case.id
        );
        let event = Event {
            v: 1,
            id: case.id.clone(),
            room: "room".into(),
            author: case.author,
            seq: 0,
            prev: String::new(),
            lamport: 0,
            ts: String::new(),
            kind: case.kind,
            body: serde_json::value::RawValue::from_string(case.body.to_string())
                .expect("valid Go event body"),
            sig: None,
        };
        let result = state.apply_event(&event);
        assert_eq!(
            result.err().unwrap_or_default(),
            case.error,
            "{}: error",
            case.id
        );
        assert_eq!(
            state.members.values().cloned().collect::<Vec<_>>(),
            case.members,
            "{}: state",
            case.id
        );
    }
}
