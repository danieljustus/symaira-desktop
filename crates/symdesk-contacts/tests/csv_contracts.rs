use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use symdesk_contacts::import::{ColumnMapping, detect_column_mapping, parse_csv};
use symdesk_contacts::pipeline::normalize;
#[test]
fn go_csv_contracts() {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../testdata/port/contacts/csv.json")).unwrap();
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    for (path, expected) in fixture["source_hashes"].as_object().unwrap() {
        assert_eq!(
            format!(
                "{:x}",
                Sha256::digest(std::fs::read(root.join(path)).unwrap())
            ),
            expected.as_str().unwrap(),
            "{path}"
        );
    }
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 77);
    for case in cases {
        let mapping: ColumnMapping = serde_json::from_value(case["Mapping"].clone()).unwrap();
        if let Some(header) = case["Header"].as_array() {
            let header: Vec<String> = header.iter().map(|v| v.as_str().unwrap().into()).collect();
            assert_eq!(
                detect_column_mapping(&header),
                mapping,
                "{} mapping",
                case["ID"]
            );
        }
        let (rows, issues, error) = match parse_csv(case["Input"].as_str().unwrap(), &mapping) {
            Ok((r, i)) => (json!(r), json!(i), String::new()),
            Err(e) => (Value::Null, Value::Null, e),
        };
        assert_eq!(error, case["Error"], "{} error", case["ID"]);
        assert_eq!(rows, case["Rows"], "{} rows", case["ID"]);
        assert_eq!(issues, case["Issues"], "{} issues", case["ID"]);
    }
    for case in fixture["normalizations"].as_array().unwrap() {
        assert_eq!(
            normalize(
                case["Kind"].as_str().unwrap(),
                case["Raw"].as_str().unwrap()
            ),
            case["Expected"],
            "{case}"
        );
    }
}
