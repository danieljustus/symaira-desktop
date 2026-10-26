use serde_json::{Value, json};
use std::collections::HashMap;
use symdesk_contacts::contact::{Entity, Error};
use symdesk_contacts::relationship::{Direction, Service, Transition};

#[test]
fn go_contacts_relationship_contract() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../testdata/port/contacts/relationship.json"
    ))
    .unwrap();
    let db = symdesk_contacts::open_memory().unwrap();
    db.execute_batch(fixture["seed"].as_str().unwrap()).unwrap();
    let mut ids = fixture["ids"].as_array().unwrap().iter();
    let stamp = fixture["timestamp"].as_str().unwrap();
    let mut svc = Service::new(
        &db,
        || ids.next().unwrap().as_str().unwrap().to_owned(),
        || stamp.to_owned(),
    );
    let mut refs: HashMap<String, String> = HashMap::new();
    let actions = fixture["actions"].as_array().unwrap();
    assert!(actions.len() >= 90);
    assert_eq!(actions.len(), fixture["results"].as_array().unwrap().len());
    for (i, action) in actions.iter().enumerate() {
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
            "relationship" => svc
                .add_relationship(&serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "outgoing" => svc
                .list_relationships(Direction::Outgoing, &id)
                .map(|x| json!(x)),
            "incoming_person" => svc
                .list_relationships(Direction::IncomingPerson, &id)
                .map(|x| json!(x)),
            "incoming_org" => svc
                .list_relationships(Direction::IncomingOrganization, &id)
                .map(|x| json!(x)),
            "followup" => svc
                .add_follow_up(entity, &id, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "complete" => svc
                .transition_follow_up(&id, Transition::Complete)
                .map(|x| json!(x)),
            "cancel" => svc
                .transition_follow_up(&id, Transition::Cancel)
                .map(|x| json!(x)),
            "followups" => svc
                .list_follow_ups(entity, &id, input["filter"].as_str().unwrap_or(""))
                .map(|x| json!(x)),
            "interaction" => svc
                .add_interaction(entity, &id, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "interactions" => svc.list_interactions(entity, &id).map(|x| json!(x)),
            "last" => svc.last_interaction(entity, &id).map(|x| json!(x)),
            other => panic!("{other}"),
        };
        let actual = match result {
            Ok(value) => {
                if let (Some(id), Some(key)) = (value["ID"].as_str(), input["ref"].as_str()) {
                    refs.insert(key.to_owned(), id.to_owned());
                }
                json!({"value":value,"error":"","kind":""})
            }
            Err(e) => json!({"value":null,"error":e.to_string(),"kind":e.kind}),
        };
        assert_eq!(actual, fixture["results"][i], "action {i}: {action}");
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
}
