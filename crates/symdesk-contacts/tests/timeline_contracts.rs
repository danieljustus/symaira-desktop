//! CONTACT-001/002: replays the Go-owned timeline and reviewed-meeting-import
//! fixture (`testdata/port/contacts/timeline-meeting.json`).
#[path = "support/diff.rs"]
mod diff;
use serde_json::{Value, json};
use std::collections::HashMap;
use symdesk_contacts::contact::{Entity, Error};
use symdesk_contacts::relationship::{MeetingImportInput, Service, Transition};

fn fixture() -> Value {
    serde_json::from_str(include_str!(
        "../../../testdata/port/contacts/timeline-meeting.json"
    ))
    .unwrap()
}

fn normalize(value: &mut Value, stamp: &str) {
    match value {
        Value::Object(map) => {
            for (key, item) in map.iter_mut() {
                if matches!(
                    key.as_str(),
                    "CreatedAt" | "UpdatedAt" | "CompletedAt" | "CancelledAt"
                ) && !item.is_null()
                {
                    *item = json!(stamp);
                } else {
                    normalize(item, stamp);
                }
            }
        }
        Value::Array(items) => items.iter_mut().for_each(|item| normalize(item, stamp)),
        _ => {}
    }
}

fn run(fixture: &Value) -> usize {
    let db = symdesk_contacts::open_memory().unwrap();
    db.execute_batch(fixture["seed"].as_str().unwrap()).unwrap();
    let mut ids = fixture["ids"].as_array().unwrap().iter();
    let stamp = fixture["timestamp"].as_str().unwrap();
    let mut refs: HashMap<String, String> = HashMap::new();
    let actions = fixture["actions"].as_array().unwrap();
    assert!(actions.len() >= 50, "corpus shrank to {}", actions.len());
    assert_eq!(actions.len(), fixture["results"].as_array().unwrap().len());
    for (i, action) in actions.iter().enumerate() {
        let mut svc = Service::new(
            &db,
            || {
                ids.next()
                    .expect("Rust consumed more UUIDs than Go")
                    .as_str()
                    .unwrap()
                    .to_owned()
            },
            || stamp.to_owned(),
        );
        let entity = match action["entity"].as_str().unwrap() {
            "person" => Entity::Person,
            "organization" => Entity::Organization,
            other => panic!("{other}"),
        };
        let op = action["op"].as_str().unwrap();
        let key = action["id"].as_str().unwrap();
        let id = refs.get(key).map_or(key, String::as_str).to_owned();
        let input = if action["input"].is_null() {
            json!({})
        } else {
            action["input"].clone()
        };
        let result: Result<Value, Error> = match op {
            "interaction" => svc
                .add_interaction(entity, &id, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "followup" => svc
                .add_follow_up(entity, &id, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "timeline" => svc.timeline(entity, &id).map(|x| json!(x)),
            "complete" => svc
                .transition_follow_up(&id, Transition::Complete)
                .map(|x| json!(x)),
            "meeting" => {
                let meeting: MeetingImportInput = serde_json::from_value(input.clone()).unwrap();
                svc.import_meeting(entity, &id, &meeting)
                    .map(|(interaction, created)| json!({"interaction": interaction, "created": created}))
            }
            other => panic!("{other}"),
        };
        let actual = match result {
            Ok(mut value) => {
                normalize(&mut value, stamp);
                if let (Some(id), Some(key)) = (value["ID"].as_str(), input["ref"].as_str()) {
                    refs.insert(key.to_owned(), id.to_owned());
                }
                json!({"value": value, "error": "", "kind": ""})
            }
            Err(e) if op == "meeting" => {
                json!({"value": {"created": false, "interaction": null}, "error": e.to_string(), "kind": e.kind})
            }
            Err(e) => json!({"value": null, "error": e.to_string(), "kind": e.kind}),
        };
        for sql in [
            "UPDATE interactions SET created_at=?1",
            "UPDATE follow_ups SET created_at=?1, updated_at=?1",
            "UPDATE follow_ups SET completed_at=?1 WHERE completed_at IS NOT NULL",
            "UPDATE follow_ups SET cancelled_at=?1 WHERE cancelled_at IS NOT NULL",
        ] {
            db.execute(sql, [stamp]).unwrap();
        }
        if actual != fixture["results"][i] {
            panic!(
                "action {i}: {action}: {}",
                diff::first_difference("", &actual, &fixture["results"][i])
            );
        }
    }
    assert!(ids.next().is_none(), "UUID consumption differs");
    for (i, query) in fixture["queries"].as_array().unwrap().iter().enumerate() {
        let mut stmt = db.prepare(query.as_str().unwrap()).unwrap();
        let width = stmt.column_count();
        let rows: Vec<Vec<Value>> = stmt
            .query_map([], |r| {
                (0..width)
                    .map(|c| {
                        Ok(match r.get_ref(c)? {
                            rusqlite::types::ValueRef::Null => Value::Null,
                            rusqlite::types::ValueRef::Text(s) => {
                                json!(std::str::from_utf8(s).unwrap())
                            }
                            rusqlite::types::ValueRef::Integer(n) => json!(n),
                            other => panic!("unsupported cell {other:?}"),
                        })
                    })
                    .collect()
            })
            .unwrap()
            .collect::<rusqlite::Result<_>>()
            .unwrap();
        assert_eq!(json!(rows), fixture["snapshots"][i], "snapshot {query}");
    }
    actions.len()
}

#[test]
fn go_contacts_timeline_meeting_contract() {
    println!(
        "contacts timeline/meeting: {} actions replayed",
        run(&fixture())
    );
}

#[test]
#[should_panic(expected = "action")]
fn negative_control_a_reordered_timeline_is_detected() {
    let mut tampered = fixture();
    let index = tampered["actions"]
        .as_array()
        .unwrap()
        .iter()
        .rposition(|a| a["op"] == "timeline" && a["entity"] == "person" && a["id"] == "p")
        .unwrap();
    tampered["results"][index]["value"]
        .as_array_mut()
        .unwrap()
        .reverse();
    run(&tampered);
}
