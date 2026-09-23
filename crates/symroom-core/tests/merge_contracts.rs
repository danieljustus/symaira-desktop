#![deny(unsafe_code)]

use std::collections::BTreeMap;

use serde::Deserialize;
use symroom_core::{event::Event, journal::merge};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hash: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    segments: BTreeMap<String, Vec<Event>>,
    ordered: Vec<String>,
    #[serde(default)]
    bodies: Vec<String>,
}

#[test]
fn go_total_order_and_stability() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/room/merge.json"))
            .expect("Go-owned merge fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hash.len(), 64);
    assert_eq!(fixture.cases.len(), 3);
    for case in fixture.cases {
        let events = merge(case.segments);
        assert_eq!(
            events.iter().map(|event| &event.id).collect::<Vec<_>>(),
            case.ordered.iter().collect::<Vec<_>>(),
            "{}: Go event order",
            case.id
        );
        if !case.bodies.is_empty() {
            // MarshalIndent reformats RawMessage whitespace inside the fixture;
            // the payload marker, not that formatting, tests stable tie order.
            assert_eq!(
                events
                    .iter()
                    .map(
                        |event| serde_json::from_str::<serde_json::Value>(event.body.get())
                            .unwrap()
                    )
                    .collect::<Vec<_>>(),
                case.bodies
                    .iter()
                    .map(|body| serde_json::from_str::<serde_json::Value>(body).unwrap())
                    .collect::<Vec<_>>(),
                "{}: stable order for equal keys",
                case.id
            );
        }
    }
}
