use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use serde_json::{Value, json};
use symdesk_index::{
    DatasetPurgeService, DatasetSyncOptions, DatasetSyncRow, DatasetSyncService, Sidecar,
};
use symdesk_vault::{HistoryStore, Provenance, retention_state::retention_state};

static COUNTER: AtomicU64 = AtomicU64::new(0);
const FIXTURE: &str = include_str!("../../../testdata/port/dataset/purge.json");

struct Sandbox {
    parent: PathBuf,
    root: PathBuf,
    db: Sidecar,
}

impl Sandbox {
    fn new(name: &str) -> Self {
        let n = COUNTER.fetch_add(1, Ordering::Relaxed);
        let parent = std::env::temp_dir().join(format!(
            "symdesk-rust-purge-{name}-{}-{n}",
            std::process::id()
        ));
        let root = parent.join("vault");
        fs::create_dir_all(&root).expect("create vault");
        let db = Sidecar::open(&parent.join("sidecar.db")).expect("open sidecar");
        Self { parent, root, db }
    }

    fn setup(&mut self) {
        let options = DatasetSyncOptions {
            title: "Orders".into(),
            slug: "orders".into(),
            identity_field: "id".into(),
            provenance: Provenance {
                imported_at: "2026-01-04T00:00:00Z".into(),
                source_name: "feed".into(),
                source_sha256: "policy-sha".into(),
            },
            sensitivity: "restricted".into(),
            retention_rule: "default".into(),
            rows: vec![DatasetSyncRow {
                identity: "o1".into(),
                values: BTreeMap::from([
                    ("amount".into(), json!(12.5)),
                    ("id".into(), json!("o1")),
                ]),
            }],
            ..DatasetSyncOptions::default()
        };
        DatasetSyncService::new(&self.root, &mut self.db)
            .sync(options)
            .expect("seed dataset");
    }

    fn setup_recovery(&self) {
        let raw_rel = "datasets/orders/2026-01-04.csv";
        let raw_path = self.root.join(raw_rel);
        let raw_bytes = fs::read(&raw_path).expect("read original raw source");
        fs::create_dir_all(self.root.join("notes")).expect("create notes dir");
        fs::write(
            self.root.join("notes/keep.md"),
            b"unrelated checkpoint content",
        )
        .expect("write unrelated note");
        let history = HistoryStore::new(&self.root);
        for path in ["datasets/orders.md", "notes/keep.md", raw_rel] {
            history
                .checkpoint_file("retention-task", path)
                .expect("checkpoint file");
        }
        history.trash(raw_rel).expect("trash dataset source");
        fs::write(raw_path, raw_bytes).expect("restore active source copy");
    }
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.parent);
    }
}

