use serde_json::{Value, json};
use symdesk_contacts::{contact::Entity, security::Service};

#[test]
fn go_contacts_erase_contract() {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../testdata/port/contacts/erase.json")).unwrap();
    let stamp = fixture["timestamp"].as_str().unwrap();
    let actor = fixture["actor"].as_str().unwrap();
    let audit_id = fixture["audit_id"].as_str().unwrap();
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 6);
    for case in cases {
        let db = symdesk_contacts::open_memory().unwrap();
        db.execute_batch(fixture["seed"].as_str().unwrap()).unwrap();
        // Pin only the schema's creation/update defaults, as the Go oracle does.
        for query in fixture["queries"].as_array().unwrap() {
            let table = query.as_str().unwrap().split_whitespace().nth(3).unwrap();
            let columns: Vec<String> = db
                .prepare(&format!("PRAGMA table_info({table})"))
                .unwrap()
                .query_map([], |r| r.get(1))
                .unwrap()
                .collect::<rusqlite::Result<_>>()
                .unwrap();
            for column in columns
                .iter()
                .filter(|c| ["created_at", "updated_at", "imported_at"].contains(&c.as_str()))
            {
                db.execute(&format!("UPDATE {table} SET {column}=?"), [stamp])
                    .unwrap();
            }
        }
        db.execute_batch(case["setup"].as_str().unwrap()).unwrap();
        let mut svc = Service::new(
            &db,
            || audit_id.to_owned(),
            || stamp.to_owned(),
            || actor.to_owned(),
        );
        let entity = match case["entity"].as_str().unwrap() {
            "person" => Entity::Person,
            "organization" => Entity::Organization,
            other => panic!("{other}"),
        };
        let (value, error, kind) = match svc.erase(entity, case["id"].as_str().unwrap()) {
            Ok(summary) => (json!(summary), String::new(), ""),
            Err(e) => (Value::Null, e.to_string(), e.kind),
        };
        assert_eq!(value, case["value"], "{case}");
        assert_eq!(error, case["error"]);
        assert_eq!(kind, case["kind"]);
        let audits = json!(svc.list_audit_events().unwrap());
        assert_eq!(audits, case["audits"]);
        assert!(
            !audits.to_string().contains("PRIVATE_"),
            "audit leaked contact values"
        );
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
            assert_eq!(
                json!(rows),
                case["snapshots"][i],
                "case {} / {}: {query}",
                case["entity"],
                case["mode"]
            );
        }
    }
}
