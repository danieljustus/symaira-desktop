//! Replays `testdata/port/render/facade.json` (recorded from the production
//! Go `draw.Render` and the other `internal/draw/draw.go` entry points)
//! against the Rust facade: every render must produce the identical result
//! (format, size, font hash, byte length and SHA-256) or the identical error.

use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use symdesk_render::draw::{
    Diagram,
    builder::build_scene,
    decode_unvalidated,
    facade::{
        RenderRequest, dialect_version, font_hash, kinds, new_scene, parse, parse_json_diagram,
        parse_mermaid_source, render, render_with_context, themes, validate, validate_json,
    },
    parse_source,
};

const FIXTURE: &str = include_str!("../../../testdata/port/render/facade.json");
const EMIT: &str = include_str!("../../../testdata/port/render/emit.json");

fn sha(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}

fn decode(mode: &str, input: &str) -> Diagram {
    if mode == "ir" {
        decode_unvalidated(input.as_bytes()).unwrap()
    } else {
        parse_source(input).unwrap()
    }
}

fn replay_render(
    srcs: &HashMap<String, (String, String)>,
    c: &serde_json::Value,
    ok: &AtomicUsize,
    errs: &AtomicUsize,
) {
    let id = c["id"].as_str().unwrap();
    let (mode, input) = &srcs[c["ref"].as_str().unwrap()];
    assert_eq!(
        sha(input.as_bytes()),
        c["input_sha256"].as_str().unwrap(),
        "{id}"
    );
    let r = &c["request"];
    let via = r["via"].as_str().unwrap();
    let mut req = RenderRequest {
        format: r["format"].as_str().unwrap().to_owned(),
        theme: r["theme"].as_str().unwrap().to_owned(),
        scale: r["scale"].as_f64().unwrap(),
        text_as_paths: r["text_as_paths"].as_bool().unwrap(),
        padding: r["padding"].as_f64().unwrap(),
        ..Default::default()
    };
    let mut canceled = false;
    match via {
        "source" => req.source = input.clone(),
        "ir" => req.ir = Some(decode(mode, input)),
        "scene" | "scene+source" => {
            req.scene = Some(build_scene(&decode(mode, input)).unwrap());
            if via == "scene+source" {
                req.source = "{ not a diagram".to_owned();
            }
        }
        "none" => {}
        "canceled" => {
            req.source = input.clone();
            canceled = true;
        }
        other => panic!("unknown via {other}"),
    }
    let want = &c["result"];
    match render_with_context(req, canceled) {
        Err(err) => {
            assert_eq!(err, want["error"].as_str().unwrap(), "{id}");
            assert_eq!(want["sha256"], "", "{id}");
            errs.fetch_add(1, Ordering::Relaxed);
        }
        Ok(res) => {
            assert_eq!(want["error"], "", "{id}");
            assert_eq!(res.format, want["format"].as_str().unwrap(), "{id} format");
            assert_eq!(
                format!("{:016x}", res.width.to_bits()),
                want["width_bits"].as_str().unwrap(),
                "{id} width"
            );
            assert_eq!(
                format!("{:016x}", res.height.to_bits()),
                want["height_bits"].as_str().unwrap(),
                "{id} height"
            );
            assert_eq!(res.font_hash, want["font_hash"].as_str().unwrap(), "{id}");
            assert_eq!(
                res.data.len() as u64,
                want["length"].as_u64().unwrap(),
                "{id} length"
            );
            assert_eq!(sha(&res.data), want["sha256"].as_str().unwrap(), "{id}");
            ok.fetch_add(1, Ordering::Relaxed);
        }
    }
}

