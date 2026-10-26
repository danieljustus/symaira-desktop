use serde_json::{Value, json};
use symdesk_contacts::{
    open_memory,
    pipeline::{self, ImportPlan, RowResolution},
};
#[test]
fn go_import_plan_apply_and_database() {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../testdata/port/contacts/import.json")).unwrap();
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    for (path, expected) in fixture["source_hashes"].as_object().unwrap() {
        use sha2::{Digest, Sha256};
        assert_eq!(
            format!(
                "{:x}",
                Sha256::digest(std::fs::read(root.join(path)).unwrap())
            ),
            expected.as_str().unwrap(),
            "{path}"
        );
    }
    let mut db = open_memory().unwrap();
    db.execute_batch(fixture["seed"].as_str().unwrap()).unwrap();
    let expected: ImportPlan = serde_json::from_value(fixture["plan"].clone()).unwrap();
    let before: i64 = db
        .query_row("SELECT total_changes()", [], |r| r.get(0))
        .unwrap();
    let plan = pipeline::plan(
        &db,
        &expected.source,
        expected.rows.clone(),
        expected.issues.clone(),
    )
    .unwrap();
    assert_eq!(plan, expected);
    let after: i64 = db
        .query_row("SELECT total_changes()", [], |r| r.get(0))
        .unwrap();
    assert_eq!(before, after, "planning must not write");
    let resolutions: Vec<RowResolution> =
        serde_json::from_value(fixture["resolutions"].clone()).unwrap();
    let mut n = 0;
    let result = pipeline::apply(
        &mut db,
        &plan,
        &resolutions,
        || {
            n += 1;
            format!("00000000-0000-4000-8000-{n:012}")
        },
        || "2000-01-02T03:04:05Z".into(),
    )
    .unwrap();
    assert_eq!(json!(result), fixture["result"]);
    for (query, expected) in fixture["queries"]
        .as_array()
        .unwrap()
        .iter()
        .zip(fixture["snapshots"].as_array().unwrap())
    {
        let mut stmt = db.prepare(query.as_str().unwrap()).unwrap();
        let columns = stmt.column_count();
        let actual: Vec<Vec<Value>> = stmt
            .query_map([], |r| {
                (0..columns)
                    .map(|i| {
                        Ok(match r.get_ref(i)? {
                            rusqlite::types::ValueRef::Null => Value::Null,
                            rusqlite::types::ValueRef::Integer(i) => json!(i),
                            rusqlite::types::ValueRef::Text(s) => {
                                json!(std::str::from_utf8(s).unwrap())
                            }
                            v => panic!("unexpected value {v:?}"),
                        })
                    })
                    .collect()
            })
            .unwrap()
            .collect::<rusqlite::Result<_>>()
            .unwrap();
        assert_eq!(json!(actual), *expected, "{query}");
    }
    // An unchanged source re-import must surface the source match, rather than
    // silently creating new people. Applying with no resolutions skips them.
    let again = pipeline::plan(&db, &plan.source, plan.rows.clone(), plan.issues.clone()).unwrap();
    assert!(
        again
            .duplicates
            .as_ref()
            .unwrap()
            .iter()
            .any(|d| d.exact_source_rematch)
    );
    let result = pipeline::apply(
        &mut db,
        &again,
        &[],
        || {
            n += 1;
            format!("00000000-0000-4000-8000-{n:012}")
        },
        || "2000-01-02T03:04:05Z".into(),
    )
    .unwrap();
    assert_eq!(json!(result), fixture["reimport"]);
}
