//! Replays `testdata/port/render/typst.json` (recorded from the production
//! Go `emit.EmitTypst`) against the Rust port: every case must produce the
//! identical Typst text (SHA-256 and length) or the identical error.

use sha2::{Digest, Sha256};
use std::collections::HashMap;
use symdesk_render::draw::{
    builder::build_scene,
    decode_unvalidated, parse_source,
    scene::{
        CircleElement, EllipseElement, GroupElement, LineElement, Primitive, RectElement, Scene,
        TextElement,
    },
    theme,
    typst::emit_typst,
};

const FIXTURE: &str = include_str!("../../../testdata/port/render/typst.json");
const EMIT: &str = include_str!("../../../testdata/port/render/emit.json");

fn sha(s: &str) -> String {
    format!("{:x}", Sha256::digest(s.as_bytes()))
}

/// A JSON number, or the strings "NaN", "+Inf", "-Inf".
fn num(v: &serde_json::Value, key: &str) -> f64 {
    match &v[key] {
        serde_json::Value::Null => 0.0,
        serde_json::Value::String(s) => match s.as_str() {
            "NaN" => f64::NAN,
            "+Inf" => f64::INFINITY,
            "-Inf" => f64::NEG_INFINITY,
            other => panic!("bad number {other}"),
        },
        n => n.as_f64().unwrap(),
    }
}

fn string(v: &serde_json::Value, key: &str) -> String {
    v[key].as_str().unwrap_or_default().to_owned()
}

fn build_spec(spec: &serde_json::Value) -> Scene {
    let mut sc = Scene::new(
        num(spec, "width"),
        num(spec, "height"),
        &theme::resolve("symaira-dark"),
    );
    sc.background = string(spec, "background");
    for p in spec["prims"].as_array().unwrap() {
        sc.add(match p["t"].as_str().unwrap() {
            "rect" => Primitive::Rect(RectElement {
                x: num(p, "x"),
                y: num(p, "y"),
                width: num(p, "w"),
                height: num(p, "h"),
                rx: num(p, "rx"),
                fill: string(p, "fill"),
                stroke: string(p, "stroke"),
                stroke_width: num(p, "sw"),
                ..Default::default()
            }),
            "circle" => Primitive::Circle(CircleElement {
                cx: num(p, "cx"),
                cy: num(p, "cy"),
                r: num(p, "r"),
                fill: string(p, "fill"),
                stroke: string(p, "stroke"),
                stroke_width: num(p, "sw"),
                ..Default::default()
            }),
            "text" => Primitive::Text(TextElement {
                x: num(p, "x"),
                y: num(p, "y"),
                text: string(p, "text"),
                font_size: num(p, "fs"),
                fill: string(p, "fill"),
                ..Default::default()
            }),
            "line" => Primitive::Line(LineElement {
                x1: num(p, "x"),
                y1: num(p, "y"),
                x2: num(p, "w"),
                y2: num(p, "h"),
                stroke: string(p, "stroke"),
                stroke_width: num(p, "sw"),
                ..Default::default()
            }),
            "ellipse" => Primitive::Ellipse(EllipseElement {
                cx: num(p, "cx"),
                cy: num(p, "cy"),
                rx: num(p, "rx"),
                ry: num(p, "r"),
                fill: string(p, "fill"),
                ..Default::default()
            }),
            "group" => Primitive::Group(GroupElement {
                id: "g".to_owned(),
                children: Some(vec![Primitive::Rect(RectElement {
                    x: 1.0,
                    y: 2.0,
                    width: 3.0,
                    height: 4.0,
                    fill: "#ffffff".to_owned(),
                    ..Default::default()
                })]),
                ..Default::default()
            }),
            other => panic!("unknown primitive {other}"),
        });
    }
    sc
}

fn emit_sources() -> HashMap<String, (String, String)> {
    let emit: serde_json::Value = serde_json::from_str(EMIT).unwrap();
    emit["cases"]
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
        .collect()
}

fn scene_for(srcs: &HashMap<String, (String, String)>, c: &serde_json::Value) -> Option<Scene> {
    let (mode, input) = &srcs[c["id"].as_str().unwrap()];
    assert_eq!(&sha(input), c["input"].as_str().unwrap(), "input drifted");
    let diagram = if mode == "ir" {
        decode_unvalidated(input.as_bytes()).unwrap()
    } else {
        parse_source(input).unwrap()
    };
    Some(build_scene(&diagram).unwrap())
}

#[test]
fn go_typst_contract() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(fixture["schema_version"], 1);
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 1312);
    let srcs = emit_sources();
    let (mut ok, mut errors) = (0, 0);
    for c in cases {
        let id = c["id"].as_str().unwrap();
        let sc = match c["mode"].as_str().unwrap() {
            "nil" => None,
            "spec" => Some(build_spec(
                &serde_json::from_str(c["input"].as_str().unwrap()).unwrap(),
            )),
            _ => scene_for(&srcs, c),
        };
        match emit_typst(sc.as_ref()) {
            Err(err) => {
                assert_eq!(err, c["error"].as_str().unwrap(), "{id}");
                assert_eq!(c["sha256"], "", "{id}");
                errors += 1;
            }
            Ok(out) => {
                assert_eq!(c["error"], "", "{id}");
                assert_eq!(
                    out.len() as u64,
                    c["length"].as_u64().unwrap(),
                    "{id} length"
                );
                assert_eq!(sha(&out), c["sha256"].as_str().unwrap(), "{id}");
                let kept = c["output"].as_str().unwrap();
                if !kept.is_empty() {
                    assert_eq!(out, kept, "{id} verbatim");
                }
                ok += 1;
            }
        }
    }
    assert_eq!((ok, errors), (1311, 1));
}

/// Mutation control: a one-byte change in the Typst text must leave the
/// recorded hash.
#[test]
fn typst_hash_detects_one_byte_change() {
    let fixture: serde_json::Value = serde_json::from_str(FIXTURE).unwrap();
    let case = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["id"] == "synthetic-rect-fill-stroke")
        .unwrap();
    let sc = build_spec(&serde_json::from_str(case["input"].as_str().unwrap()).unwrap());
    let out = emit_typst(Some(&sc)).unwrap();
    assert_eq!(sha(&out), case["sha256"].as_str().unwrap(), "control");
    let mutated = out.replacen("10.00pt", "10.01pt", 1);
    assert_ne!(sha(&mutated), case["sha256"].as_str().unwrap());
    let mut moved = sc.clone();
    if let Primitive::Rect(r) = &mut moved.primitives[0] {
        r.rx += 0.01;
    }
    assert_ne!(
        sha(&emit_typst(Some(&moved)).unwrap()),
        case["sha256"].as_str().unwrap()
    );
}
