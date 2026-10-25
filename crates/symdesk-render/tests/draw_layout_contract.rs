//! Replays the Go-generated SymDraw layout fixture
//! (`testdata/port/render/layout.json`, produced by
//! `scripts/rust-port/cmd/renderlayoutgen` from the production Go
//! `internal/draw/layout` package) through the Rust port. Every result must
//! equal the exact Go `json.Marshal` bytes, or the exact Go error text.

use serde_json::Value;
use symdesk_render::draw::layout::{layout_graph, layout_sequence};
use symdesk_render::draw::measure::Measurer;
use symdesk_render::draw::{Diagram, decode_unvalidated, parse_source};

const FIXTURE: &str = include_str!("../../../testdata/port/render/layout.json");

/// Exact shape of the Go-generated fixture (`make render-ir-fixtures-generate`).
const FIXTURE_CASES: usize = 640;
const ENTRY_COUNTS: [(&str, usize); 2] = [("layered", 521), ("sequence", 119)];
/// Layout results (successes) and error texts per entry.
const OUTCOME_COUNTS: [(&str, usize, usize); 2] = [("layered", 443, 78), ("sequence", 28, 91)];
const CIRCLE_WITHHELD: u64 = 2;
const UNPARSABLE_WITHHELD: u64 = 1010;

fn load(case: &Value) -> Diagram {
    let input = case["input"].as_str().expect("input");
    match case["mode"].as_str().expect("mode") {
        "source" => parse_source(input).expect("source cases parse"),
        "ir" => decode_unvalidated(input.as_bytes()).expect("IR cases decode"),
        other => panic!("unknown mode {other}"),
    }
}

/// The exact Go JSON bytes plus the crossing count of a layered layout.
fn actual(m: &Measurer, case: &Value) -> Result<(String, Option<usize>), String> {
    let d = load(case);
    match case["entry"].as_str().expect("entry") {
        "layered" => {
            let result = layout_graph(m, &d)?;
            Ok((result.to_json()?, Some(result.crossings)))
        }
        "sequence" => Ok((layout_sequence(&d)?.to_json()?, None)),
        other => panic!("unknown entry {other}"),
    }
}

fn clip(s: &str) -> String {
    s.chars().take(400).collect()
}

fn replay(fixture: &Value) -> (Vec<(String, usize, usize)>, Vec<String>) {
    let m = Measurer::new().expect("embedded fonts");
    let mut counts: Vec<(String, usize, usize)> = Vec::new();
    let mut failures = Vec::new();
    for case in fixture["cases"].as_array().expect("cases") {
        let id = case["id"].as_str().expect("id");
        let entry = case["entry"].as_str().expect("entry");
        let want_error = case.get("error").and_then(Value::as_str);
        let slot = match counts.iter().position(|(e, _, _)| e == entry) {
            Some(i) => i,
            None => {
                counts.push((entry.to_owned(), 0, 0));
                counts.len() - 1
            }
        };
        let got = actual(&m, case);
        match (want_error, &got) {
            (Some(want), Err(got)) => {
                counts[slot].2 += 1;
                if got != want {
                    failures.push(format!(
                        "{id} ({entry}): error differs\n  go:   {want}\n  rust: {got}"
                    ));
                }
            }
            (None, Ok((got, crossings))) => {
                counts[slot].1 += 1;
                let want = case["expected"].as_str().expect("expected");
                let want_crossings = case.get("crossings").and_then(Value::as_u64);
                if want_crossings != crossings.map(|c| c as u64) {
                    failures.push(format!(
                        "{id} ({entry}): crossings differ: go {want_crossings:?}, rust {crossings:?}"
                    ));
                }
                if got != want {
                    failures.push(format!(
                        "{id} ({entry}): differs\n  go:   {}\n  rust: {}",
                        clip(want),
                        clip(got)
                    ));
                }
            }
            (Some(want), Ok((got, _))) => failures.push(format!(
                "{id} ({entry}): Go failed with {want:?}, Rust returned {}",
                clip(got)
            )),
            (None, Err(got)) => failures.push(format!(
                "{id} ({entry}): Go succeeded, Rust failed with {got:?}"
            )),
        }
    }
    (counts, failures)
}

#[test]
fn go_layout_fixture_replays_byte_exact() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    assert_eq!(fixture["schema_version"], 1);
    let entries: Vec<&str> = fixture["entries"]
        .as_array()
        .expect("entries")
        .iter()
        .map(|e| e.as_str().expect("entry name"))
        .collect();
    assert_eq!(entries, ["layered", "sequence"]);
    assert_eq!(
        fixture["withheld"]["circle_hypot_ambiguous_skipped"],
        CIRCLE_WITHHELD
    );
    assert_eq!(
        fixture["withheld"]["unparsable_sources_skipped"],
        UNPARSABLE_WITHHELD
    );
    assert_eq!(
        fixture["cases"].as_array().expect("cases").len(),
        FIXTURE_CASES,
        "the fixture must keep its exact case count"
    );

    let (counts, failures) = replay(&fixture);
    for (entry, want) in ENTRY_COUNTS {
        let got = counts
            .iter()
            .find(|(e, _, _)| e == entry)
            .map_or(0, |(_, ok, err)| ok + err);
        assert_eq!(got, want, "{entry} case count");
    }
    for (entry, ok, err) in OUTCOME_COUNTS {
        let got = counts
            .iter()
            .find(|(e, _, _)| e == entry)
            .map_or((0, 0), |(_, o, e)| (*o, *e));
        assert_eq!(got, (ok, err), "{entry} success/error split");
    }
    assert!(
        failures.is_empty(),
        "{} of {} cases differ from Go:\n{}",
        failures.len(),
        FIXTURE_CASES,
        failures
            .iter()
            .take(5)
            .cloned()
            .collect::<Vec<_>>()
            .join("\n")
    );
}

/// The fixture must exercise the paths that make the port non-trivial:
/// merged stable-sort blocks (more than 20 nodes in one layer), dummy nodes,
/// reversed edges, self loops, every direction and the engine's rejections.
#[test]
fn fixture_covers_the_hard_paths() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    let m = Measurer::new().expect("embedded fonts");
    let (mut wide, mut dummies, mut reversed, mut self_loops) = (0, 0, 0, 0);
    let mut directions = std::collections::BTreeSet::new();
    for case in fixture["cases"].as_array().expect("cases") {
        if case["entry"] != "layered" || case.get("error").is_some() {
            continue;
        }
        let d = load(case);
        directions.insert(d.direction.clone());
        let Ok(result) = layout_graph(&m, &d) else {
            continue;
        };
        if result.layers.iter().any(|l| l.len() > 20) {
            wide += 1;
        }
        if !result.dummies.is_empty() {
            dummies += 1;
        }
        if result.routes.iter().any(|r| r.reversed && !r.self_loop) {
            reversed += 1;
        }
        if result.routes.iter().any(|r| r.self_loop) {
            self_loops += 1;
        }
    }
    assert!(wide >= 10, "layers wider than Go's sort block: {wide}");
    assert!(dummies >= 100, "graphs with dummy nodes: {dummies}");
    assert!(reversed >= 50, "graphs with reversed edges: {reversed}");
    assert!(self_loops >= 20, "graphs with self loops: {self_loops}");
    assert_eq!(
        directions.len(),
        6,
        "every direction incl. empty: {directions:?}"
    );
}