fn replay_call(c: &serde_json::Value) {
    let id = c["id"].as_str().unwrap();
    let arg = c["arg"].as_str().unwrap_or_default();
    let want_out = c["output"].as_str().unwrap_or_default();
    let want_err = c["error"].as_str().unwrap_or_default();
    let (out, err): (String, String) = match c["op"].as_str().unwrap() {
        "kinds" => (serde_json::to_string(&kinds()).unwrap(), String::new()),
        "themes" => (serde_json::to_string(&themes()).unwrap(), String::new()),
        "font_hash" => (font_hash(), String::new()),
        "dialect_version" => (dialect_version().to_owned(), String::new()),
        "validate_nil" => (String::new(), validate(None).unwrap_err()),
        "validate_json" => (
            String::new(),
            validate_json(arg.as_bytes())
                .err()
                .map(|e| e.to_string())
                .unwrap_or_default(),
        ),
        "parse_json" => (
            String::new(),
            parse_json_diagram(arg.as_bytes())
                .err()
                .map(|e| e.to_string())
                .unwrap_or_default(),
        ),
        "parse" => (
            String::new(),
            parse(arg).err().map(|e| e.to_string()).unwrap_or_default(),
        ),
        "parse_mermaid" => (
            String::new(),
            parse_mermaid_source(arg)
                .err()
                .map(|e| e.to_string())
                .unwrap_or_default(),
        ),
        "new_scene" => (
            new_scene(321.5, 123.0, arg).to_json().unwrap(),
            String::new(),
        ),
        other => panic!("unknown op {other}"),
    };
    assert_eq!(out, want_out, "{id}");
    assert_eq!(err, want_err, "{id}");
}

#[test]
fn go_facade_contract() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(fixture["schema_version"], 1);
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 3664);
    let emit: serde_json::Value = serde_json::from_str(EMIT).unwrap();
    let srcs: HashMap<String, (String, String)> = emit["cases"]
        .as_array()
        .unwrap()
        .iter()
        .map(|c| {
            (
                c["id"].as_str().unwrap().to_owned(),
                (
                    c["mode"].as_str().unwrap().to_owned(),
                    c["input"].as_str().unwrap().to_owned(),
                ),
            )
        })
        .collect();
    let next = AtomicUsize::new(0);
    let (ok, errs, calls) = (
        AtomicUsize::new(0),
        AtomicUsize::new(0),
        AtomicUsize::new(0),
    );
    let workers = std::thread::available_parallelism().map_or(2, |n| n.get());
    std::thread::scope(|scope| {
        for _ in 0..workers {
            scope.spawn(|| {
                loop {
                    let i = next.fetch_add(1, Ordering::Relaxed);
                    let Some(c) = cases.get(i) else { break };
                    if c["kind"] == "call" {
                        replay_call(c);
                        calls.fetch_add(1, Ordering::Relaxed);
                    } else {
                        replay_render(&srcs, c, &ok, &errs);
                    }
                }
            });
        }
    });
    assert_eq!(
        (
            ok.load(Ordering::Relaxed),
            errs.load(Ordering::Relaxed),
            calls.load(Ordering::Relaxed)
        ),
        (3409, 206, 49)
    );
}

/// Mutation control: changing any request field changes the recorded result.
#[test]
fn facade_hash_detects_request_changes() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    let emit: serde_json::Value = serde_json::from_str(EMIT).unwrap();
    let case = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| {
            c["kind"] == "render" && c["request"]["format"] == "svg" && c["result"]["error"] == ""
        })
        .unwrap();
    let id = case["ref"].as_str().unwrap();
    let input = emit["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["id"] == id)
        .map(|c| c["input"].as_str().unwrap().to_owned())
        .unwrap();
    let r = &case["request"];
    let req = || RenderRequest {
        source: if r["via"] == "source" {
            input.clone()
        } else {
            String::new()
        },
        ir: (r["via"] == "ir").then(|| decode("ir", &input)),
        format: "svg".to_owned(),
        theme: r["theme"].as_str().unwrap().to_owned(),
        scale: r["scale"].as_f64().unwrap(),
        text_as_paths: r["text_as_paths"].as_bool().unwrap(),
        padding: r["padding"].as_f64().unwrap(),
        ..Default::default()
    };
    let want = case["result"]["sha256"].as_str().unwrap();
    assert_eq!(sha(&render(req()).unwrap().data), want, "control");
    let mut other = req();
    other.padding += 3.0;
    assert_ne!(sha(&render(other).unwrap().data), want, "padding");
    let mut other = req();
    other.format = "typst".to_owned();
    assert_ne!(sha(&render(other).unwrap().data), want, "format");
}