#[test]
fn dataset_purge_matches_go_service_fixture() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("Go fixture JSON");
    assert_eq!(fixture["schema_version"], 1);
    for case in fixture["cases"].as_array().expect("cases") {
        let id = case["id"].as_str().expect("case id");
        let stale = case["stale"].as_bool().expect("stale input");
        let mut sandbox = Sandbox::new(id);
        sandbox.setup();
        if !stale {
            sandbox.setup_recovery();
        }
        let state = retention_state(&sandbox.root, "datasets/orders.md").expect("retention state");
        if stale {
            fs::write(
                sandbox.root.join("datasets/orders/2026-01-04.csv"),
                b"id,amount\no1,99\n",
            )
            .expect("mutate raw snapshot");
        }
        let result = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db).purge(
            "orders",
            "default",
            &state.fingerprint,
        );
        let error = result.err().map(|e| e.to_string()).unwrap_or_default();
        let handle_exists = sandbox.root.join("datasets/orders.md").exists();
        let raw_exists = sandbox.root.join("datasets/orders/2026-01-04.csv").exists();
        let rows = sandbox
            .db
            .dataset_rows("orders")
            .expect("read sidecar rows")
            .len();
        let journal_exists = sandbox
            .root
            .join(".symdesk/dataset-purge/orders.json")
            .exists();
        let history = HistoryStore::new(&sandbox.root);
        let mut dataset_trash = 0;
        for entry in history.trash_list_strict().expect("list strict trash") {
            if entry.original_path == "datasets/orders.md"
                || entry.original_path.starts_with("datasets/orders/")
            {
                dataset_trash += 1;
            }
        }
        let raw_history_entries = history
            .list("datasets/orders/2026-01-04.csv")
            .expect("list raw history")
            .map_or(0, |entries| entries.len());
        let checkpoint_paths: Vec<String> = history
            .list_checkpoints()
            .expect("list checkpoints")
            .into_iter()
            .flat_map(|checkpoint| checkpoint.files.into_iter().map(|file| file.rel_path))
            .collect();
        let history_objects = fs::read_dir(sandbox.root.join(".symdesk/history/objects"))
            .map(|entries| entries.count())
            .unwrap_or(0);
        assert_eq!(handle_exists, case["handle_exists"], "case {id}");
        assert_eq!(raw_exists, case["raw_exists"], "case {id}");
        assert_eq!(rows, case["rows"], "case {id}");
        assert_eq!(journal_exists, case["journal_exists"], "case {id}");
        assert_eq!(dataset_trash, case["dataset_trash"], "case {id}");
        assert_eq!(
            raw_history_entries, case["raw_history_entries"],
            "case {id}"
        );
        let expected_checkpoint_paths: Vec<String> = case["checkpoint_paths"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|value| value.as_str().expect("checkpoint path").to_owned())
            .collect();
        assert_eq!(checkpoint_paths, expected_checkpoint_paths, "case {id}");
        assert_eq!(history_objects, case["history_objects"], "case {id}");
        let expected_error = case["error"].as_str().unwrap_or_default();
        if expected_error.is_empty() {
            assert!(error.is_empty(), "case {id}: unexpected error {error}");
        } else {
            assert!(
                error.contains(expected_error),
                "case {id}: expected {expected_error:?}, got {error:?}"
            );
        }
    }

    let recovery_cases = fixture["recovery_cases"]
        .as_array()
        .expect("recovery cases");
    assert_eq!(recovery_cases.len(), 3);
    for case in recovery_cases {
        let id = case["id"].as_str().expect("recovery case id");
        let mut sandbox = Sandbox::new(id);
        sandbox.setup();
        seed_go_view_file(&sandbox, &recovery_cases[0]);
        match id {
            "corrupt-history-fails-before-mutation" => {
                let manifest = sandbox
                    .root
                    .join(".symdesk/history/manifest/datasets/orders.md.json");
                fs::create_dir_all(manifest.parent().expect("manifest parent"))
                    .expect("create manifest parent");
                fs::write(&manifest, b"null").expect("write corrupt history manifest");
                let before = snapshot(&sandbox);
                let result = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                    .purge("orders", "default", "");
                let error = result
                    .err()
                    .map(|error| error.to_string())
                    .unwrap_or_default();
                let after = snapshot(&sandbox);
                assert!(
                    error.contains(case["error"].as_str().expect("error fragment")),
                    "case {id}: unexpected error {error:?}"
                );
                assert_eq!(before, case["before"], "case {id} before mutation");
                assert_eq!(after, case["after"], "case {id} after mutation");
                assert_eq!(
                    before, after,
                    "case {id} changed state before preflight completed"
                );
            }
            "replacement-trash-retry-fails-closed" => {
                let raw_rel = "datasets/orders/2026-01-04.csv";
                let entry = HistoryStore::new(&sandbox.root)
                    .trash(raw_rel)
                    .expect("trash dataset raw file");
                sandbox
                    .db
                    .close()
                    .expect("close sidecar for recovery setup");
                let first_result = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                    .purge("orders", "default", "");
                let first_error = first_result
                    .err()
                    .map(|error| error.to_string())
                    .unwrap_or_default();
                assert!(
                    first_error.contains(
                        case["initial_error"]
                            .as_str()
                            .expect("initial error fragment")
                    ),
                    "case {id}: initial failure was {first_error:?}"
                );
                let trash_path = sandbox.root.join(".symdesk/trash").join(&entry.name);
                fs::write(&trash_path, b"replacement payload")
                    .expect("replace trash payload after journal creation");
                sandbox.db = Sidecar::open(&sandbox.parent.join("sidecar.db"))
                    .expect("reopen sidecar for recovery retry");
                let before = snapshot(&sandbox);
                let retry_result = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                    .purge("orders", "default", "");
                let retry_error = retry_result
                    .err()
                    .map(|error| error.to_string())
                    .unwrap_or_default();
                let after = snapshot(&sandbox);
                // Windows' fallback file identity includes size/mtime, so a
                // rewritten trash payload fails at identity before its hash.
                // Both errors must leave the replacement and journal intact.
                let windows_replaced = cfg!(windows)
                    && retry_error == format!("dataset trash {} was replaced", entry.name);
                assert!(
                    windows_replaced
                        || retry_error
                            .contains(case["error"].as_str().expect("retry error fragment")),
                    "case {id}: unexpected retry error {retry_error:?}"
                );
                assert_eq!(before, case["before"], "case {id} before retry");
                assert_eq!(after, case["after"], "case {id} after retry");
                assert_eq!(
                    fs::read(&trash_path).expect("replacement trash payload survives"),
                    b"replacement payload",
                    "case {id} deleted replacement trash"
                );
            }
            "symlink-journal-fails-before-mutation" => {
                sandbox.db.close().expect("close sidecar for journal setup");
                let setup_result = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                    .purge("orders", "default", "");
                let setup_error = setup_result
                    .err()
                    .map(|error| error.to_string())
                    .unwrap_or_default();
                assert!(setup_error.contains("closed"), "case {id}: {setup_error:?}");

                let journal_dir = sandbox.root.join(".symdesk/dataset-purge");
                let journal = journal_dir.join("orders.json");
                let target = journal_dir.join("valid-journal.json");
                fs::rename(&journal, &target).expect("move valid journal to symlink target");
                create_file_symlink(Path::new("valid-journal.json"), &journal)
                    .expect("create journal symlink");
                sandbox.db = Sidecar::open(&sandbox.parent.join("sidecar.db"))
                    .expect("reopen sidecar for symlink test");
                let before = snapshot(&sandbox);
                let target_before = fs::read(&target).expect("read valid target journal");
                let result = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                    .purge("orders", "default", "");
                let error = result
                    .err()
                    .map(|error| error.to_string())
                    .unwrap_or_default();
                let after = snapshot(&sandbox);
                assert_eq!(error, case["error"].as_str().expect("exact error"));
                assert_eq!(before, case["before"], "case {id} before mutation");
                assert_eq!(after, case["after"], "case {id} after mutation");
                assert_eq!(before, after, "case {id} mutated state");
                assert_eq!(
                    fs::read(&target).expect("valid target journal survives"),
                    target_before,
                    "case {id} changed journal target"
                );
            }
            other => panic!("unknown dataset purge recovery case {other:?}"),
        }
    }
}

