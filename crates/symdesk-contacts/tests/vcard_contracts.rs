use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use symdesk_contacts::vcard::parse_vcard;
#[test]
fn go_vcard_contracts() {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../testdata/port/contacts/vcard.json")).unwrap();
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
    assert_eq!(cases.len(), 40);
    for case in cases {
        let (rows, issues, error) = match parse_vcard(case["Input"].as_str().unwrap()) {
            Ok((r, i)) => (json!(r), json!(i), String::new()),
            Err(e) => (Value::Null, Value::Null, e),
        };
        assert_eq!(error, case["Error"], "{} error", case["ID"]);
        assert_eq!(rows, case["Rows"], "{} rows", case["ID"]);
        assert_eq!(issues, case["Issues"], "{} issues", case["ID"]);
    }
}
