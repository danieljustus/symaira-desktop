use serde_json::{Value, json};
use std::{fs, path::Path};
use symdesk_ingest::{
    paperless::{Document, DownloadMetadata},
    paperless_import::*,
};
fn fixture() -> Value {
    serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/ingest/paperless-mapping.json"),
        )
        .unwrap(),
    )
    .unwrap()
}
fn replay(f: &Value) -> Result<(), String> {
    let lookup: Lookups = serde_json::from_value(f["lookups"].clone()).unwrap();
    let mut docs = Vec::new();
    let check = |id: &str, actual: Value, expected: &Value| {
        if &actual == expected {
            Ok(())
        } else {
            Err(format!("{id}\nactual {actual:#}\nexpected {expected:#}"))
        }
    };
    for case in f["cases"].as_array().unwrap() {
        let doc: Document = serde_json::from_value(case["input"].clone()).unwrap();
        check(
            "resolved",
            json!(resolve_document_meta(&doc, &lookup)),
            &case["resolved"],
        )?;
        check(
            "planned",
            json!(planned_document_result(&doc, "fixed-run", "planned")),
            &case["planned"],
        )?;
        check("created", json!(paperless_created(&doc)), &case["created"])?;
        docs.push(doc);
    }
    check(
        "audit",
        json!(build_audit_report(&docs, &lookup)),
        &f["audit"],
    )?;
    check(
        "empty audit",
        json!(build_audit_report(&[], &lookup)),
        &f["empty_audit"],
    )?;
    for case in f["extensions"].as_array().unwrap() {
        check(
            "extension",
            json!(normalize_extension(case["input"].as_str().unwrap())),
            &case["output"],
        )?;
    }
    for case in f["downloads"].as_array().unwrap() {
        let doc = docs.iter().find(|d| json!(d.id) == case["doc_id"]).unwrap();
        let meta: DownloadMetadata = serde_json::from_value(case["metadata"].clone()).unwrap();
        check(
            "download",
            json!(download_extension(doc, &meta)),
            &case["extension"],
        )?;
    }
    for case in f["storage_paths"].as_array().unwrap() {
        check(
            "storage path",
            json!(sanitize_storage_path(case["input"].as_str().unwrap()).replace('\\', "/")),
            &case["output"],
        )?;
    }
    for case in f["stages"].as_array().unwrap() {
        let input = case["input"].as_str().unwrap();
        check(
            "stage",
            json!(stage_from_error(input, None)),
            &case["stage"],
        )?;
        check(
            "explicit stage",
            json!(stage_from_error(input, Some("custom"))),
            &case["explicit_stage"],
        )?;
        check(
            "bounded",
            json!(bounded_error_string(input)),
            &case["bounded"],
        )?;
    }
    Ok(())
}
#[test]
fn paperless_mapping_go_contract() {
    let f = fixture();
    assert_eq!(f["cases"].as_array().unwrap().len(), 4);
    replay(&f).unwrap();
}
#[test]
fn paperless_mapping_negative_control() {
    let mut f = fixture();
    replay(&f).unwrap();
    f["cases"][0]["resolved"]["Correspondent"] = json!("wrong");
    assert!(replay(&f).unwrap_err().starts_with("resolved"));
}
