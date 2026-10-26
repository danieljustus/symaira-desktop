use serde::Deserialize;
use symdesk_ingest::extract::{self, Extraction};
#[derive(Clone, Deserialize)]
struct Case {
    id: String,
    name: String,
    input: String,
    kind: String,
    read_kind: String,
    detect_error: String,
    extract: bool,
    result: Option<Extraction>,
    error: String,
}
#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}
fn fixture() -> Fixture {
    serde_json::from_slice(
        &std::fs::read(
            std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/ingest/extraction.json"),
        )
        .unwrap(),
    )
    .unwrap()
}
fn compare(c: &Case) -> bool {
    use base64::{Engine, engine::general_purpose::STANDARD};
    let data = STANDARD.decode(&c.input).unwrap();
    // Explicit kind overrides are used only to exercise the real archive reader
    // on invalid/count-limited containers that Detect intentionally calls unknown.
    let detection = extract::detect_bytes(&c.name, &data).map_err(|e| {
        e.strip_suffix(&c.name)
            .map(|s| format!("{s}$PATH"))
            .unwrap_or(e)
    });
    let expected = if c.detect_error.is_empty() {
        Ok(c.kind.clone())
    } else {
        Err(c.detect_error.clone())
    };
    if detection != expected {
        eprintln!("{} detection {detection:?} != {expected:?}", c.id);
        return false;
    }
    if !c.extract {
        return true;
    }
    let actual = extract::extract_bytes(&data, &c.read_kind, usize::MAX);
    let expected = if c.error.is_empty() {
        Ok(c.result.clone().unwrap())
    } else {
        Err(c.error.clone())
    };
    if actual != expected {
        eprintln!("{} extraction {actual:?} != {expected:?}", c.id);
        return false;
    }
    true
}
#[test]
fn ingest_extraction_go_fixture() {
    let f = fixture();
    assert_eq!(f.schema_version, 1);
    assert_eq!(f.cases.len(), 84);
    for c in &f.cases {
        assert!(compare(c), "{} drift", c.id);
    }
}
#[test]
fn ingest_extraction_negative_control() {
    let mut f = fixture();
    let c = f.cases.iter_mut().find(|c| c.id == "html-0").unwrap();
    assert!(compare(c));
    c.result.as_mut().unwrap().text.push_str("tampered");
    assert!(!compare(c));
}
#[test]
fn caller_bound_is_explicit_safety_divergence() {
    assert_eq!(
        extract::extract_bytes(b"abc", "text/plain", 2).unwrap_err(),
        "extract: source exceeds the caller byte limit of 2 bytes"
    );
    assert_eq!(
        extract::extract_bytes(b"abc", "text/plain", 3)
            .unwrap()
            .text,
        "abc"
    );
}
#[test]
fn optional_engines_remain_explicit_residuals() {
    for kind in [
        "application/pdf",
        "image/png",
        "image/jpeg",
        "message/rfc822",
        symdesk_core::document_format::kind_for_extension("docx").unwrap(),
        symdesk_core::document_format::kind_for_extension("xlsx").unwrap(),
    ] {
        assert_eq!(
            extract::extract_bytes(b"source", kind, 100).unwrap_err(),
            format!("ingest Rust extraction not yet ported for kind {kind:?}")
        );
    }
}
