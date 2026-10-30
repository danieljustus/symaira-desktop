use rusqlite::{Connection, params};
use symdesk_index::Sidecar;

struct TestRoot(std::path::PathBuf);

impl TestRoot {
    fn new() -> Self {
        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!("backlinks-{}-{stamp}", std::process::id()));
        std::fs::create_dir(&path).expect("create test root");
        Self(path)
    }
}

impl Drop for TestRoot {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).expect("remove closed database root");
    }
}

#[test]
fn basename_backlinks_resolve_native_and_slash_paths() {
    for native in [false, true] {
        let root = TestRoot::new();
        let db_path = root.0.join("sidecar.db");
        let sidecar = Sidecar::open(&db_path).expect("open sidecar");
        let conn = Connection::open(&db_path).expect("seed connection");
        let target = if native {
            std::path::PathBuf::from("vault")
                .join("nested")
                .join("target.md")
                .to_string_lossy()
                .into_owned()
        } else {
            "vault/nested/target.md".to_owned()
        };
        conn.execute(
            "INSERT INTO files(path, title, sha256, modified_at, indexed_at) VALUES (?, 'Target Title', 'target', '2026-01-02', '2026-01-02')",
            [&target],
        )
        .expect("target document");
        let id = conn.last_insert_rowid();
        conn.execute(
            "INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, 'aliases', ?, 'array')",
            params![id, "[\"Alias Name\"]"],
        )
        .expect("target aliases");
        for (source, link) in [
            ("title.md", "Target Title"),
            ("alias.md", "Alias Name"),
            ("basename.md", "target"),
        ] {
            conn.execute(
                "INSERT INTO links(from_path, to_path, kind) VALUES (?, ?, 'wikilink')",
                params![source, link],
            )
            .expect("backlink");
        }
        for mut actual in [
            sidecar.get_backlinks("target.md").expect("read backlinks"),
            sidecar.backlinks("target.md", false).expect("backlinks"),
        ] {
            actual.sort();
            assert_eq!(actual, ["alias.md", "basename.md", "title.md"]);
        }
    }
}
