use rusqlite::{Connection, functions::FunctionFlags, types::ValueRef};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};
use symdesk_ingest::Store;

struct Temp(PathBuf);
impl Drop for Temp {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}
fn open(path: &Path, clock: Arc<Mutex<String>>) -> Store {
    let connection = symaira_core_sqlite::open_with_existing_parent(path).unwrap();
    let c = clock.clone();
    connection
        .create_scalar_function(
            "current_timestamp",
            0,
            FunctionFlags::SQLITE_UTF8,
            move |_| Ok(c.lock().unwrap().clone()),
        )
        .unwrap();
    connection
        .create_scalar_function("datetime", 2, FunctionFlags::SQLITE_UTF8, move |ctx| {
            assert_eq!(ctx.get::<String>(0)?, "now");
            assert_eq!(ctx.get::<String>(1)?, "-10 seconds");
            let now = clock.lock().unwrap();
            // Corpus timestamps intentionally stay in one day; explicit second arithmetic.
            let h: i64 = now[11..13].parse().unwrap();
            let m: i64 = now[14..16].parse().unwrap();
            let s: i64 = now[17..19].parse().unwrap();
            let seconds = h * 3600 + m * 60 + s - 10;
            Ok(format!(
                "{} {:02}:{:02}:{:02}",
                &now[..10],
                seconds / 3600,
                seconds % 3600 / 60,
                seconds % 60
            ))
        })
        .unwrap();
    Store::from_connection(connection).unwrap()
}
fn snapshot(c: &Connection, query: &str) -> Value {
    let mut stmt = c.prepare(query).unwrap();
    let names = stmt.column_names().map(str::to_owned).collect::<Vec<_>>();
    let rows = stmt
        .query_map([], |r| {
            let mut out = BTreeMap::new();
            for (i, k) in names.iter().enumerate() {
                let value = match r.get_ref(i)? {
                    ValueRef::Null => Value::Null,
                    ValueRef::Integer(v) => json!(v),
                    ValueRef::Real(v) => json!(v),
                    ValueRef::Text(v) => {
                        let s = std::str::from_utf8(v).unwrap();
                        if k == "vault_path" {
                            json!(s.replace('\\', "/"))
                        } else {
                            json!(s)
                        }
                    }
                    ValueRef::Blob(_) => panic!("unexpected blob"),
                };
                out.insert(k.clone(), value);
            }
            Ok(out)
        })
        .unwrap()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    json!(rows)
}
#[test]
fn go_queue_state_machine() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Value = serde_json::from_slice(
        &std::fs::read(root.join("testdata/port/ingest/queue.json")).unwrap(),
    )
    .unwrap();
    let temp = Temp(std::env::temp_dir().join(format!("ingest-queue-{}", std::process::id())));
    std::fs::create_dir(&temp.0).unwrap();
    let path = temp.0.join("queue.db");
    let clock = Arc::new(Mutex::new("2000-01-01 00:00:00".to_owned()));
    let mut store = open(&path, clock.clone());
    for vault in ["/vault/a", "/vault/b"] {
        store.connection().execute("INSERT INTO documents(source_path,sha256,mime,vault_root,status) VALUES ('input.pdf','hash','application/pdf',?,'pending')",[vault]).unwrap();
    }
    let steps = fixture["steps"].as_array().unwrap();
    assert_eq!(steps.len(), 75);
    for (index, step) in steps.iter().enumerate() {
        let input = &step["input"];
        let id = input["id"].as_i64().unwrap();
        let text = input["text"].as_str().unwrap();
        let result: Result<Value, String> = match input["op"].as_str().unwrap() {
            "doc_create" => store
                .create_or_get("other.pdf", "hash", "changed/mime", text)
                .map(|(d, c)| json!({"document":d,"created":c})),
            "doc_paths" => store
                .set_vault_and_archive_path(
                    id,
                    &symdesk_ingest::DocumentPaths {
                        vault_path: &if text == "/vault/legacy/note.md" {
                            format!("/vault/legacy{}note.md", std::path::MAIN_SEPARATOR)
                        } else {
                            text.to_owned()
                        },
                        archive_path: "archive.pdf",
                        category: "finance",
                        tags: &["a&b".into(), "<tag>".into(), "日本語".into()],
                        correspondent: "ACME",
                        document_type: "invoice",
                    },
                )
                .map(|()| Value::Null),
            "doc_provenance" => store
                .set_provenance(id, "mail-id", "sender")
                .map(|()| Value::Null),
            "doc_id" => store.by_id(id).map(|d| json!(d)),
            "doc_hash" => store.by_hash("hash").map(|d| json!(d)),
            "doc_vault" => store.by_hash_in_vault("hash", text).map(|d| json!(d)),
            "doc_archive" => store.by_archive_path(text).map(|d| json!(d)),
            "doc_list" => store.list_documents().map(|d| json!(d)),
            "mail_has" => store.has_mail_message(text).map(|v| json!(v)),
            "mail_track" => store
                .track_mail_message(text, "INBOX")
                .map(|()| Value::Null),
            "advance" => {
                *clock.lock().unwrap() = text.to_owned();
                Ok(Value::Null)
            }
            "enqueue" => store.enqueue_job(id, text).map(|j| json!(j)),
            "skipped" => store.enqueue_skipped_job(id, "ocr", text).map(|j| json!(j)),
            "reocr" => store
                .enqueue_reprocess_job(id)
                .map(|(j, c)| json!({"job":j,"created":c})),
            "claim_id" => store.claim_job_by_id(id).map(|j| json!(j)),
            "claim" => store.claim_job().map(|j| json!(j)),
            "complete" => store.complete_job(id).map(|()| Value::Null),
            "fail" => store.fail_job(id, text).map(|()| Value::Null),
            "retry" => store.retry_job(id).map(|()| Value::Null),
            "reset" => store.reset_running_jobs().map(|()| Value::Null),
            "restart" => {
                drop(store);
                store = open(&path, clock.clone());
                store.reset_running_jobs().map(|()| Value::Null)
            }
            "list" => store
                .list_jobs_page(text, id, 1)
                .map(|(j, t)| json!({"jobs":j,"total":t})),
            other => panic!("unknown op {other}"),
        };
        let (mut value, error) = match result {
            Ok(v) => (v, String::new()),
            Err(e) => {
                let value = match input["op"].as_str().unwrap() {
                    "reocr" => json!({"job":null,"created":false}),
                    "doc_create" => json!({"document":null,"created":false}),
                    _ => Value::Null,
                };
                (value, e)
            }
        };
        fn normalize_paths(v: &mut Value) {
            match v {
                Value::Object(m) => {
                    for (k, v) in m {
                        if k == "VaultPath" {
                            if let Some(s) = v.as_str() {
                                *v = json!(s.replace('\\', "/"));
                            }
                        } else {
                            normalize_paths(v)
                        }
                    }
                }
                Value::Array(a) => {
                    for v in a {
                        normalize_paths(v)
                    }
                }
                _ => {}
            }
        }
        normalize_paths(&mut value);
        assert_eq!(
            error.as_bytes(),
            step["error"].as_str().unwrap().as_bytes(),
            "step {index} {input}"
        );
        assert_eq!(value, step["result"], "return step {index} {input}");
        let raw = match input["op"].as_str().unwrap() {
            "enqueue" | "skipped" | "claim" | "claim_id" => {
                if value.is_null() {
                    "null".to_owned()
                } else {
                    serde_json::to_string(
                        &serde_json::from_value::<symdesk_ingest::Job>(value.clone()).unwrap(),
                    )
                    .unwrap()
                }
            }
            "doc_id" | "doc_hash" | "doc_vault" | "doc_archive" => {
                if value.is_null() {
                    "null".to_owned()
                } else {
                    serde_json::to_string(
                        &serde_json::from_value::<symdesk_ingest::Document>(value.clone()).unwrap(),
                    )
                    .unwrap()
                }
            }
            // Map results contain typed structs: serialize through their production
            // types rather than letting serde_json::Value reorder the struct fields.
            "reocr" => {
                let j: Option<symdesk_ingest::Job> =
                    serde_json::from_value(value["job"].clone()).unwrap();
                format!(
                    "{{\"created\":{},\"job\":{}}}",
                    value["created"],
                    serde_json::to_string(&j).unwrap()
                )
            }
            "doc_create" => {
                let d: Option<symdesk_ingest::Document> =
                    serde_json::from_value(value["document"].clone()).unwrap();
                format!(
                    "{{\"created\":{},\"document\":{}}}",
                    value["created"],
                    serde_json::to_string(&d).unwrap()
                )
            }
            "list" => {
                let j: Option<Vec<symdesk_ingest::Job>> =
                    serde_json::from_value(value["jobs"].clone()).unwrap();
                format!(
                    "{{\"jobs\":{},\"total\":{}}}",
                    serde_json::to_string(&j).unwrap(),
                    value["total"]
                )
            }
            "doc_list" => serde_json::to_string(
                &serde_json::from_value::<Option<Vec<symdesk_ingest::Document>>>(value.clone())
                    .unwrap(),
            )
            .unwrap(),
            _ => serde_json::to_string(&value).unwrap(),
        }
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
        assert_eq!(
            raw.as_bytes(),
            step["result_json"].as_str().unwrap().as_bytes(),
            "wire bytes step {index}"
        );
        for (table, expected) in step["state"].as_object().unwrap() {
            assert!(table.chars().all(|c| c.is_ascii_alphanumeric() || c == '_'));
            assert_eq!(
                snapshot(
                    store.connection(),
                    &format!("SELECT * FROM {table} ORDER BY rowid")
                ),
                *expected,
                "database table {table} step {index}"
            );
        }
        assert_eq!(
            snapshot(store.connection(), "SELECT * FROM jobs ORDER BY id"),
            step["jobs"],
            "jobs step {index} {input}"
        );
        assert_eq!(
            snapshot(store.connection(), "SELECT * FROM documents ORDER BY id"),
            step["documents"],
            "documents step {index} {input}"
        );
    }
    assert_eq!(
        snapshot(
            store.connection(),
            "SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name"
        ),
        fixture["schema"]
    );
    // Close/reopen must retain the same schema and state (startup reset is explicit).
    drop(store);
    let store = open(&path, clock);
    assert_eq!(
        snapshot(store.connection(), "SELECT * FROM jobs ORDER BY id"),
        steps.last().unwrap()["jobs"]
    );
    drop(store);
}
/// SHA-256 of the Go ingest-store migrations at the last Go commit (36aad3d4,
/// tag v0.12.2); the Go tree is retired (RUST-019).
const GO_MIGRATION_SHA256: &[(&str, &str)] = &[
    (
        "001_documents",
        "0b3a941383ccb88eb64a4f2aa55a813ccfdb1436ad0d260c2601862fa4b5537b",
    ),
    (
        "002_jobs",
        "93e23cb5396efd99fb5f04e104233e3fd825e966a93e2d9f5d21c993182654ee",
    ),
    (
        "003_archive_path",
        "6fdde2cd32e146e5d5deab819ad96d245040b522c27241ee79fe24d0537b6b43",
    ),
    (
        "004_classification_rules",
        "6a6e5465aa434a806b88181d78d06b0d6a407efae774439a366322b7f83074d7",
    ),
    (
        "005_paperless_import_state",
        "80e762e11513af64eecc4c1432885df17350a99ed1e13000043b2a5174d17462",
    ),
    (
        "006_paperless_import_state_targets",
        "e0ebf8029183ad6caa86eb777bd621ab4067bec7d134cadfd127e63b7bc23335",
    ),
    (
        "007_paperless_import_state_paths",
        "602ca9b89b7b19afb736331071206fa0fece798e68bb0e39fcd17e2aa4b09aec",
    ),
    (
        "008_paperless_import_state_target_key",
        "450bb109cc2fb2baba36bf68f92df568e301f3959898b177660a89875895565d",
    ),
    (
        "009_document_extractions",
        "05454985273dd459ac7a1bd3fd0cc76bdeb376b8ad4a7bf0b7eb4a23cd35afa7",
    ),
    (
        "010_mail_messages",
        "fff7ee186920b719adf02039c8ff40eb1e118095ae0c097a384807eb265847e2",
    ),
    (
        "011_document_provenance",
        "b68ec36c8c36a45b3c664e85fb6f4755b42dfc490cc8c6b5a26c9e2fd2972115",
    ),
    (
        "012_mail_poll_status",
        "6b9d3bd9684465414489abfd68a65689bd2a51e9ebf7b2118b15dd04f7161aff",
    ),
    (
        "013_mail_poll_cursor",
        "69e7d0119c2a8b0344f055275c3ba2df32108f4b894145d1712a01efefc050d5",
    ),
    (
        "014_document_vault_scope",
        "842895a53d1f57ac491ad3597f16d25753e1f6c7469fa64b6f9e86b4941eebce",
    ),
];

#[test]
fn migration_bytes_match_go() {
    use sha2::{Digest, Sha256};
    assert_eq!(symdesk_ingest::MIGRATIONS.len(), GO_MIGRATION_SHA256.len());
    for ((name, sql), (go_name, go_digest)) in
        symdesk_ingest::MIGRATIONS.iter().zip(GO_MIGRATION_SHA256)
    {
        assert_eq!(name, go_name);
        assert_eq!(
            format!("{:x}", Sha256::digest(sql.as_bytes())),
            *go_digest,
            "{name}"
        );
    }
}
