//! Replays the Go-generated SymDraw DSL parser fixture
//! (`testdata/port/render/dsl-parse.json`, produced by
//! `scripts/rust-port/cmd/renderdslgen` from the production Go parser) through
//! the Rust port. Accepted inputs must serialize to the exact Go
//! `json.Marshal` bytes; rejected inputs must produce the identical typed
//! error (stage, message, detail, hint, line) and `Error()` string.

use serde_json::Value;
use symdesk_render::draw::{parse_json, parse_mermaid, parse_source};

const FIXTURE: &str = include_str!("../../../testdata/port/render/dsl-parse.json");

/// Exact shape of the Go-generated fixture (`make render-ir-fixtures-generate`).
const FIXTURE_CASES: usize = 3316;
const FIXTURE_ACCEPTED: usize = 811;
const FIXTURE_REJECTED: usize = 2505;

const STACK_1_MIB: usize = 1 << 20;

fn run(entry: &str, input: &str) -> Result<String, symdesk_render::draw::ParseError> {
    match entry {
        "source" => parse_source(input),
        "mermaid" => parse_mermaid(input),
        "json" => parse_json(input.as_bytes()),
        other => panic!("unknown entry {other}"),
    }
    .map(|diagram| diagram.to_json())
}

fn replay(fixture: &Value) -> (usize, usize, Vec<String>) {
    let mut accepted = 0;
    let mut rejected = 0;
    let mut failures = Vec::new();
    for case in fixture["cases"].as_array().expect("cases") {
        let id = case["id"].as_str().expect("id");
        let entry = case["entry"].as_str().expect("entry");
        let input = case["input"].as_str().expect("input");
        let got = run(entry, input);
        if case["accepted"] == true {
            accepted += 1;
            let want = case["diagram_json"].as_str().expect("diagram_json");
            match got {
                Ok(json) if json == want => {}
                Ok(json) => failures.push(format!(
                    "{id}: IR bytes differ\n  go:   {}\n  rust: {}",
                    clip(want),
                    clip(&json)
                )),
                Err(err) => failures.push(format!("{id}: Go accepted, Rust rejected: {err}")),
            }
        } else {
            rejected += 1;
            let want = &case["error"];
            match got {
                Ok(json) => failures.push(format!(
                    "{id}: Go rejected ({}), Rust accepted: {}",
                    want["text"],
                    clip(&json)
                )),
                Err(err) => {
                    let line = u64::try_from(err.line).expect("line");
                    let same = err.stage == want["stage"].as_str().unwrap()
                        && err.message == want["message"].as_str().unwrap()
                        && err.detail == want["detail"].as_str().unwrap()
                        && err.hint == want["hint"].as_str().unwrap()
                        && line == want["line"].as_u64().unwrap()
                        && err.to_string() == want["text"].as_str().unwrap();
                    if !same {
                        failures.push(format!(
                            "{id}: error differs\n  go:   {}\n  rust: stage={} message={:?} detail={:?} hint={:?} line={} text={:?}",
                            clip(&want.to_string()),
                            err.stage,
                            err.message,
                            err.detail,
                            err.hint,
                            err.line,
                            err.to_string()
                        ));
                    }
                }
            }
        }
    }
    (accepted, rejected, failures)
}

fn clip(s: &str) -> String {
    s.chars().take(400).collect()
}

