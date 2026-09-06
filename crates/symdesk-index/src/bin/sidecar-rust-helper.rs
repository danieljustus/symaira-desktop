#![deny(unsafe_code)]

use std::{
    env, fs,
    path::Path,
    thread,
    time::{Duration, Instant},
};

use rusqlite::{Connection, Row};
use serde_json::{Value, json};
use symdesk_index::{IndexedDocument, Sidecar};

fn main() {
    let started = Instant::now();
    let (command, db, input_path) = arguments();
    let input = input_path.as_deref().map(read_json).transpose();
    let result =
        input.and_then(|value| run(&command, Path::new(&db), value.unwrap_or_else(|| json!({}))));
    match result {
        Ok(mut output) => {
            output["outcome"] = json!("ok");
            output["elapsed_ms"] = json!(started.elapsed().as_millis() as u64);
            println!("{}", serde_json::to_string(&output).expect("result JSON"));
        }
        Err(error) => {
            eprintln!("{error}");
            let text = error.to_string().to_lowercase();
            let (class, busy) = if text.contains("locked") || text.contains("busy") {
                ("locked", true)
            } else if text.contains("not a database")
                || text.contains("malformed")
                || text.contains("disk image")
                || text.contains("encrypted")
                || text.contains("integrity check failed")
                || text.contains("page")
                || text.contains("btree")
            {
                ("corrupt", false)
            } else if text.contains("permission")
                || text.contains("read-only")
                || text.contains("readonly")
            {
                ("readonly", false)
            } else if text.contains("constraint") {
                ("constraint", false)
            } else {
                ("error", false)
            };
            println!(
                "{}",
                json!({"outcome":"error", "error_class":class, "busy":busy, "elapsed_ms":started.elapsed().as_millis() as u64})
            );
        }
    }
}

fn arguments() -> (String, String, Option<String>) {
    let mut command = String::new();
    let mut db = String::new();
    let mut input = None;
    let mut args = env::args().skip(1);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--db" => db = args.next().unwrap_or_default(),
            "--input" => input = args.next(),
            _ if command.is_empty() => command = arg,
            _ => {}
        }
    }
    (command, db, input)
}

fn read_json(path: &str) -> Result<Value, Box<dyn std::error::Error>> {
    Ok(serde_json::from_slice(&fs::read(path)?)?)
}

fn run(command: &str, db: &Path, input: Value) -> Result<Value, Box<dyn std::error::Error>> {
    if command == "writer" {
        return writer_retry(db, &input);
    }
    match command {
        "open-check" => {
            let sidecar = Sidecar::open(db)?;
            drop(sidecar);
            Ok(json!({}))
        }
        "integrity" => {
            let sidecar = Sidecar::open(db)?;
            sidecar.check_integrity()?;
            Ok(json!({}))
        }
        "snapshot" => {
            let sidecar = Sidecar::open(db)?;
            drop(sidecar);
            let connection = open_raw_connection(db)?;
            Ok(json!({"snapshot": snapshot(&connection)?}))
        }
        "search" => {
            let sidecar = Sidecar::open(db)?;
            let hits = sidecar.search(
                input
                    .get("query")
                    .and_then(Value::as_str)
                    .unwrap_or_default(),
            )?;
            Ok(
                json!({"hits": hits.into_iter().map(|hit| json!({"path":hit.path,"title":hit.title,"snippet":hit.snippet})).collect::<Vec<_>>() }),
            )
        }
        "create" | "mutate" | "writer" | "refresh" | "prune" => {
            let mut sidecar = Sidecar::open(db)?;
            if command == "refresh" || command == "prune" {
                let vault = input
                    .get("vault")
                    .and_then(Value::as_str)
                    .ok_or("lifecycle vault is required")?;
                if command == "refresh" {
                    sidecar.refresh_index(Path::new(vault))?;
                } else {
                    sidecar.prune(Path::new(vault))?;
                }
            } else {
                let docs = documents(
                    input
                        .get("documents")
                        .and_then(Value::as_array)
                        .map_or(&[][..], Vec::as_slice),
                )?;
                if !docs.is_empty() {
                    sidecar.index_documents(&docs)?;
                }
                for path in input
                    .get("delete")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .filter_map(Value::as_str)
                {
                    sidecar.delete_document(path)?;
                }
            }
            Ok(json!({}))
        }
        "rollback" => {
            let mut sidecar = Sidecar::open(db)?;
            let values = input
                .get("documents")
                .and_then(Value::as_array)
                .ok_or("rollback documents missing")?;
            if values.len() != 1 {
                return Err("rollback requires one document".into());
            }
            let doc = document(&values[0])?;
            sidecar.index_document(&doc)?;
            Ok(json!({}))
        }
        "lock-holder" => hold_lock(db, &input),
        _ => Err(format!("unknown command {command:?}").into()),
    }
}

