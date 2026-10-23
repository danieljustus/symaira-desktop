//! Replays the Go-generated SymDraw text measurement fixture
//! (`testdata/port/render/text-measure.json`, produced by
//! `scripts/rust-port/cmd/rendermeasuregen` from the production Go
//! `internal/draw/measure` package and its embedded Inter fonts) through the
//! Rust port. Every result must equal the exact Go `json.Marshal` bytes; the
//! sweep entry additionally checks the glyph advance of every Unicode scalar
//! value through SHA-256 digests.

use serde_json::Value;
use sha2::{Digest, Sha256};
use symdesk_render::draw::measure::{
    FontWeight, Measurer, NodeMeasureOptions, fonts_version_key, go_json_number as go_number,
    normalize_font_weight,
};

const FIXTURE: &str = include_str!("../../../testdata/port/render/text-measure.json");

/// Exact shape of the Go-generated fixture (`make render-ir-fixtures-generate`).
const FIXTURE_CASES: usize = 5847;
const ENTRY_COUNTS: [(&str, usize); 6] = [
    ("measure_text", 1081),
    ("measure_text_detailed", 862),
    ("break_lines", 1534),
    ("measure_node", 1257),
    ("normalize_weight", 27),
    ("advance_sweep", 1086),
];

fn num(case: &Value, key: &str) -> f64 {
    case.get(key).and_then(Value::as_f64).unwrap_or(0.0)
}

fn text(case: &Value) -> &str {
    case.get("text").and_then(Value::as_str).unwrap_or("")
}

fn weight(case: &Value) -> FontWeight {
    FontWeight::from_go(case.get("weight").and_then(Value::as_str).unwrap_or(""))
}

fn options(case: &Value) -> NodeMeasureOptions {
    let o = &case["options"];
    NodeMeasureOptions {
        font_size: num(o, "font_size"),
        font_weight: FontWeight::from_go(o["font_weight"].as_str().expect("font_weight")),
        padding_x: num(o, "padding_x"),
        padding_y: num(o, "padding_y"),
        min_width: num(o, "min_width"),
        min_height: num(o, "min_height"),
        max_width: num(o, "max_width"),
        line_spacing: num(o, "line_spacing"),
    }
}

fn chunk_text(start: u32, count: u32) -> String {
    (start..start + count)
        .map(|r| char::from_u32(r).expect("sweep chunks contain only scalar values"))
        .collect()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn actual(m: &Measurer, case: &Value) -> String {
    let entry = case["entry"].as_str().expect("entry");
    match entry {
        "measure_text" => {
            let (w, h) = m.measure_text(text(case), num(case, "size"), weight(case));
            format!("[{},{}]", go_number(w), go_number(h))
        }
        "measure_text_detailed" => m
            .measure_text_detailed(text(case), num(case, "size"), weight(case))
            .expect("metrics")
            .to_json(),
        "break_lines" => m
            .break_lines(
                text(case),
                num(case, "max_width"),
                num(case, "size"),
                weight(case),
            )
            .to_json(),
        "measure_node" => m
            .measure_node(
                text(case),
                case.get("shape").and_then(Value::as_str).unwrap_or(""),
                options(case),
            )
            .to_json(),
        "normalize_weight" => format!("\"{}\"", normalize_font_weight(text(case)).as_go()),
        "advance_sweep" => {
            let start = u32::try_from(case["start"].as_u64().unwrap_or(0)).expect("start");
            let count = u32::try_from(case["count"].as_u64().expect("count")).expect("count");
            let metrics = m
                .measure_text_detailed(&chunk_text(start, count), num(case, "size"), weight(case))
                .expect("metrics");
            // The digest is over the JSON array of advances, as Go marshals it.
            let advances = metrics.advances_json();
            format!(
                "{{\"advances_sha256\":\"{}\",\"width\":{}}}",
                hex(&Sha256::digest(advances.as_bytes())),
                go_number(metrics.width)
            )
        }
        other => panic!("unknown entry {other}"),
    }
}

fn clip(s: &str) -> String {
    s.chars().take(300).collect()
}

fn replay(fixture: &Value) -> (Vec<(String, usize)>, Vec<String>) {
    let m = Measurer::new().expect("embedded fonts");
    let mut counts: Vec<(String, usize)> = Vec::new();
    let mut failures = Vec::new();
    for case in fixture["cases"].as_array().expect("cases") {
        let id = case["id"].as_str().expect("id");
        let entry = case["entry"].as_str().expect("entry");
        match counts.iter_mut().find(|(e, _)| e == entry) {
            Some((_, n)) => *n += 1,
            None => counts.push((entry.to_owned(), 1)),
        }
        let want = case["expected"].as_str().expect("expected");
        let got = actual(&m, case);
        if got != want {
            failures.push(format!(
                "{id} ({entry}): differs\n  go:   {}\n  rust: {}",
                clip(want),
                clip(&got)
            ));
        }
    }
    (counts, failures)
}

#[test]
fn go_text_measure_fixture_replays_byte_exact() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    assert_eq!(fixture["schema_version"], 1);
    let (counts, failures) = replay(&fixture);
    // A zero-case or partial run is not a pass: pin every entry's count.
    for (entry, want) in ENTRY_COUNTS {
        let got = counts
            .iter()
            .find(|(e, _)| e == entry)
            .map_or(0, |(_, n)| *n);
        assert_eq!(got, want, "cases replayed for {entry}");
    }
    let total: usize = counts.iter().map(|(_, n)| n).sum();
    assert_eq!(total, FIXTURE_CASES, "total cases replayed");
    assert_eq!(
        fixture["cases"].as_array().expect("cases").len(),
        FIXTURE_CASES,
        "fixture case count changed; regenerate and update the pinned counts"
    );
    assert!(
        failures.is_empty(),
        "{} Go/Rust divergences, first 12:\n{}",
        failures.len(),
        failures
            .iter()
            .take(12)
            .cloned()
            .collect::<Vec<_>>()
            .join("\n")
    );
}

#[test]
fn embedded_font_identity_matches_go() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    let m = Measurer::new().expect("embedded fonts");
    assert_eq!(
        m.font_hash(),
        fixture["fonts"]["measurer_hash"]
            .as_str()
            .expect("measurer_hash")
    );
    assert_eq!(
        fonts_version_key(),
        fixture["fonts"]["version_key"]
            .as_str()
            .expect("version_key")
    );
}

#[test]
fn fixture_covers_the_whole_unicode_scalar_range() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    let mut covered = 0u64;
    for case in fixture["cases"].as_array().expect("cases") {
        if case["entry"] == "advance_sweep" && case["weight"] == "regular" {
            covered += case["count"].as_u64().expect("count");
        }
    }
    // 0x110000 code points minus the 2048 surrogates that are not scalar values.
    assert_eq!(covered, 0x11_0000 - 0x800);
}
