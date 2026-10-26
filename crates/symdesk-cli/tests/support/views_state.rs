use std::{collections::BTreeMap, fs, path::Path};

pub fn snapshot(root: &Path) -> serde_json::Value {
    let mut files = BTreeMap::<String, String>::new();
    let mut databases = BTreeMap::<String, BTreeMap<String, i64>>::new();
    walk(root, root, &mut files, &mut databases);
    serde_json::json!({"files":files,"databases":databases})
}

fn walk(
    root: &Path,
    dir: &Path,
    files: &mut BTreeMap<String, String>,
    databases: &mut BTreeMap<String, BTreeMap<String, i64>>,
) {
    for entry in fs::read_dir(dir).expect("snapshot directory") {
        let entry = entry.expect("snapshot entry");
        let path = entry.path();
        if entry.file_type().expect("entry type").is_dir() {
            walk(root, &path, files, databases);
            continue;
        }
        let rel = path
            .strip_prefix(root)
            .expect("rooted entry")
            .to_string_lossy()
            .replace('\\', "/");
        if rel.starts_with("vault/") {
            files.insert(
                rel,
                fs::read(&path)
                    .expect("snapshot vault bytes")
                    .iter()
                    .map(|byte| format!("{byte:02x}"))
                    .collect(),
            );
        }
        if path.extension().is_some_and(|ext| ext == "db") {
            let db = rusqlite::Connection::open_with_flags(
                &path,
                rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY,
            )
            .expect("snapshot database");
            let mut stmt = db
                .prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
                .expect("table list");
            let names = stmt
                .query_map([], |row| row.get::<_, String>(0))
                .expect("read table list")
                .collect::<Result<Vec<_>, _>>()
                .expect("table names");
            let mut counts = BTreeMap::new();
            for name in names {
                let count: i64 = db
                    .query_row(
                        &format!("SELECT count(*) FROM \"{}\"", name.replace('"', "\"\"")),
                        [],
                        |row| row.get(0),
                    )
                    .expect("table count");
                counts.insert(name, count);
            }
            databases.insert("sidecar".to_owned(), counts);
        }
    }
}
