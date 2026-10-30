//! Stateful notebook replay helpers. Normalize only generated UTC clock text;
//! immutable object hashes and indexed content hashes remain checked.
use serde_json::{Value, json};
use std::{fs, path::Path};

pub fn clock(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = String::new();
    let mut start = 0;
    let mut i = 0;
    while i + 20 <= bytes.len() {
        let shape = &bytes[i..i + 19];
        if shape.iter().enumerate().all(|(n, b)| match n {
            4 | 7 => *b == b'-',
            10 => *b == b'T',
            13 | 16 => *b == b':',
            _ => b.is_ascii_digit(),
        }) {
            let mut end = i + 19;
            if bytes.get(end) == Some(&b'.') {
                end += 1;
                while bytes.get(end).is_some_and(u8::is_ascii_digit) {
                    end += 1;
                }
            }
            if bytes.get(end) == Some(&b'Z') {
                out.push_str(&text[start..i]);
                out.push_str("$TIME");
                i = end + 1;
                start = i;
                continue;
            }
        }
        i += 1;
    }
    out.push_str(&text[start..]);
    out
}

pub fn snapshot(root: &Path, mut state: Value) -> Value {
    for encoded in state["files"]
        .as_object_mut()
        .expect("vault files")
        .values_mut()
    {
        let hex = encoded.as_str().expect("hex bytes");
        let bytes = (0..hex.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&hex[i..i + 2], 16).expect("hex byte"))
            .collect::<Vec<_>>();
        let normalized = clock(std::str::from_utf8(&bytes).expect("UTF-8 controlled fixture"));
        *encoded = json!(
            normalized
                .bytes()
                .map(|b| format!("{b:02x}"))
                .collect::<String>()
        );
    }
    let mut tables = serde_json::Map::new();
    collect(root, root, &mut tables);
    state["tables"] = Value::Object(tables);
    state
}

fn collect(root: &Path, dir: &Path, tables: &mut serde_json::Map<String, Value>) {
    for entry in fs::read_dir(dir).expect("state directory") {
        let entry = entry.expect("state entry");
        let path = entry.path();
        if entry.file_type().expect("entry type").is_dir() {
            collect(root, &path, tables);
            continue;
        }
        if path.extension().is_none_or(|e| e != "db") {
            continue;
        }
        let db = rusqlite::Connection::open_with_flags(
            &path,
            rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY,
        )
        .expect("read sidecar");
        let mut stmt = db
            .prepare("SELECT path,sha256 FROM files ORDER BY path")
            .expect("hash query");
        for row in stmt
            .query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)))
            .expect("hash rows")
        {
            let (path, hash) = row.expect("hash row");
            assert_eq!(
                symdesk_vault::sha256_hex(&fs::read(path).expect("indexed file")),
                hash,
                "indexed hash matches authoritative bytes"
            );
        }
        for (key, query) in [
            ("files", "SELECT path,title FROM files ORDER BY path"),
            (
                "properties",
                "SELECT f.path,p.key,COALESCE(p.value,''),p.value_type FROM file_properties p JOIN files f ON f.id=p.file_id ORDER BY f.path,p.key",
            ),
            (
                "links",
                "SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind",
            ),
            (
                "fts",
                "SELECT title,body FROM fts_search ORDER BY title,body",
            ),
        ] {
            let mut stmt = db.prepare(query).expect("logical state query");
            let n = stmt.column_count();
            let rows = stmt
                .query_map([], |row| {
                    (0..n)
                        .map(|i| row.get::<_, String>(i))
                        .collect::<Result<Vec<_>, _>>()
                })
                .expect("logical rows");
            let rows = rows
                .map(|r| {
                    r.expect("logical row")
                        .iter()
                        .map(|s| {
                            clock(
                                &s.replace(root.to_string_lossy().as_ref(), "$ROOT")
                                    .replace('\\', "/"),
                            )
                        })
                        .collect::<Vec<_>>()
                })
                .collect::<Vec<_>>();
            tables.insert(key.to_owned(), json!(rows));
        }
    }
}