#[cfg(unix)]
fn create_file_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
    std::os::unix::fs::symlink(target, link)
}

#[cfg(windows)]
fn create_file_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
    std::os::windows::fs::symlink_file(target, link)
}

fn snapshot(sandbox: &Sandbox) -> Value {
    let mut files = Vec::new();
    visit_snapshot(sandbox.root.as_path(), sandbox.root.as_path(), &mut files);
    files.sort_by(|left: &Value, right: &Value| left["path"].as_str().cmp(&right["path"].as_str()));
    let rows = sandbox
        .db
        .dataset_rows("orders")
        .expect("read dataset rows for snapshot")
        .into_iter()
        .map(|row| {
            json!({
                "row_key": row.row_key,
                "identity": row.identity,
                "values_json": row.values_json,
                "source_path": row.source_path,
                "row_number": row.row_number,
            })
        })
        .collect::<Vec<_>>();
    json!({"files": files, "rows": rows})
}

fn seed_go_view_file(sandbox: &Sandbox, fixture_case: &Value) {
    let file = fixture_case["before"]["files"]
        .as_array()
        .expect("Go recovery snapshot files")
        .iter()
        .find(|file| file["path"] == "bases/orders.md")
        .expect("Go-owned dataset view file");
    let content = file["content"].as_str().expect("Go view content");
    fs::create_dir_all(sandbox.root.join("bases")).expect("create bases directory");
    fs::write(sandbox.root.join("bases/orders.md"), content).expect("seed Go view file");
}

