#[path = "../../../scripts/rust-port/rust/oracle_identity.rs"]
mod oracle_identity;

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    sync::atomic::{AtomicU64, Ordering},
};

use base64::{Engine as _, engine::general_purpose::STANDARD as BASE64};
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

    fn import(&mut self, source: &Path, input_options: &Value) -> Result<Value, String> {
        let schema: BTreeMap<String, PropertyConfig> = input_options
            .get("schema")
            .filter(|schema| !schema.is_null())
            .map(|schema| serde_json::from_value(schema.clone()).expect("input schema"))
            .unwrap_or_default();
        let options = DatasetImportOptions {
            title: input_options["title"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            slug: input_options["slug"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            identity_field: input_options["identity_field"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            schema,
            refresh_command: input_options["refresh_command"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            sensitivity: input_options["sensitivity"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            retention_rule: input_options["retention_rule"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            now: Some(
                OffsetDateTime::parse(
                    input_options["now"].as_str().expect("input import time"),
                    &Rfc3339,
                )
                .expect("fixture input time"),
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
    let value: Value =
        serde_json::from_str(FIXTURE).expect("parse Go-owned dataset import fixture");
    oracle_identity::validate_live_document("testdata/port/dataset/import.json", &value)
        .expect("canonical live source identity");
    value
}

#[test]
fn every_recorded_service_rejection_executes_against_the_native_go_oracle() {
    let mut sandbox = Sandbox::new();
    let oracle_path = sandbox.parent.join("native-service-errors.json");
    let repository = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let output = Command::new("go")
        .current_dir(repository)
        .args([
            "test",
            "-count=1",
            "./internal/service",
            "-run",
            "^TestPortDatasetImportServiceRejections$",
        ])
        .env("PORT_DATASET_ERROR_FIXTURE", &oracle_path)
        .env_remove("PORT_GENERATE")
        .output()
        .expect("run the current Go DatasetImport service oracle");
    assert!(
        output.status.success(),
        "Go service oracle failed: stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let native: Value =
        serde_json::from_slice(&fs::read(&oracle_path).expect("fresh native errors"))
            .expect("decode native error fixture");
    let recorded: Value =
        serde_json::from_str(include_str!("../../../testdata/port/dataset/sync.json"))
            .expect("recorded import-helper ledger");
    let cases = native.as_array().expect("native error cases");
    assert_eq!(cases.len(), 5);
    assert_eq!(
        recorded["error_cases"].as_array().expect("ledger").len(),
        cases.len()
    );

    let source = sandbox.parent.join("source.csv");
    fs::write(&source, b"id,a\n1,2\n").expect("CSV input");
    let text_source = sandbox.parent.join("source.txt");
    fs::write(&text_source, b"id,a\n1,2\n").expect("non-CSV input");
    for (index, case) in cases.iter().enumerate() {
        assert_eq!(case["name"], recorded["error_cases"][index]["name"]);
        let name = case["name"].as_str().expect("case name");
        let options = DatasetImportOptions {
            title: "Orders".to_owned(),
            identity_field: "id".to_owned(),
            slug: if name == "slug-not-filesystem-safe" {
                "../escape".to_owned()
            } else {
                String::new()
            },
            ..DatasetImportOptions::default()
        };
        let input = match name {
            "non-csv-source" => text_source.clone(),
            "missing-source-file" => sandbox.parent.join("absent.csv"),
            _ => source.clone(),
        };
        let mut service = match name {
            "missing-vault" => DatasetSyncService::from_parts(None, Some(&mut sandbox.sidecar)),
            "missing-sidecar" => DatasetSyncService::from_parts(Some(&sandbox.root), None),
            "non-csv-source" | "slug-not-filesystem-safe" | "missing-source-file" => {
                DatasetSyncService::new(&sandbox.root, &mut sandbox.sidecar)
            }
            _ => panic!("unreplayed Go rejection {name}"),
        };
        let actual = service
            .import_csv(&input, options)
            .expect_err("Go rejected this import")
            .to_string()
            .replace(
                sandbox.parent.to_str().expect("fixture-owned root"),
                "<tmp>",
            );
        assert_eq!(
            actual,
            case["error"].as_str().expect("Go error"),
            "case {name}"
        );
        assert_eq!(
            fs::read_dir(&sandbox.root)
                .expect("vault remains readable")
                .count(),
            0,
            "case {name} wrote an authoritative vault file"
        );
        assert!(
            sandbox
                .sidecar
                .dataset_rows("orders")
                .expect("sidecar rows")
                .is_empty()
        );
    }
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
                let mut entry = json!({
                    "path": relative,
                    "kind": "file",
                    "size": bytes.len(),
                    "sha256": symdesk_vault::sha256_hex(&bytes),
                });
                if let Ok(text) = std::str::from_utf8(&bytes) {
                    entry["content"] = json!(text);
                } else {
                    entry["content_base64"] = json!(BASE64.encode(&bytes));
                }
                output.push(entry);
            }
        }
    }

    let mut vault = Vec::new();
    visit(&sandbox.root, &sandbox.root, &mut vault);
    vault.sort_by_key(|entry| entry["path"].as_str().unwrap_or_default().to_owned());
    let rows = sandbox.sidecar.dataset_rows_bytes(slug).expect("read raw dataset sidecar rows").into_iter().map(|row| {
        let mut encoded = json!({"dataset_slug": row.dataset_slug, "row_key": symdesk_vault::dataset::bytes::text(&row.row_key), "identity": symdesk_vault::dataset::bytes::text(&row.identity), "values_json": row.values_json, "source_path": row.source_path, "row_number": row.row_number});
        if std::str::from_utf8(&row.row_key).is_err() { encoded["row_key_base64"] = json!(BASE64.encode(&row.row_key)); }
        if std::str::from_utf8(&row.identity).is_err() { encoded["identity_base64"] = json!(BASE64.encode(&row.identity)); }
        encoded
    }).collect::<Vec<_>>();
    let mut state = json!({"label": label, "vault": vault, "rows": rows});
    let handle_path = format!("datasets/{slug}.md");
    match fs::read(sandbox.root.join(&handle_path)) {
        Ok(handle_bytes) => {
            let handle =
                parse_dataset_handle(&handle_path, &handle_bytes).expect("parse Markdown handle");
            state["handle"] = json!(handle);
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => panic!("read Markdown handle: {error}"),
    }
    state
}

#[test]
fn source_import_cases_match_go_oracle() {
    let fixture = fixture();
    let cases = fixture["cases"].as_array().expect("cases");
    assert_eq!(cases.len(), 22, "complete Go import case inventory");
    let mut mismatches = Vec::new();
    for case in cases {
        let calls = case["calls"].as_array().expect("calls");
        let states = case["states"].as_array().expect("states");
        let inputs = case["inputs"].as_array().expect("inputs");
        assert_eq!(inputs.len(), calls.len(), "{} input/call count", case["id"]);
        assert_eq!(
            inputs.len(),
            states.len(),
            "{} input/state count",
            case["id"]
        );

        let mut sandbox = Sandbox::new();
        for (index, input) in inputs.iter().enumerate() {
            let expected_state = &states[index];
            let source = sandbox
                .parent
                .join(input["source_name"].as_str().expect("source name"));
            let csv = if let Some(raw) = input["csv_base64"].as_str() {
                assert_eq!(input["csv"].as_str(), Some(""), "ambiguous CSV input");
                BASE64.decode(raw).expect("Go-owned raw CSV bytes")
            } else {
                input["csv"]
                    .as_str()
                    .expect("CSV input")
                    .as_bytes()
                    .to_vec()
            };
            fs::write(&source, csv).expect("write selected source CSV");
            let result = sandbox.import(&source, &input["options"]);
            let expected = if let Some(expected_result) = calls[index].get("result") {
                Ok(expected_result.clone())
            } else {
                Err(calls[index]["error"].as_str().expect("Go error").to_owned())
            };
            if result != expected {
                mismatches.push(format!(
                    "{} call {index}: expected {expected:?}, got {result:?}",
                    case["id"]
                ));
            }
            let state = actual_state(
                &sandbox,
                expected_state["label"].as_str().unwrap(),
                input["options"]["slug"].as_str().unwrap(),
            );
            if state != *expected_state {
                mismatches.push(format!("{} persisted state after import {index}: expected {expected_state}, got {state}",case["id"]));
            }
        }
    }
    assert!(mismatches.is_empty(), "{}", mismatches.join("\n"));
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
