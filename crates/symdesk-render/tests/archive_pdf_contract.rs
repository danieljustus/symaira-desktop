//! Contract test for pdfcpu image import and the pdfcpu watermark structure
//! against the Go oracle (`testdata/port/render/archive-pdf.json`, RENDER-002).
//!
//! Per case the Rust `archive::generate` writes a PDF and
//! `scripts/rust-port/pdfops/pdfcanon.py` (qpdf based) describes it; the
//! description must equal the one recorded from the PDF Go really wrote. The
//! description ignores writer details (object numbers, compressor output,
//! /Info, /ID) and keeps everything else: page tree, boxes, resources, content
//! streams, image dictionaries and decoded pixel data. Reals compare to five
//! decimals (Go on arm64 fuses multiply-adds).
//!
//! Needs python3 and qpdf. Windows validates the stored oracle only (no qpdf);
//! any other platform fails without them.

use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};

use base64::Engine as _;
use serde_json::Value;
use symdesk_render::archive::generate;

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    helvetica: Helvetica,
    import: Vec<ImportCase>,
    stamp: Vec<StampCase>,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
#[allow(dead_code)]
struct Oracle {
    commit: String,
    go_version: String,
    pdfcpu: String,
    canon_tool: String,
    canon_tool_sha256: String,
    normalisation: String,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Helvetica {
    widths: Vec<i64>,
    fbox: [f64; 4],
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct ImportCase {
    id: String,
    ext: String,
    image: String,
    ocr_text: String,
    want: Want,
    #[serde(default)]
    go_pdf_b64: String,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct StampCase {
    id: String,
    input_pdf: String,
    ocr_text: String,
    want: Want,
    #[serde(default)]
    go_pdf_b64: String,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Want {
    ok: bool,
    error_kind: String,
    canon: Option<Value>,
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "symdesk-archive-pdf-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&path).expect("create temp dir");
        Self(path)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn fixture() -> Fixture {
    let text = std::fs::read_to_string(root().join("testdata/port/render/archive-pdf.json")).expect("read fixture");
    let fixture: Fixture = serde_json::from_str(&text).expect("decode Go-generated archive PDF fixture");
    let provenance: Value = serde_json::from_str(
        &std::fs::read_to_string(root().join("testdata/port/provenance.json")).expect("read provenance"),
    )
    .expect("decode central port provenance");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(Some(fixture.oracle.commit.as_str()), provenance["oracle"]["commit"].as_str());
    assert_eq!(fixture.oracle.go_version, "go1.26.10");
    assert_eq!(fixture.oracle.pdfcpu, "v0.16.2");
    fixture
}

/// True when the measurement tools exist. Only Windows may lack them.
fn tools() -> bool {
    let present = ["python3", "qpdf", "pdftoppm"].iter().all(|tool| Command::new(tool).arg("--version").output().is_ok());
    if present {
        return true;
    }
    if !cfg!(windows) {
        panic!("python3, qpdf and pdftoppm are required on PATH for the archive PDF contract");
    }
    eprintln!("note: python3/qpdf missing on Windows, skipping the PDF measurements");
    false
}

fn canon(pdf: &Path) -> Value {
    let tool = root().join("scripts/rust-port/pdfops/pdfcanon.py");
    let out = Command::new("python3").arg("-I").arg(&tool).arg(pdf).output().expect("run pdfcanon.py");
    assert!(out.status.success(), "pdfcanon: {}", String::from_utf8_lossy(&out.stderr));
    serde_json::from_slice(&out.stdout).expect("decode pdfcanon output")
}

fn run_case(id: &str, input_name: &str, input: &[u8], ocr: &str, want: &Want, go_pdf: &str) -> Result<(), String> {
    let dir = TempDir::new();
    let input_path = dir.0.join(input_name);
    let output_path = dir.0.join("output.pdf");
    std::fs::write(&input_path, input).expect("write input");
    match generate(&input_path, &output_path, ocr) {
        Err(error) => {
            if want.ok {
                return Err(format!("case {id}: Go succeeded, Rust failed: {error}"));
            }
            let kind = error.split(':').next().unwrap_or_default();
            if kind != want.error_kind {
                return Err(format!("case {id}: error kind {kind:?}, want {:?}", want.error_kind));
            }
            Ok(())
        }
        Ok(()) => {
            if !want.ok {
                return Err(format!("case {id}: Go failed with {:?}, Rust succeeded", want.error_kind));
            }
            let got = canon(&output_path);
            let expected = want.canon.as_ref().expect("canon for ok case");
            if !close(&got, expected) {
                return Err(format!(
                    "case {id}: canonical PDF differs from Go\n  rust: {}\n  go:   {}",
                    serde_json::to_string(&got).unwrap(),
                    serde_json::to_string(expected).unwrap()
                ));
            }
            if !go_pdf.is_empty() {
                pixels_equal(id, &dir.0, &output_path, &decode(go_pdf))?;
            }
            Ok(())
        }
    }
}

/// Largest allowed difference between two reals: one unit of the fifth decimal.
/// pdfcpu prints `%.5f` and Go on arm64 fuses multiply-adds, so a value on a
/// rounding boundary prints one unit apart per host. Everything else (names,
/// strings, structure, integers, binary stream hashes) must be identical.
const REAL_TOLERANCE: f64 = 1.1e-5;

fn tokens_close(a: &str, b: &str) -> bool {
    let (mut x, mut y) = (a.split_whitespace(), b.split_whitespace());
    loop {
        match (x.next(), y.next()) {
            (None, None) => return true,
            (Some(p), Some(q)) if p == q => {}
            (Some(p), Some(q)) => match (p.parse::<f64>(), q.parse::<f64>()) {
                (Ok(m), Ok(n)) if p.contains('.') && q.contains('.') && (m - n).abs() <= REAL_TOLERANCE => {}
                _ => return false,
            },
            _ => return false,
        }
    }
}

fn close(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(m), Value::Number(n)) => {
            if m == n {
                return true;
            }
            match (m.as_f64(), n.as_f64()) {
                (Some(p), Some(q)) => (m.is_f64() || n.is_f64()) && (p - q).abs() <= REAL_TOLERANCE,
                _ => false,
            }
        }
        (Value::Array(m), Value::Array(n)) => m.len() == n.len() && m.iter().zip(n).all(|(p, q)| close(p, q)),
        (Value::Object(m), Value::Object(n)) => {
            if m.len() != n.len() || !m.keys().all(|k| n.contains_key(k)) {
                return false;
            }
            let text_stream = m.contains_key("text") && n.contains_key("text");
            m.iter().all(|(k, v)| {
                let w = &n[k];
                if text_stream && k == "len" {
                    true
                } else if text_stream && k == "text" {
                    tokens_close(v.as_str().unwrap_or(""), w.as_str().unwrap_or(""))
                } else {
                    close(v, w)
                }
            })
        }
        _ => a == b,
    }
}

/// Render the PDF Go wrote and the PDF Rust wrote with the same Poppler and
/// require identical pixels on every page (first four pages, 40 dpi).
fn pixels_equal(id: &str, dir: &Path, rust_pdf: &Path, go_bytes: &[u8]) -> Result<(), String> {
    if cfg!(windows) {
        return Ok(());
    }
    let go_pdf = dir.join("go.pdf");
    std::fs::write(&go_pdf, go_bytes).expect("write Go PDF");
    let render = |pdf: &Path, prefix: &str| -> Result<Vec<Vec<u8>>, String> {
        let out = dir.join(prefix);
        let status = Command::new("pdftoppm")
            .args(["-r", "40", "-l", "4", "-q"])
            .arg(pdf)
            .arg(&out)
            .status()
            .map_err(|e| format!("case {id}: pdftoppm: {e}"))?;
        if !status.success() {
            return Err(format!("case {id}: pdftoppm failed on {}", pdf.display()));
        }
        let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
            .unwrap()
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.file_name().is_some_and(|n| n.to_string_lossy().starts_with(&format!("{prefix}-"))))
            .collect();
        files.sort();
        Ok(files.iter().map(|f| std::fs::read(f).unwrap()).collect())
    };
    let (a, b) = (render(rust_pdf, "rust")?, render(&go_pdf, "go")?);
    if a.is_empty() || a != b {
        return Err(format!("case {id}: rendered pixels differ from the PDF Go wrote ({} vs {} pages)", a.len(), b.len()));
    }
    Ok(())
}

fn decode(b64: &str) -> Vec<u8> {
    base64::engine::general_purpose::STANDARD.decode(b64).expect("base64")
}

#[test]
fn import_cases_match_go_pdfcpu() {
    let fixture = fixture();
    assert!(fixture.import.len() >= 60, "fixture shrank: {} import cases", fixture.import.len());
    if !tools() {
        return;
    }
    let failures: Vec<String> = fixture
        .import
        .iter()
        .filter_map(|c| run_case(&c.id, &format!("scan{}", c.ext), &decode(&c.image), &c.ocr_text, &c.want, &c.go_pdf_b64).err())
        .collect();
    assert!(failures.is_empty(), "{} of {} import cases differ:\n{}", failures.len(), fixture.import.len(), failures.join("\n"));
}

#[test]
fn stamp_structure_matches_go_pdfcpu() {
    let fixture = fixture();
    assert!(fixture.stamp.len() >= 40, "fixture shrank: {} stamp cases", fixture.stamp.len());
    if !tools() {
        return;
    }
    let failures: Vec<String> = fixture
        .stamp
        .iter()
        .filter_map(|c| run_case(&c.id, "input.pdf", &decode(&c.input_pdf), &c.ocr_text, &c.want, &c.go_pdf_b64).err())
        .collect();
    assert!(failures.is_empty(), "{} of {} stamp cases differ:\n{}", failures.len(), fixture.stamp.len(), failures.join("\n"));
}

#[test]
fn helvetica_metrics_match_go() {
    let fixture = fixture();
    let (widths, fbox) = symdesk_render::stamp::helvetica_metrics();
    assert_eq!(fixture.helvetica.widths.len(), 256);
    assert_eq!(widths.to_vec(), fixture.helvetica.widths);
    assert_eq!(fbox, fixture.helvetica.fbox);
}

/// Mutation control: a one-character difference in the recorded structure, or
/// in the pixel data, is a mismatch (the comparison is not vacuous).
#[test]
fn mutated_expectations_are_rejected() {
    let fixture = fixture();
    if !tools() {
        return;
    }
    let case = fixture.import.iter().find(|c| c.id == "png-rgb-opaque").expect("case");
    let mut want = Want { ok: true, error_kind: String::new(), canon: case.want.canon.clone() };
    run_case(&case.id, "scan.png", &decode(&case.image), &case.ocr_text, &want, "").expect("unmutated case passes");
    let text = want.canon.as_ref().unwrap().to_string().replacen("DeviceRGB", "DeviceGRB", 1);
    want.canon = Some(serde_json::from_str(&text).unwrap());
    assert!(run_case(&case.id, "scan.png", &decode(&case.image), &case.ocr_text, &want, "").is_err());
    let stamp = fixture.stamp.iter().find(|c| c.id == "a4-hello").expect("case");
    let mut want = Want { ok: true, error_kind: String::new(), canon: stamp.want.canon.clone() };
    let text = want.canon.as_ref().unwrap().to_string().replacen("/Pagination", "/Paginatiom", 1);
    want.canon = Some(serde_json::from_str(&text).unwrap());
    assert!(run_case(&stamp.id, "input.pdf", &decode(&stamp.input_pdf), &stamp.ocr_text, &want, "").is_err());
    // Pixel control: a different case's Go PDF must be a pixel mismatch.
    let other = fixture.import.iter().find(|c| c.id == "png-gray8").expect("case");
    let honest = Want { ok: true, error_kind: String::new(), canon: case.want.canon.clone() };
    let err = run_case(&case.id, "scan.png", &decode(&case.image), &case.ocr_text, &honest, &other.go_pdf_b64).expect_err("pixel mismatch");
    assert!(err.contains("pixels differ"), "{err}");
    run_case(&case.id, "scan.png", &decode(&case.image), &case.ocr_text, &honest, &case.go_pdf_b64).expect("same-case Go PDF matches pixel for pixel");
}

/// The tolerance is exactly one unit of the fifth decimal on reals and nothing else.
#[test]
fn tolerance_is_narrow() {
    let v = |t: &str| -> Value { serde_json::from_str(t).unwrap() };
    assert!(close(&v("[158.63007]"), &v("[158.63008]")));
    assert!(!close(&v("[158.63007]"), &v("[158.6301]")));
    assert!(!close(&v("[2]"), &v("[3]")));
    assert!(close(&v("[2]"), &v("[2.00001]")));
    assert!(!close(&v("[2]"), &v("[2.0001]")));
    assert!(close(&v(r#"{"len":5,"text":"1 0 0 1.00001 cm"}"#), &v(r#"{"len":6,"text":"1 0 0 1.00002 cm"}"#)));
    assert!(!close(&v(r#"{"len":5,"text":"1 0 0 1.001 cm"}"#), &v(r#"{"len":5,"text":"1 0 0 1.00002 cm"}"#)));
    assert!(!close(&v(r#"{"len":5,"text":"1 0 0 1 cm"}"#), &v(r#"{"len":5,"text":"1 0 0 1 Cm"}"#)));
    assert!(!close(&v(r#"{"sha256":"aa"}"#), &v(r#"{"sha256":"ab"}"#)));
}
