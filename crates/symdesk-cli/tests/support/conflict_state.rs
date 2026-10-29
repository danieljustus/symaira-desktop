//! Sidecar plus both standalone and per-vault retrieval state after conflicts.
use std::{collections::BTreeMap, fs, path::Path};
pub fn snapshot(
    vault: &Path,
    home: &Path,
    root: &Path,
    canonical_root: &Path,
) -> BTreeMap<String, Vec<Vec<String>>> {
    let mut state = super::link_state::snapshot(vault, home, root, canonical_root);
    fn walk(path: &Path, paths: &mut Vec<std::path::PathBuf>) {
        for e in fs::read_dir(path).expect("state directory") {
            let e = e.expect("state entry");
            if e.file_type().expect("state type").is_dir() {
                walk(&e.path(), paths)
            } else if e.file_name() == "retrieval.db" {
                paths.push(e.path())
            }
        }
    }
    let mut paths = Vec::new();
    walk(home, &mut paths);
    for path in paths {
        let prefix = if path
            .to_string_lossy()
            .replace(std::path::MAIN_SEPARATOR, "/")
            .contains("/vaults/")
        {
            "retrieval-vault/"
        } else {
            "retrieval-standalone/"
        };
        let db =
            rusqlite::Connection::open_with_flags(path, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)
                .expect("retrieval state");
        for (key, query) in [
            ("documents", "SELECT path,hash FROM documents ORDER BY path"),
            (
                "chunks",
                "SELECT document_path,CAST(chunk_index AS TEXT),content,hash,hex(embedding) FROM chunks ORDER BY document_path,chunk_index",
            ),
            (
                "generation",
                "SELECT key,CAST(value AS TEXT) FROM index_meta ORDER BY key",
            ),
        ] {
            let mut statement = db.prepare(query).expect("state query");
            let count = statement.column_count();
            let rows = statement
                .query_map([], |row| {
                    (0..count)
                        .map(|i| {
                            row.get::<_, String>(i)
                                .map(|v| super::normalize(&v, root, canonical_root))
                        })
                        .collect::<Result<Vec<_>, _>>()
                })
                .expect("state rows")
                .collect::<Result<Vec<_>, _>>()
                .expect("state values");
            assert!(
                state.insert(format!("{prefix}{key}"), rows).is_none(),
                "one retrieval store per scope"
            );
        }
    }
    state
}