fn writer_retry(db: &Path, input: &Value) -> Result<Value, Box<dyn std::error::Error>> {
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        let attempt = (|| -> Result<(), Box<dyn std::error::Error>> {
            let mut sidecar = Sidecar::open(db)?;
            let docs = documents(
                input
                    .get("documents")
                    .and_then(Value::as_array)
                    .map_or(&[][..], Vec::as_slice),
            )?;
            if !docs.is_empty() {
                sidecar.index_documents(&docs)?;
            }
            for path in input
                .get("delete")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .filter_map(Value::as_str)
            {
                sidecar.delete_document(path)?;
            }
            Ok(())
        })();
        match attempt {
            Ok(()) => return Ok(json!({})),
            Err(error)
                if (error.to_string().to_lowercase().contains("locked")
                    || error.to_string().to_lowercase().contains("busy"))
                    && Instant::now() < deadline =>
            {
                thread::sleep(Duration::from_millis(25))
            }
            Err(error) => return Err(error),
        }
    }
}

fn documents(values: &[Value]) -> Result<Vec<IndexedDocument>, Box<dyn std::error::Error>> {
    values.iter().map(document).collect()
}

fn document(value: &Value) -> Result<IndexedDocument, Box<dyn std::error::Error>> {
    let path = value
        .get("path")
        .and_then(Value::as_str)
        .ok_or("document path missing")?;
    let markdown = value
        .get("markdown")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let parsed = symdesk_vault::parse_bytes(path, markdown.as_bytes())?;
    let mtime = value.get("mtime_ns").and_then(Value::as_i64);
    let mut indexed = IndexedDocument::from_vault(&parsed, mtime)?;
    if let Some(links) = value.get("links").and_then(Value::as_array) {
        indexed.links = links
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
    }
    Ok(indexed)
}

fn hold_lock(db: &Path, input: &Value) -> Result<Value, Box<dyn std::error::Error>> {
    let sidecar = Sidecar::open(db)?;
    drop(sidecar);
    let connection = open_raw_connection(db)?;
    connection.execute_batch("BEGIN IMMEDIATE")?;
    let ready = input
        .get("ready")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let go = input.get("go").and_then(Value::as_str).unwrap_or_default();
    let release = input
        .get("release")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if !ready.is_empty() {
        fs::write(ready, b"ready\n")?;
    }
    if !go.is_empty() {
        wait_file(go, Duration::from_secs(10))?;
    }
    let hold_ms = input.get("hold_ms").and_then(Value::as_u64).unwrap_or(0);
    let deadline = Instant::now() + Duration::from_millis(hold_ms);
    loop {
        if !release.is_empty() && Path::new(release).exists() {
            break;
        }
        if hold_ms > 0 && Instant::now() >= deadline {
            break;
        }
        thread::sleep(Duration::from_millis(10));
    }
    connection.execute_batch("ROLLBACK")?;
    Ok(json!({}))
}

fn wait_file(path: &str, timeout: Duration) -> Result<(), Box<dyn std::error::Error>> {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if Path::new(path).exists() {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(10));
    }
    Err(format!("handshake timeout waiting for {path}").into())
}

fn open_raw_connection(db: &Path) -> Result<Connection, rusqlite::Error> {
    let connection = Connection::open(db)?;
    connection.busy_timeout(Duration::from_millis(5000))?;
    connection.pragma_update(None, "foreign_keys", true)?;
    connection.pragma_update(None, "journal_mode", "WAL")?;
    Ok(connection)
}