#[test]
fn go_dsl_parser_fixture_replays_byte_exact() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    assert_eq!(fixture["schema_version"], 1);
    let fixture_len = fixture["cases"].as_array().expect("cases").len();
    // Custom values up to Go's depth limit are handled iteratively, so the
    // replay runs on a 1 MiB stack (the Windows main-thread size) in a debug
    // build, not on a stack sized to hide recursion.
    let (accepted, rejected, failures) = std::thread::Builder::new()
        .stack_size(STACK_1_MIB)
        .spawn(move || replay(&fixture))
        .expect("spawn")
        .join()
        .expect("replay thread");
    // A zero-case or one-sided run is not a pass: pin the regenerated
    // fixture's exact accepted/rejected/total counts.
    assert_eq!(accepted, FIXTURE_ACCEPTED, "accepted cases replayed");
    assert_eq!(rejected, FIXTURE_REJECTED, "rejected cases replayed");
    assert_eq!(accepted + rejected, FIXTURE_CASES, "total cases replayed");
    assert_eq!(
        fixture_len, FIXTURE_CASES,
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
fn fixture_covers_every_error_message_and_stage() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    let mut stages = std::collections::BTreeSet::new();
    let mut shapes = std::collections::BTreeSet::new();
    for case in fixture["cases"].as_array().unwrap() {
        if case["accepted"] == false {
            stages.insert(case["error"]["stage"].as_str().unwrap().to_owned());
            // Normalize quoted payloads so each distinct message template counts once.
            let message = case["error"]["message"].as_str().unwrap();
            let mut template = String::new();
            let mut in_quote = false;
            for c in message.chars() {
                if c == '"' {
                    in_quote = !in_quote;
                    template.push(c);
                } else if !in_quote {
                    template.push(c);
                }
            }
            shapes.insert(template);
        }
    }
    assert_eq!(
        stages.into_iter().collect::<Vec<_>>(),
        ["contract", "parse", "schema"]
    );
    for expected in [
        "empty diagram source",
        "empty JSON diagram input",
        "unknown field in diagram JSON",
        "malformed JSON diagram input",
        "unclosed quote string literal",
        "unexpected 'end' outside subgraph",
        "unclosed subgraph \"\"",
        "unsupported construct \"\"",
        "unsupported diagram kind \"\"",
        "unknown or unsupported diagram header \"\"",
        "unexpected token \"\"",
        "missing target node in edge definition",
        "missing node following '&' operator",
        "unclosed edge pipe label '|...|'",
        "unclosed shape delimiter \"\"",
        "invalid node identifier near \"\"",
        "sequence diagram has no participants",
        "diagram kind is required",
        "chart must have at least one series",
        "group must have at least one member",
    ] {
        assert!(
            shapes.contains(expected),
            "fixture lacks a case for message template {expected:?}"
        );
    }
}

/// Not a parity claim (there is no Go oracle for these prefixes): every prefix
/// and suffix of every fixture input must be rejected or accepted without a
/// panic, since a libFuzzer target for `draw_parser` is still open.
#[test]
fn parser_never_panics_on_input_prefixes_and_suffixes() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    let inputs: Vec<String> = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .map(|case| case["input"].as_str().unwrap().to_owned())
        .filter(|input| input.len() <= 600)
        .collect();
    assert!(inputs.len() > 2000, "only {} short inputs", inputs.len());
    let checked = std::thread::Builder::new()
        .stack_size(64 << 20)
        .spawn(move || {
            let mut checked = 0usize;
            for input in &inputs {
                for (idx, _) in input
                    .char_indices()
                    .chain(std::iter::once((input.len(), ' ')))
                {
                    for piece in [&input[..idx], &input[idx..]] {
                        let _ = parse_source(piece);
                        let _ = parse_mermaid(piece);
                        let _ = parse_json(piece.as_bytes());
                        checked += 1;
                    }
                }
            }
            checked
        })
        .expect("spawn")
        .join()
        .expect("parser panicked on a prefix/suffix");
    assert!(checked > 100_000, "only {checked} pieces checked");
}

/// Go accepts `custom` values nested up to depth 10 000. Parsing, JSON output,
/// clone/compare and dropping must not consume native stack per level: run the
/// deepest values the fixture accepts on a 1 MiB thread and compare the output
/// with the Go bytes.
#[test]
fn deepest_custom_values_round_trip_on_a_1_mib_stack() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("fixture JSON");
    for id in ["json-depth-ok", "json-depth-ok-mixed"] {
        let case = fixture["cases"]
            .as_array()
            .unwrap()
            .iter()
            .find(|case| case["id"] == id)
            .unwrap_or_else(|| panic!("fixture lacks {id}"));
        assert_eq!(case["accepted"], true, "{id}");
        let input = case["input"].as_str().unwrap().to_owned();
        let want = case["diagram_json"].as_str().unwrap().to_owned();
        let got = std::thread::Builder::new()
            .stack_size(STACK_1_MIB)
            .spawn(move || {
                let diagram = parse_json(input.as_bytes()).expect("depth within Go's limit");
                let json = diagram.to_json();
                let cloned = diagram.clone();
                assert!(cloned == diagram, "iterative clone/eq");
                drop(cloned);
                drop(diagram);
                json
            })
            .expect("spawn")
            .join()
            .unwrap_or_else(|_| panic!("{id}: overflowed or panicked on a 1 MiB stack"));
        assert!(got == want, "{id}: output differs from the Go bytes");
    }
}
