//! Semantic persisted-state comparison shared by indexed link CLI replays.
use std::{collections::BTreeMap, fs, path::Path};

pub fn snapshot(
    vault: &Path,
    home: &Path,
    root: &Path,
    canonical_root: &Path,
) -> BTreeMap<String, Vec<Vec<String>>> {
    fn walk(root: &Path, paths: &mut Vec<std::path::PathBuf>) {
        for entry in fs::read_dir(root).expect("read isolated state directory") {
            let entry = entry.expect("state entry");
            if entry.file_type().expect("state type").is_dir() {
                walk(&entry.path(), paths);
            } else {
                paths.push(entry.path());
            }
        }
    }
    let mut paths = Vec::new();
    walk(vault, &mut paths);
    paths.sort();
    let mut state = BTreeMap::new();
    state.insert(
        "vault".to_owned(),
        paths
            .iter()
            .map(|path| {
                vec![
                    path.strip_prefix(vault)
                        .expect("vault relative path")
                        .to_string_lossy()
                        .replace(std::path::MAIN_SEPARATOR, "/"),
                    fs::read_to_string(path).expect("preserved fixture bytes"),
                ]
            })
            .collect(),
    );
    paths.clear();
    walk(home, &mut paths);
    for path in paths
        .iter()
        .filter(|p| p.file_name().is_some_and(|v| v == "sidecar.db"))
    {
        let db =
            rusqlite::Connection::open_with_flags(path, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)
                .expect("read sidecar state");
        for (key, query) in [
            ("files", "SELECT path, title FROM files ORDER BY path"),
            (
                "links",
                "SELECT from_path, to_path, kind FROM links ORDER BY from_path, to_path, kind",
            ),
            (
                "properties",
                "SELECT f.path, p.key, p.value FROM file_properties p JOIN files f ON f.id=p.file_id ORDER BY f.path,p.key,p.value",
            ),
        ] {
            let mut stmt = db.prepare(query).expect("prepare state query");
            let count = stmt.column_count();
            let rows = stmt
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
                .expect("decode state rows");
            assert!(
                state.insert(key.to_owned(), rows).is_none(),
                "only one isolated sidecar"
            );
        }
    }
    state
}
