use serde_json::{Value, json};
use std::collections::HashMap;
use symdesk_contacts::contact::{Entity, Error, Service};

#[test]
fn go_contacts_crud_contract() {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../testdata/port/contacts/crud.json")).unwrap();
    let db = symdesk_contacts::open_memory().unwrap();
    let mut ids = fixture["ids"].as_array().unwrap().iter();
    let stamp = fixture["timestamp"].as_str().unwrap();
    let mut svc = Service::new(
        &db,
        || ids.next().unwrap().as_str().unwrap().to_owned(),
        || stamp.to_owned(),
    );
    let mut refs: HashMap<String, String> = HashMap::new();
    let actions = fixture["actions"].as_array().unwrap();
    assert!(actions.len() >= 100);
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
        let value = input["value"].as_str().unwrap_or("");
        let result: Result<Value, Error> = match op {
            "create" => svc
                .create(entity, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "get" => svc.get(entity, &id).map(|x| json!(x)),
            "update" => svc
                .update(entity, &id, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "delete" => svc.delete(entity, &id).map(|_| Value::Null),
            "list" => svc
                .list(entity, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "alias" => svc.add_alias(entity, &id, value).map(|_| Value::Null),
            "tag" => svc.add_tag(entity, &id, value).map(|_| Value::Null),
            "classify" => svc
                .set_classification(entity, &id, value)
                .map(|_| Value::Null),
            "unclassify" => svc
                .remove_classification(entity, &id, value)
                .map(|_| Value::Null),
            "point" => svc
                .add_contact_point(entity, &id, &serde_json::from_value(input.clone()).unwrap())
                .map(|x| json!(x)),
            "remove_point" => svc
                .remove_contact_point(entity, &id, refs.get(value).map_or(value, String::as_str))
                .map(|_| Value::Null),
            other => panic!("{other}"),
        };
        let actual = match result {
            Ok(value) => {
                if let Some(id) = value["ID"].as_str() {
                    if op == "create" {
                        refs.insert(key.to_owned(), id.to_owned());
                    }
                    if op == "point" {
                        refs.insert(format!("point-{key}"), id.to_owned());
                    }
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
