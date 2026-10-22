//! Replays the Go-generated SymDraw DSL parser fixture
//! (`testdata/port/render/dsl-parse.json`, produced by
//! `scripts/rust-port/cmd/renderdslgen` from the production Go parser) through
//! the Rust port. Accepted inputs must serialize to the exact Go
//! `json.Marshal` bytes; rejected inputs must produce the identical typed
//! error (stage, message, detail, hint, line) and `Error()` string.

use serde_json::Value;
use symdesk_render::draw::{parse_json, parse_mermaid, parse_source};

const FIXTURE: &str = include_str!("../../../testdata/port/render/dsl-parse.json");

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
    // Deeply nested custom values recurse; give the replay a generous stack.
    let (accepted, rejected, failures) = std::thread::Builder::new()
        .stack_size(256 << 20)
        .spawn(move || replay(&fixture))
        .expect("spawn")
        .join()
        .expect("replay thread");
    // A zero-case or one-sided run is not a pass.
    assert!(accepted >= 400, "only {accepted} accepted cases replayed");
    assert!(rejected >= 700, "only {rejected} rejected cases replayed");
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
