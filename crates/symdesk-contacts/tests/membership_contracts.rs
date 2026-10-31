//! CONTACT-001: replays the Go-owned membership / quick-add / reference
//! fixture (`testdata/port/contacts/membership-ref.json`).
#[path = "support/diff.rs"]
mod diff;
use serde_json::{Value, json};
use std::collections::HashMap;
use symdesk_contacts::contact::{Entity, Error, ListOptions, Service};
use symdesk_contacts::membership::MembershipInput;

fn snapshot(db: &rusqlite::Connection, query: &str) -> Vec<Vec<Value>> {
    let mut stmt = db.prepare(query).unwrap();
    let width = stmt.column_count();
    stmt.query_map([], |r| {
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
    .unwrap()
}

fn normalize_timestamps(db: &rusqlite::Connection, stamp: &str) {
    for sql in [
        "UPDATE persons SET created_at=?1, updated_at=?1",
        "UPDATE organizations SET created_at=?1, updated_at=?1",
        "UPDATE organization_memberships SET created_at=?1, updated_at=?1",
        "UPDATE contact_points SET created_at=?1, updated_at=?1",
        "UPDATE aliases SET created_at=?1",
    ] {
        db.execute(sql, [stamp]).unwrap();
    }
}

/// Go normalizes every `CreatedAt`/`UpdatedAt` JSON field to one instant.
fn normalize(value: &mut Value, stamp: &str) {
    match value {
        Value::Object(map) => {
            for (key, item) in map.iter_mut() {
                if key == "CreatedAt" || key == "UpdatedAt" {
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

fn run_fixture(fixture: &Value, skip_check: Option<(&str, &str)>) -> usize {
    let db = symdesk_contacts::open_memory().unwrap();
    let mut ids = fixture["ids"].as_array().unwrap().iter();
    let stamp = fixture["timestamp"].as_str().unwrap();
    let mut refs: HashMap<String, String> = HashMap::new();
    let actions = fixture["actions"].as_array().unwrap();
    assert!(actions.len() >= 60, "corpus shrank to {}", actions.len());
    assert_eq!(actions.len(), fixture["results"].as_array().unwrap().len());
    for (i, action) in actions.iter().enumerate() {
        let mut svc = Service::new(
            &db,
            || ids.next().expect("Rust consumed more UUIDs than Go").as_str().unwrap().to_owned(),
            || stamp.to_owned(),
        );
        let op = action["op"].as_str().unwrap();
        let key = action["id"].as_str().unwrap();
        let resolve = |key: &str| refs.get(key).map_or(key, String::as_str).to_owned();
        let id = resolve(key);
        let second = resolve(action["second"].as_str().unwrap_or(""));
        let input = if action["input"].is_null() { json!({}) } else { action["input"].clone() };
        fn from<T: serde::de::DeserializeOwned>(v: &Value) -> T {
            serde_json::from_value(v.clone()).unwrap()
        }
        let result: Result<Value, Error> = match op {
            "create_person" => svc.create(Entity::Person, &from(&input)).map(|x| json!(x)),
            "create_org" => svc.create(Entity::Organization, &from(&input)).map(|x| json!(x)),
            "quickadd" => svc
                .create_person_with_contact_points(
                    &from(&input),
                    input["email"].as_str().unwrap_or(""),
                    input["phone"].as_str().unwrap_or(""),
                )
                .map(|x| json!(x)),
            "get_person" => svc.get(Entity::Person, &id).map(|x| json!(x)),
            "persons" => svc.list(Entity::Person, &ListOptions::default()).map(|x| json!(x)),
            "membership" => svc.add_membership(&id, &second, &from::<MembershipInput>(&input)).map(|x| json!(x)),
            "end_membership" => svc
                .end_membership(&id, input["ValidTo"].as_str().unwrap())
                .map(|x| json!(x)),
            "memberships_person" => svc.list_memberships_by_person(&id).map(|x| json!(x)),
            "memberships_org" => svc.list_memberships_by_organization(&id).map(|x| json!(x)),
            "get_ref" => svc.get_ref(&id).map(|x| json!(x)),
            "find_refs" => svc.find_refs_by_name(key).map(|x| json!(x)),
            "delete_person" => svc.delete(Entity::Person, &id).map(|()| Value::Null),
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
            Err(e) => json!({"value": null, "error": e.to_string(), "kind": e.kind}),
        };
        normalize_timestamps(&db, stamp);
        let mut expected = fixture["results"][i].clone();
        if let Some((name, replacement)) = skip_check {
            if op == name {
                expected["error"] = json!(replacement);
            }
        }
        if actual != expected {
            panic!("action {i}: {action}: {}", diff::first_difference("", &actual, &expected));
        }
    }
    assert!(ids.next().is_none(), "UUID consumption differs");
    for (i, query) in fixture["queries"].as_array().unwrap().iter().enumerate() {
        assert_eq!(
            json!(snapshot(&db, query.as_str().unwrap())),
            fixture["snapshots"][i],
            "snapshot {query}"
        );
    }
    actions.len()
}

fn fixture() -> Value {
    serde_json::from_str(include_str!("../../../testdata/port/contacts/membership-ref.json")).unwrap()
}

#[test]
fn go_contacts_membership_ref_contract() {
    let executed = run_fixture(&fixture(), None);
    println!("contacts membership/ref: {executed} actions replayed");
}

#[test]
#[should_panic(expected = "action")]
fn negative_control_a_tampered_expectation_is_detected() {
    let mut tampered = fixture();
    let first_ref = tampered["actions"]
        .as_array()
        .unwrap()
        .iter()
        .position(|a| a["op"] == "get_ref")
        .unwrap();
    tampered["results"][first_ref]["value"]["display_name"] = json!("Mallory");
    run_fixture(&tampered, None);
}
