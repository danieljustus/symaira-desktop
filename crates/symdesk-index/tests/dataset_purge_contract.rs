use std::{
    collections::BTreeMap,
    fs,
    path::PathBuf,
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
}
