//! CONTACT-002: replays the Go-owned memory-link fixture
//! (`testdata/port/contacts/memorylink.json`).
#[path = "support/diff.rs"]
mod diff;
use serde_json::{Value, json};
use symdesk_contacts::contact::{Entity, Error};
use symdesk_contacts::memorylink::Service;

fn fixture() -> Value {
    serde_json::from_str(include_str!("../../../testdata/port/contacts/memorylink.json")).unwrap()
}

fn run(fixture: &Value) -> usize {
    let db = symdesk_contacts::open_memory().unwrap();
    db.execute_batch(fixture["seed"].as_str().unwrap()).unwrap();
    let mut ids = fixture["ids"].as_array().unwrap().iter();
    let stamp = fixture["timestamp"].as_str().unwrap();
    let actions = fixture["actions"].as_array().unwrap();
    assert!(actions.len() >= 30, "corpus shrank to {}", actions.len());
    assert_eq!(actions.len(), fixture["results"].as_array().unwrap().len());
    for (i, action) in actions.iter().enumerate() {
        let mut svc = Service::new(
            &db,
            || ids.next().expect("Rust consumed more UUIDs than Go").as_str().unwrap().to_owned(),
            || stamp.to_owned(),
        );
        let op = action["op"].as_str().unwrap();
        let id = action["id"].as_str().unwrap();
        let input = if action["input"].is_null() { json!({}) } else { action["input"].clone() };
        let link = || serde_json::from_value(input.clone()).unwrap();
        let result: Result<Value, Error> = match op {
            "link_person" => svc.link(Entity::Person, id, &link()).map(|x| json!(x)),
            "link_org" => svc.link(Entity::Organization, id, &link()).map(|x| json!(x)),
            "get_person" => svc.get(Entity::Person, id).map(|x| json!(x)),
            "get_org" => svc.get(Entity::Organization, id).map(|x| json!(x)),
            "unlink_person" => svc.unlink(Entity::Person, id).map(|()| Value::Null),
            "unlink_org" => svc.unlink(Entity::Organization, id).map(|()| Value::Null),
            "delete_person" => db
                .execute("DELETE FROM persons WHERE id = ?", [id])
                .map(|_| Value::Null)
                .map_err(|e| Error { kind: "internal", message: e.to_string() }),
            other => panic!("{other}"),
        };
        let mut actual = match result {
            Ok(value) => json!({"value": value, "error": "", "kind": ""}),
            Err(e) => json!({"value": null, "error": e.to_string(), "kind": e.kind}),
        };
        db.execute("UPDATE memory_links SET created_at=?1", [stamp]).unwrap();
        if let Some(created) = actual["value"].get_mut("CreatedAt") {
            *created = json!(stamp);
        }
        if actual != fixture["results"][i] {
            panic!("action {i}: {action}: {}", diff::first_difference("", &actual, &fixture["results"][i]));
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
                            rusqlite::types::ValueRef::Text(s) => json!(std::str::from_utf8(s).unwrap()),
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
fn go_contacts_memory_link_contract() {
    println!("contacts memory links: {} actions replayed", run(&fixture()));
}

#[test]
#[should_panic(expected = "action")]
fn negative_control_a_dropped_conflict_is_detected() {
    let mut tampered = fixture();
    let index = tampered["results"]
        .as_array()
        .unwrap()
        .iter()
        .position(|r| r["kind"] == "conflict")
        .unwrap();
    tampered["results"][index] = json!({"value": null, "error": "", "kind": ""});
    run(&tampered);
}