fn visit_snapshot(root: &Path, directory: &Path, output: &mut Vec<Value>) {
    let mut entries = fs::read_dir(directory)
        .expect("read dataset purge snapshot directory")
        .map(|entry| entry.expect("read dataset purge snapshot entry"))
        .collect::<Vec<_>>();
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        let path = entry.path();
        let relative = path
            .strip_prefix(root)
            .expect("snapshot path under vault")
            .to_string_lossy()
            .replace('\\', "/");
        let metadata = fs::symlink_metadata(&path).expect("snapshot metadata");
        if metadata.is_dir() {
            output.push(json!({"path": relative, "kind": "directory"}));
            visit_snapshot(root, &path, output);
            continue;
        }
        if metadata.file_type().is_symlink() {
            let target = fs::read_link(&path).expect("read snapshot symlink");
            output.push(json!({
                "path": relative,
                "kind": "symlink",
                "target": target.to_string_lossy().replace('\\', "/"),
            }));
            continue;
        }
        let mut bytes = fs::read(&path).expect("read snapshot file");
        let mut journal_content = None;
        if let Ok(mut value) = serde_json::from_slice::<Value>(&bytes) {
            normalize_times(&mut value);
            // Go json.Marshal sorts map keys even when another workspace crate
            // enables serde_json's preserve_order feature for this test binary.
            value.sort_all_objects();
            bytes = serde_json::to_vec(&value).expect("normalize snapshot JSON");
        } else if let Ok(text) = std::str::from_utf8(&bytes) {
            bytes = normalize_text_times(text).into_bytes();
        }
        if relative == ".symdesk/dataset-purge/orders.json" || relative == "bases/orders.md" {
            journal_content = Some(String::from_utf8(bytes.clone()).expect("snapshot file UTF-8"));
        }
        let mut file = json!({
            "path": relative,
            "kind": "file",
            "size": bytes.len(),
            "sha256": symdesk_vault::sha256_hex(&bytes),
        });
        if let Some(content) = journal_content {
            file["content"] = Value::String(content);
        }
        output.push(file);
    }
}

fn normalize_times(value: &mut Value) {
    match value {
        Value::Object(map) => {
            for (key, child) in map.iter_mut() {
                if matches!(
                    key.as_str(),
                    "timestamp"
                        | "deleted_at"
                        | "created"
                        | "imported_at"
                        | "identity"
                        | "payload_identity"
                        | "metadata_identity"
                        | "fingerprint"
                        | "metadata_hash"
                ) {
                    *child = Value::String("{{timestamp}}".to_owned());
                } else {
                    normalize_times(child);
                }
            }
        }
        Value::Array(values) => {
            for child in values {
                normalize_times(child);
            }
        }
        _ => {}
    }
}

fn normalize_text_times(text: &str) -> String {
    let mut output = String::with_capacity(text.len());
    for line in text.split_inclusive('\n') {
        let without_newline = line.strip_suffix('\n').unwrap_or(line);
        let trimmed = without_newline.trim_start();
        let (key, _) = trimmed.split_once(':').unwrap_or((trimmed, ""));
        if matches!(key, "created" | "imported_at") {
            let indent = &without_newline[..without_newline.len() - trimmed.len()];
            output.push_str(indent);
            output.push_str(key);
            output.push_str(": \"{{timestamp}}\"");
            if line.ends_with('\n') {
                output.push('\n');
            }
        } else {
            output.push_str(line);
        }
    }
    output
}
