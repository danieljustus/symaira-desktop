use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use serde_json::{Value, json};
use symdesk_index::{DatasetImportOptions, DatasetSyncService, Sidecar};
use symdesk_vault::{PropertyConfig, parse_dataset_handle};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

static COUNTER: AtomicU64 = AtomicU64::new(0);
const FIXTURE: &str = include_str!("../../../testdata/port/dataset/import.json");

struct Sandbox {
    root: PathBuf,
    parent: PathBuf,
    sidecar: Sidecar,
}

impl Sandbox {
    fn new() -> Self {
        let counter = COUNTER.fetch_add(1, Ordering::Relaxed);
        let parent = std::env::temp_dir().join(format!(
            "symdesk-dataset-import-rust-{}-{counter}",
            std::process::id()
        ));
        let _ = fs::remove_dir_all(&parent);
        fs::create_dir_all(parent.join("vault")).expect("create vault");
        fs::create_dir_all(parent.join("state")).expect("create state");
        let sidecar = Sidecar::open(&parent.join("state/sidecar.db")).expect("open sidecar");
        Self {
            root: parent.join("vault"),
            parent,
            sidecar,
        }
    }

    fn import(&mut self, source: &Path, handle: &Value) -> Result<Value, String> {
        let schema: BTreeMap<String, PropertyConfig> =
            serde_json::from_value(handle["schema"].clone()).expect("handle schema");
        let options = DatasetImportOptions {
            title: handle["title"].as_str().expect("handle title").to_owned(),
            slug: handle["slug"].as_str().expect("handle slug").to_owned(),
            identity_field: handle["identity_field"]
                .as_str()
                .expect("identity field")
                .to_owned(),
            schema,
            refresh_command: handle["refresh_command"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            sensitivity: handle["sensitivity"]
                .as_str()
                .expect("handle sensitivity")
                .to_owned(),
            retention_rule: handle["retention_rule"]
                .as_str()
                .expect("handle retention rule")
                .to_owned(),
            now: Some(
                OffsetDateTime::parse(
                    handle["provenance"]["imported_at"]
                        .as_str()
                        .expect("handle import time"),
                    &Rfc3339,
                )
                .expect("fixture import time"),
            ),
        };
        DatasetSyncService::new(&self.root, &mut self.sidecar)
            .import_csv(source, options)
            .map(|result| serde_json::to_value(result).expect("serialize result"))
            .map_err(|error| error.to_string())
    }
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.parent);
    }
}

fn fixture() -> Value {
    serde_json::from_str(FIXTURE).expect("parse Go-owned dataset import fixture")
}

fn file_entry<'a>(state: &'a Value, path: &str) -> &'a Value {
    state["vault"]
        .as_array()
        .expect("vault manifest")
        .iter()
        .find(|entry| entry["path"] == path)
        .unwrap_or_else(|| panic!("missing Go fixture vault entry {path}"))
}

fn actual_state(sandbox: &Sandbox, label: &str, slug: &str) -> Value {
    fn visit(root: &Path, current: &Path, output: &mut Vec<Value>) {
        let mut entries = fs::read_dir(current)
            .expect("read vault directory")
            .map(|entry| entry.expect("read vault entry"))
            .collect::<Vec<_>>();
        entries.sort_by_key(fs::DirEntry::file_name);
        for entry in entries {
            let path = entry.path();
            let relative = path
                .strip_prefix(root)
                .expect("relative vault path")
                .to_string_lossy()
                .replace('\\', "/");
            let metadata = entry.metadata().expect("vault metadata");
            if metadata.is_dir() {
                output.push(json!({"path": relative, "kind": "directory"}));
                visit(root, &path, output);
            } else {
                let bytes = fs::read(&path).expect("read vault file");
                output.push(json!({
                    "path": relative,
                    "kind": "file",
                    "size": bytes.len(),
                    "sha256": symdesk_vault::sha256_hex(&bytes),
                    "content": String::from_utf8(bytes).expect("UTF-8 fixture vault file"),
                }));
            }
        }
    }

    let mut vault = Vec::new();
    visit(&sandbox.root, &sandbox.root, &mut vault);
    vault.sort_by_key(|entry| entry["path"].as_str().unwrap_or_default().to_owned());
    let rows = sandbox
        .sidecar
        .dataset_rows(slug)
        .expect("read dataset sidecar rows")
        .into_iter()
        .map(|row| {
            json!({
                "dataset_slug": row.dataset_slug,
                "row_key": row.row_key,
                "identity": row.identity,
                "values_json": row.values_json,
                "source_path": row.source_path,
                "row_number": row.row_number,
            })
        })
        .collect::<Vec<_>>();
    let handle_path = format!("datasets/{slug}.md");
    let handle_bytes = fs::read(sandbox.root.join(&handle_path)).expect("read Markdown handle");
    let handle = parse_dataset_handle(&handle_path, &handle_bytes).expect("parse Markdown handle");
    json!({"label": label, "vault": vault, "rows": rows, "handle": handle})
}

#[test]
fn source_import_and_same_day_collision_match_go_oracle() {
    let fixture = fixture();
    let cases = fixture["cases"].as_array().expect("cases");
    assert_eq!(cases.len(), 1, "case inventory changed");
    assert_eq!(
        cases[0]["id"],
        "same-day-source-import-collision-and-manifest-projection"
    );
    let calls = cases[0]["calls"].as_array().expect("calls");
    let states = cases[0]["states"].as_array().expect("states");
    assert_eq!(calls.len(), 2);
    assert_eq!(states.len(), 2);

    let mut sandbox = Sandbox::new();
    let inputs = [
        ("first-feed.CSV", "datasets/ledger/2026-02-03.csv"),
        ("second-feed.csv", "datasets/ledger/2026-02-03-2.csv"),
    ];
    for (index, (source_name, raw_path)) in inputs.iter().enumerate() {
        let expected_state = &states[index];
        let expected_handle = &expected_state["handle"];
        let source = sandbox.parent.join(source_name);
        fs::write(
            &source,
            file_entry(expected_state, raw_path)["content"]
                .as_str()
                .expect("raw source bytes"),
        )
        .expect("write selected source CSV");
        let result = sandbox.import(&source, expected_handle);
        assert_eq!(
            result,
            Ok(calls[index]["result"].clone()),
            "DatasetImport result {index}"
        );
        assert_eq!(
            actual_state(
                &sandbox,
                expected_state["label"].as_str().unwrap(),
                "ledger"
            ),
            *expected_state,
            "persisted state after import {index}"
        );
    }
}

#[test]
fn imported_schema_preserves_declared_metadata_and_infers_types() {
    let fixture = fixture();
    let result = &fixture["cases"][0]["calls"][0]["result"];
    let columns: BTreeMap<String, PropertyConfig> =
        serde_json::from_value(result["columns"].clone()).expect("result columns");
    assert_eq!(columns["id"].r#type, "text");
    assert_eq!(columns["amount"].r#type, "number");
    assert_eq!(columns["amount"].description, "Ledger amount");
    assert_eq!(columns["amount"].default, "0");
    assert_eq!(columns["when"].r#type, "date");
}
