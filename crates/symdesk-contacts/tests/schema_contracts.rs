use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use symdesk_contacts::{MIGRATIONS, migrate, open_memory};

#[test]
fn go_schema_and_migration_bookkeeping() {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../testdata/port/contacts/schema.json")).unwrap();
    assert_eq!(MIGRATIONS.len(), 7);
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    for (path, expected) in fixture["source_hashes"].as_object().unwrap() {
        let bytes = std::fs::read(root.join(path)).unwrap();
        assert_eq!(
            format!("{:x}", Sha256::digest(&bytes)),
            expected.as_str().unwrap(),
            "{path}"
        );
        if path.ends_with(".sql") {
            let filename = std::path::Path::new(path).file_name().unwrap();
            assert_eq!(
                bytes,
                std::fs::read(
                    root.join("crates/symdesk-contacts/migrations")
                        .join(filename)
                )
                .unwrap()
            );
        }
    }
    let mut db = open_memory().unwrap();
    db.execute(
        "UPDATE schema_migrations SET applied_at = '2000-01-02T03:04:05.000Z'",
        [],
    )
    .unwrap();
    migrate(&mut db).unwrap();
    let objects: Vec<Value> = db.prepare("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name").unwrap()
        .query_map([], |r| Ok(json!({"type":r.get::<_,String>(0)?, "name":r.get::<_,String>(1)?, "table":r.get::<_,String>(2)?, "sql":r.get::<_,Option<String>>(3)?}))).unwrap()
        .collect::<rusqlite::Result<_>>().unwrap();
    assert!(!objects.is_empty());
    assert_eq!(json!(objects), fixture["objects"]);
    let migrations: Vec<Value> = db.prepare("SELECT version, name, applied_at FROM schema_migrations ORDER BY version").unwrap()
        .query_map([], |r| Ok(json!({"version":r.get::<_,i64>(0)?, "name":r.get::<_,String>(1)?, "applied_at":r.get::<_,String>(2)?}))).unwrap()
        .collect::<rusqlite::Result<_>>().unwrap();
    assert_eq!(json!(migrations), fixture["migrations"]);
}

#[test]
fn failed_migration_rolls_back_its_version_only() {
    let mut db = rusqlite::Connection::open_in_memory().unwrap();
    db.execute_batch("CREATE TABLE persons (id TEXT)").unwrap();
    assert!(migrate(&mut db).is_err());
    let versions: Vec<i64> = db
        .prepare("SELECT version FROM schema_migrations ORDER BY version")
        .unwrap()
        .query_map([], |r| r.get(0))
        .unwrap()
        .collect::<rusqlite::Result<_>>()
        .unwrap();
    assert_eq!(versions, vec![1]);
    let count: i64 = db
        .query_row(
            "SELECT count(*) FROM sqlite_master WHERE name='organizations'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(count, 0);
}
