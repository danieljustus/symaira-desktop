use rusqlite::{Connection, functions::FunctionFlags, types::ValueRef};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};
use symdesk_ingest::{PaperlessStateWrite, Store};
struct Root(PathBuf);
impl Drop for Root {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}
fn open(path: &Path, clock: Arc<Mutex<String>>) -> Store {
    let c = symaira_core_sqlite::open_with_existing_parent(path).unwrap();
    c.create_scalar_function(
        "current_timestamp",
        0,
        FunctionFlags::SQLITE_UTF8,
        move |_| Ok(clock.lock().unwrap().clone()),
    )
    .unwrap();
    Store::from_connection(c).unwrap()
}
fn snapshot(c: &Connection) -> Value {
    let mut s = c
        .prepare("SELECT * FROM paperless_import_state ORDER BY id")
        .unwrap();
    let columns = s
        .column_names()
        .iter()
        .map(|s| s.to_string())
        .collect::<Vec<_>>();
    let rows = s
        .query_map([], |r| {
            let mut out = BTreeMap::new();
            for (i, k) in columns.iter().enumerate() {
                let v = match r.get_ref(i)? {
                    ValueRef::Null => Value::Null,
                    ValueRef::Integer(v) => json!(v),
                    ValueRef::Text(v) => json!(std::str::from_utf8(v).unwrap()),
                    v => panic!("unexpected {v:?}"),
                };
                out.insert(k.clone(), v);
            }
            Ok(out)
        })
        .unwrap()
        .collect::<rusqlite::Result<Vec<_>>>()
        .unwrap();
    json!(rows)
}
fn fixture() -> Value {
    serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/ingest/paperless-state.json"),
        )
        .unwrap(),
    )
    .unwrap()
}
fn replay(f: &Value) -> Result<(), String> {
    let root = Root(std::env::temp_dir().join(format!(
            "paperless-state-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        )));
    fs::create_dir(&root.0).unwrap();
    let path = root.0.join("state.db");
    let clock = Arc::new(Mutex::new("2000-01-01 00:00:00".to_owned()));
    let mut s = open(&path, clock.clone());
    for (i, step) in f["steps"].as_array().unwrap().iter().enumerate() {
        let op = &step["input"];
        let strval = |k: &str| op[k].as_str().unwrap();
        let id = op["id"].as_i64().unwrap();
        let result: Result<Value, String> = match strval("op") {
            "upsert" => s
                .upsert_paperless_import_state_for_target(&PaperlessStateWrite {
                    base_url: strval("base"),
                    target_vault: strval("vault"),
                    target_archive: strval("archive"),
                    document_id: id,
                    status: strval("status"),
                    last_error: strval("error"),
                    vault_path: strval("note"),
                    archive_path: strval("file"),
                    sha256: strval("hash"),
                })
                .map(|()| Value::Null),
            "legacy-upsert" => s
                .upsert_paperless_import_state(
                    strval("base"),
                    id,
                    strval("status"),
                    strval("error"),
                )
                .map(|()| Value::Null),
            "status" | "target-status" => {
                let result = if strval("op") == "status" {
                    s.paperless_import_status(strval("base"), id)
                } else {
                    s.paperless_import_status_for_target(
                        strval("base"),
                        strval("vault"),
                        strval("archive"),
                        id,
                    )
                };
                result.map(|v| json!({"found":v.is_some(),"status":v.unwrap_or_default()}))
            }
            "get" => s
                .paperless_import_state_by_id(strval("base"), id)
                .map(|v| json!(v)),
            "target-get" => s
                .paperless_import_state_for_target(
                    strval("base"),
                    strval("vault"),
                    strval("archive"),
                    id,
                )
                .map(|v| json!(v)),
            "list" => s
                .list_paperless_import_state(strval("base"), strval("status"))
                .map(|v| json!(v)),
            "advance" => {
                *clock.lock().unwrap() = strval("status").into();
                Ok(Value::Null)
            }
            "reopen" => {
                drop(s);
                s = open(&path, clock.clone());
                Ok(Value::Null)
            }
            op => panic!("unexpected {op}"),
        };
        let (result, error) = match result {
            Ok(v) => (v, String::new()),
            Err(e) => (Value::Null, e),
        };
        if result != step["result"] {
            return Err(format!(
                "step {i} result\nactual {result:#}\nexpected {:#}",
                step["result"]
            ));
        }
        if json!(error) != step["error"] {
            return Err(format!(
                "step {i} error {error:?} expected {}",
                step["error"]
            ));
        }
        let rows = snapshot(s.connection());
        if rows != step["rows"] {
            return Err(format!(
                "step {i} rows\nactual {rows:#}\nexpected {:#}",
                step["rows"]
            ));
        }
        let seq:i64=s.connection().query_row("SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name='paperless_import_state'",[],|r|r.get(0)).unwrap();
        if json!(seq) != step["sequence"] {
            return Err(format!("step {i} sequence {seq}"));
        }
    }
    Ok(())
}
#[test]
fn paperless_state_go_contract() {
    let f = fixture();
    assert!(!f["steps"].as_array().unwrap().is_empty());
    replay(&f).unwrap();
}
#[test]
fn paperless_state_negative_control() {
    let mut f = fixture();
    replay(&f).unwrap();
    f["steps"][3]["rows"][0]["target_vault"] = json!("wrong-target");
    assert!(replay(&f).unwrap_err().contains("step 3 rows"));
}