fn snapshot(connection: &Connection) -> Result<Value, Box<dyn std::error::Error>> {
    let journal_mode: String = connection.query_row("PRAGMA journal_mode", [], |row| row.get(0))?;
    let foreign_keys: i64 = connection.query_row("PRAGMA foreign_keys", [], |row| row.get(0))?;
    let busy_timeout: i64 = connection.query_row("PRAGMA busy_timeout", [], |row| row.get(0))?;
    Ok(json!({
        "migrations": query(connection, "SELECT version FROM schema_migrations ORDER BY version", |row| Ok(json!({"version":row.get::<_,String>(0)?})))?,
        "schema": query(connection, "SELECT type,name,COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name NOT LIKE 'fts_search_%' AND name NOT LIKE 'fts_norm_%' AND name NOT LIKE 'fts_tri_%' ORDER BY type,name", |row| Ok(json!({"type":row.get::<_,String>(0)?,"name":row.get::<_,String>(1)?,"sql":row.get::<_,String>(2)?})))?,
        "pragmas": {"journal_mode": journal_mode, "foreign_keys": foreign_keys.to_string(), "busy_timeout": busy_timeout.to_string()},
        "files": query(connection, "SELECT path,sha256,title,created_at,modified_at,\"type\",document_date,person,status,due_date,confidence,ocr_json_path,simhash,asn,size,mtime_ns FROM files ORDER BY path", |row| Ok(json!({"path":row.get::<_,String>(0)?,"sha256":row.get::<_,String>(1)?,"title":row.get::<_,String>(2)?,"created_at":row.get::<_,String>(3)?,"modified_at":row.get::<_,String>(4)?,"type":row.get::<_,String>(5)?,"document_date":row.get::<_,Option<String>>(6)?,"person":row.get::<_,Option<String>>(7)?,"status":row.get::<_,Option<String>>(8)?,"due_date":row.get::<_,Option<String>>(9)?,"confidence":row.get::<_,Option<i64>>(10)?,"ocr_json_path":row.get::<_,Option<String>>(11)?,"simhash":row.get::<_,Option<String>>(12)?,"asn":row.get::<_,Option<i64>>(13)?,"size":row.get::<_,Option<i64>>(14)?,"mtime_ns":row.get::<_,Option<i64>>(15)?})))?,
        "properties": query(connection, "SELECT f.path,p.key,p.value,p.value_type FROM file_properties p JOIN files f ON f.id=p.file_id ORDER BY f.path,p.key", |row| Ok(json!({"path":row.get::<_,String>(0)?,"key":row.get::<_,String>(1)?,"value":row.get::<_,Option<String>>(2)?,"value_type":row.get::<_,String>(3)?})))?,
        "links": query(connection, "SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind", |row| Ok(json!({"from":row.get::<_,String>(0)?,"to":row.get::<_,String>(1)?,"kind":row.get::<_,String>(2)?})))?,
        "fts_search": query(connection, "SELECT f.path,x.title,x.body FROM fts_search x JOIN files f ON f.id=x.rowid ORDER BY f.path", |row| Ok(json!({"path":row.get::<_,String>(0)?,"title":row.get::<_,String>(1)?,"body":row.get::<_,String>(2)?})))?,
        "fts_norm": query(connection, "SELECT f.path,x.norm FROM fts_norm x JOIN files f ON f.id=x.rowid ORDER BY f.path", |row| Ok(json!({"path":row.get::<_,String>(0)?,"norm":row.get::<_,String>(1)?})))?,
        "fts_tri": query(connection, "SELECT f.path,x.body FROM fts_tri x JOIN files f ON f.id=x.rowid ORDER BY f.path", |row| Ok(json!({"path":row.get::<_,String>(0)?,"body":row.get::<_,String>(1)?})))?,
    }))
}

fn query<F>(
    connection: &Connection,
    sql: &str,
    mut map: F,
) -> Result<Vec<Value>, Box<dyn std::error::Error>>
where
    F: FnMut(&Row<'_>) -> rusqlite::Result<Value>,
{
    let mut statement = connection.prepare(sql)?;
    let rows = statement.query_map([], |row| map(row))?;
    Ok(rows.collect::<Result<Vec<_>, _>>()?)
}
