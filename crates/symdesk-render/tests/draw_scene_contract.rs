//! Replays the Go-generated SymDraw scene/theme fixture
//! (`testdata/port/render/scene-bounds.json`, produced by
//! `scripts/rust-port/cmd/renderscenegen` from the production Go
//! `internal/draw/scene` and `internal/draw/theme` packages) through the Rust
//! port. Every result must equal the exact Go text: `json.Marshal` bytes,
//! the error text of a failed marshal, `Scene.Validate` messages and the
//! theme registry state.

use std::cell::RefCell;
use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};
use symdesk_render::draw::scene::{
    CircleElement, EllipseElement, GroupElement, JsonResult, LineElement, MarkerDef, PathElement,
    PathSegment, Point, PolygonElement, PolylineElement, Primitive, Rect, RectElement, Scene,
    TextElement, default_markers, go_fmt_v, go_max, go_min, markers_json,
};
use symdesk_render::draw::theme::{self, Theme, ThemeRegistry};

const FIXTURE: &str = include_str!("../../../testdata/port/render/scene-bounds.json");

/// Exact shape of the Go-generated fixture (`make render-ir-fixtures-generate`).
const FIXTURE_CASES: usize = 3422;
const FMA_AMBIGUOUS_SKIPPED: u64 = 147;
const ENTRY_COUNTS: [(&str, usize); 6] = [
    ("theme_resolve", 39),
    ("rect_op", 529),
    ("primitive", 2465),
    ("scene", 362),
    ("default_markers", 13),
    ("theme_registry", 14),
];

// ------------------------------------------------------------------ decoding

/// A spec object that remembers which keys the decoder consumed, so a field
/// the Go oracle honored but this decoder ignores fails loudly.
struct Spec<'a> {
    map: &'a Map<String, Value>,
    used: RefCell<BTreeSet<String>>,
}

impl<'a> Spec<'a> {
    fn new(value: &'a Value) -> Spec<'a> {
        Spec {
            map: value.as_object().expect("spec object"),
            used: RefCell::new(BTreeSet::new()),
        }
    }

    fn get(&self, key: &str) -> Option<&'a Value> {
        let value = self.map.get(key);
        if value.is_some() {
            self.used.borrow_mut().insert(key.to_owned());
        }
        value
    }

    fn num(&self, key: &str) -> f64 {
        self.get(key).map_or(0.0, number)
    }

    fn str_(&self, key: &str) -> String {
        self.get(key)
            .map_or_else(String::new, |v| v.as_str().expect("string").to_owned())
    }

    fn finish(&self, what: &str) {
        let used = self.used.borrow();
        for key in self.map.keys() {
            assert!(
                used.contains(key) || key == "type",
                "{what}: spec key {key:?} was not decoded"
            );
        }
    }
}

fn number(value: &Value) -> f64 {
    match value {
        Value::Number(n) => n.as_f64().expect("finite JSON number"),
        Value::String(s) => match s.as_str() {
            "NaN" => f64::NAN,
            "+Inf" => f64::INFINITY,
            "-Inf" => f64::NEG_INFINITY,
            "-0" => -0.0,
            other => panic!("unsupported spelled number {other:?}"),
        },
        other => panic!("not a number: {other}"),
    }
}

fn rect(value: &Value) -> Rect {
    let s = Spec::new(value);
    let r = Rect {
        x: s.num("x"),
        y: s.num("y"),
        width: s.num("width"),
        height: s.num("height"),
    };
    s.finish("rect");
    r
}

fn points(value: &Value) -> Option<Vec<Point>> {
    value.as_array().map(|list| {
        list.iter()
            .map(|item| {
                let s = Spec::new(item);
                let p = Point {
                    x: s.num("x"),
                    y: s.num("y"),
                };
                s.finish("point");
                p
            })
            .collect()
    })
}

fn segments(value: &Value) -> Vec<PathSegment> {
    value.as_array().map_or_else(Vec::new, |list| {
        list.iter()
            .map(|item| {
                let s = Spec::new(item);
                let segment = PathSegment {
                    op: s.str_("op"),
                    args: s.get("args").and_then(points).unwrap_or_default(),
                };
                s.finish("segment");
                segment
            })
            .collect()
    })
}

fn primitive(value: &Value) -> Primitive {
    let s = Spec::new(value);
    let kind = value["type"].as_str().expect("primitive type").to_owned();
    let out = match kind.as_str() {
        "rect" => Primitive::Rect(RectElement {
            id: s.str_("id"),
            class: s.str_("class"),
            x: s.num("x"),
            y: s.num("y"),
            width: s.num("width"),
            height: s.num("height"),
            rx: s.num("rx"),
            ry: s.num("ry"),
            fill: s.str_("fill"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            dash_array: s.str_("dash_array"),
            opacity: s.num("opacity"),
            link: s.str_("link"),
        }),
        "circle" => Primitive::Circle(CircleElement {
            id: s.str_("id"),
            class: s.str_("class"),
            cx: s.num("cx"),
            cy: s.num("cy"),
            r: s.num("r"),
            fill: s.str_("fill"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            opacity: s.num("opacity"),
            link: s.str_("link"),
        }),
        "ellipse" => Primitive::Ellipse(EllipseElement {
            id: s.str_("id"),
            class: s.str_("class"),
            cx: s.num("cx"),
            cy: s.num("cy"),
            rx: s.num("rx"),
            ry: s.num("ry"),
            fill: s.str_("fill"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            opacity: s.num("opacity"),
            link: s.str_("link"),
        }),
        "line" => Primitive::Line(LineElement {
            id: s.str_("id"),
            class: s.str_("class"),
            x1: s.num("x1"),
            y1: s.num("y1"),
            x2: s.num("x2"),
            y2: s.num("y2"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            dash_array: s.str_("dash_array"),
            marker_start: s.str_("marker_start"),
            marker_end: s.str_("marker_end"),
            opacity: s.num("opacity"),
        }),
        "polyline" => Primitive::Polyline(PolylineElement {
            id: s.str_("id"),
            class: s.str_("class"),
            points: s.get("points").and_then(points),
            fill: s.str_("fill"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            dash_array: s.str_("dash_array"),
            marker_end: s.str_("marker_end"),
            opacity: s.num("opacity"),
        }),
        "polygon" => Primitive::Polygon(PolygonElement {
            id: s.str_("id"),
            class: s.str_("class"),
            points: s.get("points").and_then(points),
            fill: s.str_("fill"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            dash_array: s.str_("dash_array"),
            opacity: s.num("opacity"),
        }),
        "path" => Primitive::Path(PathElement {
            id: s.str_("id"),
            class: s.str_("class"),
            d: s.str_("d"),
            segments: s.get("segments").map_or_else(Vec::new, segments),
            fill: s.str_("fill"),
            stroke: s.str_("stroke"),
            stroke_width: s.num("stroke_width"),
            dash_array: s.str_("dash_array"),
            marker_end: s.str_("marker_end"),
            opacity: s.num("opacity"),
            link: s.str_("link"),
            bounds_hint: s.get("bounds_hint").map(rect),
        }),
        "text" => Primitive::Text(TextElement {
            id: s.str_("id"),
            class: s.str_("class"),
            x: s.num("x"),
            y: s.num("y"),
            text: s.str_("text"),
            font_size: s.num("font_size"),
            font_family: s.str_("font_family"),
            font_weight: s.str_("font_weight"),
            fill: s.str_("fill"),
            anchor: s.str_("anchor"),
            baseline: s.str_("baseline"),
            text_length: s.num("text_length"),
            opacity: s.num("opacity"),
            link: s.str_("link"),
        }),
        "group" => Primitive::Group(GroupElement {
            id: s.str_("id"),
            class: s.str_("class"),
            transform: s.str_("transform"),
            children: s
                .get("children")
                .and_then(Value::as_array)
                .map(|list| list.iter().map(primitive).collect()),
            link: s.str_("link"),
        }),
        other => panic!("unknown primitive type {other:?}"),
    };
    s.finish(&kind);
    out
}

fn theme_from_spec(value: &Value) -> Theme {
    let s = Spec::new(value);
    if let Some(name) = s.get("resolve") {
        s.finish("theme resolve");
        return theme::resolve(name.as_str().expect("resolve name"));
    }
    let palette = s.get("palette").and_then(Value::as_array).map(|list| {
        list.iter()
            .map(|c| c.as_str().expect("palette entry").to_owned())
            .collect()
    });
    let th = Theme {
        name: s.str_("name"),
        is_dark: s.get("is_dark").is_some_and(|v| v.as_bool() == Some(true)),
        background: s.str_("background"),
        surface: s.str_("surface"),
        surface_subtle: s.str_("surface_subtle"),
        border: s.str_("border"),
        border_subtle: s.str_("border_subtle"),
        text: s.str_("text"),
        text_muted: s.str_("text_muted"),
        primary: s.str_("primary"),
        secondary: s.str_("secondary"),
        accent: s.str_("accent"),
        edge: s.str_("edge"),
        group_background: s.str_("group_background"),
        group_border: s.str_("group_border"),
        grid: s.str_("grid"),
        palette,
        font_family: s.str_("font_family"),
    };
    s.finish("theme");
    th
}

fn marker(value: &Value) -> MarkerDef {
    let s = Spec::new(value);
    let def = MarkerDef {
        id: s.str_("id"),
        view_box: s.get("view_box").map(rect).unwrap_or_default(),
        ref_x: s.num("ref_x"),
        ref_y: s.num("ref_y"),
        marker_width: s.num("marker_width"),
        marker_height: s.num("marker_height"),
        orient: s.str_("orient"),
        path: s.get("path").filter(|v| !v.is_null()).map(primitive),
    };
    s.finish("marker");
    def
}

// ----------------------------------------------------------------- execution

/// Go `goJSON`: marshal bytes, or `!` plus the marshal error.
fn gj(result: JsonResult) -> String {
    result.unwrap_or_else(|err| format!("!{err}"))
}

fn gj_float(value: f64) -> String {
    if value.is_finite() {
        let mut out = String::new();
        out.push_str(&symdesk_render::draw::measure::go_json_number(value));
        out
    } else {
        gj(Err(if value.is_nan() {
            "json: unsupported value: NaN".to_owned()
        } else if value > 0.0 {
            "json: unsupported value: +Inf".to_owned()
        } else {
            "json: unsupported value: -Inf".to_owned()
        }))
    }
}

fn run_rect_op(input: &Value) -> String {
    let s = Spec::new(input);
    let a = rect(s.get("a").expect("a"));
    let b = rect(s.get("b").expect("b"));
    let padding = number(s.get("padding").expect("padding"));
    s.finish("rect_op");
    [
        format!("max_x={}", gj_float(a.max_x())),
        format!("max_y={}", gj_float(a.max_y())),
        format!("is_empty={}", a.is_empty()),
        format!("union={}", gj(a.union(b).to_json())),
        format!("contains={}", a.contains(b)),
        format!("pad={}", gj(a.pad(padding).to_json())),
    ]
    .join("\n")
}

fn run_primitive(input: &Value) -> String {
    let s = Spec::new(input);
    let prim = primitive(s.get("primitive").expect("primitive"));
    s.finish("primitive case");
    [
        format!("type={}", prim.type_name()),
        format!("id={}", prim.element_id()),
        format!("bounds={}", gj(prim.bounds().to_json())),
        format!("json={}", gj(prim.to_json())),
    ]
    .join("\n")
}

fn validate_text(scene: &Scene) -> String {
    scene.validate().err().unwrap_or_else(|| "ok".to_owned())
}

fn run_scene(input: &Value) -> String {
    let s = Spec::new(input);
    let th = s.get("theme").map(theme_from_spec).unwrap_or_default();
    let mut scene = Scene::new(number(&input["width"]), number(&input["height"]), &th);
    s.get("width");
    s.get("height");
    if let Some(v) = s.get("view_box") {
        scene.view_box = rect(v);
    }
    if let Some(v) = s.get("background") {
        scene.background = v.as_str().expect("background").to_owned();
    }
    if let Some(v) = s.get("metadata") {
        scene.metadata = v
            .as_object()
            .expect("metadata")
            .iter()
            .map(|(k, v)| (k.clone(), v.as_str().expect("metadata value").to_owned()))
            .collect::<BTreeMap<_, _>>();
    }
    if s.get("no_markers")
        .is_some_and(|v| v.as_bool() == Some(true))
    {
        scene.markers.clear();
    }
    if let Some(v) = s.get("extra_markers") {
        for item in v.as_array().expect("extra_markers") {
            scene.add_marker(marker(item));
        }
    }
    if let Some(v) = s.get("primitives") {
        for item in v.as_array().expect("primitives") {
            // A JSON null is a nil Go primitive, which Scene.Add skips.
            if !item.is_null() {
                scene.add(primitive(item));
            }
        }
    }
    let fit = s.get("fit_padding").map(number);
    s.finish("scene");

    let mut lines = vec![
        format!("bounds={}", gj(scene.compute_bounds().to_json())),
        format!("validate={}", validate_text(&scene)),
        format!("json={}", gj(scene.to_json())),
    ];
    if let Some(padding) = fit {
        scene.fit_to_bounds(padding);
        lines.push(format!("fit_view_box={}", gj(scene.view_box.to_json())));
        lines.push(format!("fit_width={}", gj_float(scene.width)));
        lines.push(format!("fit_height={}", gj_float(scene.height)));
        lines.push(format!(
            "bounds_after_fit={}",
            gj(scene.compute_bounds().to_json())
        ));
        lines.push(format!("validate_after_fit={}", validate_text(&scene)));
    }
    lines.join("\n")
}

fn run_markers(input: &Value) -> String {
    let s = Spec::new(input);
    let th = theme_from_spec(s.get("theme").expect("theme"));
    s.finish("default_markers");
    gj(markers_json(&default_markers(&th)))
}

fn names_json(names: &[String]) -> String {
    let mut out = String::from("[");
    for (index, name) in names.iter().enumerate() {
        if index > 0 {
            out.push(',');
        }
        out.push('"');
        out.push_str(name);
        out.push('"');
    }
    out.push(']');
    out
}

fn run_registry(registry: &mut ThemeRegistry, input: &Value) -> String {
    let s = Spec::new(input);
    let mut lines = Vec::new();
    for op in s.get("ops").and_then(Value::as_array).expect("ops") {
        let o = Spec::new(op);
        match o.str_("op").as_str() {
            "register" => {
                // Registered themes are inline specs, never `resolve` specs.
                registry.register(theme_from_spec(o.get("theme").expect("theme")));
                lines.push("register".to_owned());
            }
            "resolve" => {
                let name = o.str_("name");
                lines.push(format!("resolve={}", registry.resolve(&name).to_json()));
            }
            "names" => lines.push(format!("names={}", names_json(&registry.names()))),
            other => panic!("unknown registry op {other:?}"),
        }
        o.finish("registry op");
    }
    s.finish("registry script");
    lines.join("\n")
}

/// Runs one case; the registry only matters for `theme_registry` cases.
fn actual(registry: &mut ThemeRegistry, case: &Value) -> String {
    let input = &case["input"];
    match case["entry"].as_str().expect("entry") {
        "theme_resolve" => {
            let name = input["name"].as_str().expect("name");
            theme::resolve(name).to_json()
        }
        "rect_op" => run_rect_op(input),
        "primitive" => run_primitive(input),
        "scene" => run_scene(input),
        "default_markers" => run_markers(input),
        "theme_registry" => run_registry(registry, input),
        other => panic!("unknown entry {other}"),
    }
}

fn clip(s: &str) -> String {
    s.chars().take(400).collect()
}

fn replay(fixture: &Value) -> (BTreeMap<String, usize>, Vec<String>) {
    let mut registry = ThemeRegistry::builtin();
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    let mut failures = Vec::new();
    for case in fixture["cases"].as_array().expect("cases") {
        let id = case["id"].as_str().expect("id");
        let entry = case["entry"].as_str().expect("entry");
        *counts.entry(entry.to_owned()).or_default() += 1;
        let want = case["expected"].as_str().expect("expected");
        let got = actual(&mut registry, case);
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

fn parsed() -> Value {
    serde_json::from_str(FIXTURE).expect("fixture JSON")
}

// --------------------------------------------------------------------- tests

#[test]
fn go_scene_fixture_replays_byte_exact() {
    let fixture = parsed();
    assert_eq!(fixture["schema_version"], 1);
    // The host-dependence guard: how many FMA-ambiguous cases the generator
    // withheld. A change here means the corpus or the guard moved.
    assert_eq!(
        fixture["fma_ambiguous_skipped"].as_u64(),
        Some(FMA_AMBIGUOUS_SKIPPED)
    );
    let (counts, failures) = replay(&fixture);
    // A zero-case or partial run is not a pass: pin every entry's count.
    for (entry, want) in ENTRY_COUNTS {
        assert_eq!(
            counts.get(entry).copied().unwrap_or(0),
            want,
            "cases for {entry}"
        );
    }
    assert_eq!(counts.values().sum::<usize>(), FIXTURE_CASES, "total cases");
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
fn registry_cases_come_last_so_other_cases_saw_the_pristine_registry() {
    let fixture = parsed();
    let mut seen_registry = false;
    for case in fixture["cases"].as_array().expect("cases") {
        if case["entry"] == "theme_registry" {
            seen_registry = true;
        } else {
            assert!(!seen_registry, "{} follows a registry case", case["id"]);
        }
    }
    assert!(seen_registry);
}

/// The corpus must reach the behaviors the port could silently get wrong.
#[test]
fn fixture_exercises_the_discriminating_shapes() {
    let fixture = parsed();
    let expected: Vec<&str> = fixture["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .map(|c| c["expected"].as_str().expect("expected"))
        .collect();
    let any = |needle: &str| expected.iter().any(|e| e.contains(needle));
    for needle in [
        "has NaN bounds: {X:NaN ",
        "has NaN bounds: {X:1e+06 Y:2e-07 ",
        "invalid scene width: NaN",
        "invalid scene width: +Inf",
        "invalid scene width: -5",
        "invalid scene height: -0.5",
        "scene viewBox has negative dimensions",
        "!json: unsupported value: NaN",
        "!json: unsupported value: +Inf",
        "!json: unsupported value: -Inf",
        "\"x\":-0,",
        "\"primitives\":null",
        "\"children\":null",
        "\"points\":null",
        "\"palette\":null",
        "\"font_family\":\"Inter, -apple-system",
        "\\u003c",
        "\\u2028",
    ] {
        assert!(any(needle), "no fixture case contains {needle:?}");
    }
}

#[test]
fn builtin_themes_match_go_names_and_order() {
    assert_eq!(
        theme::names(),
        [
            "behoerde",
            "brief",
            "meeting",
            "report",
            "symaira-dark",
            "symaira-light"
        ]
    );
    assert_eq!(theme::resolve("DARK").name, "symaira-dark");
    assert_eq!(theme::resolve("light").name, "symaira-light");
    assert_eq!(theme::resolve("nope").name, "symaira-dark");
}

#[test]
fn go_math_min_max_follow_the_documented_special_cases() {
    // Go math.Min/Max special cases (math/all_test.go vfminmaxSC).
    assert_eq!(go_min(f64::NAN, f64::NEG_INFINITY), f64::NEG_INFINITY);
    assert!(go_min(f64::NAN, 1.0).is_nan());
    assert!(go_min(1.0, f64::NAN).is_nan());
    assert!(go_min(0.0, -0.0).is_sign_negative());
    assert!(go_min(-0.0, 0.0).is_sign_negative());
    assert!(go_max(-0.0, 0.0).is_sign_positive());
    assert!(go_max(0.0, -0.0).is_sign_positive());
    assert_eq!(go_max(f64::NAN, f64::INFINITY), f64::INFINITY);
    assert!(go_max(f64::NAN, 1.0).is_nan());
    assert_eq!(go_min(2.0, 3.0), 2.0);
    assert_eq!(go_max(2.0, 3.0), 3.0);
}

#[test]
fn go_percent_v_float_formatting() {
    for (value, want) in [
        (100_000.0, "100000"),
        (123_456.0, "123456"),
        (1e6, "1e+06"),
        (1_234_567.0, "1.234567e+06"),
        (123_456_789.0, "1.23456789e+08"),
        (1e20, "1e+20"),
        (1e21, "1e+21"),
        (0.0001, "0.0001"),
        (0.00001, "1e-05"),
        (12345.678, "12345.678"),
        (2e-7, "2e-07"),
        (-5.0, "-5"),
        (-0.5, "-0.5"),
        (0.0, "0"),
        (-0.0, "-0"),
        (f64::NAN, "NaN"),
        (f64::INFINITY, "+Inf"),
        (f64::NEG_INFINITY, "-Inf"),
    ] {
        assert_eq!(go_fmt_v(value), want, "%v of {value:?}");
    }
}

// ---------------------------------------------------------- negative controls

/// The replay must notice a changed expectation: the comparer is not vacuous.
#[test]
fn negative_control_changed_expectation_fails_the_replay() {
    let mut fixture = parsed();
    let cases = fixture["cases"].as_array_mut().expect("cases");
    let case = cases
        .iter_mut()
        .find(|c| c["entry"] == "primitive")
        .expect("a primitive case");
    let mut text = case["expected"].as_str().expect("expected").to_owned();
    text.push('x');
    case["expected"] = Value::String(text);
    let (_, failures) = replay(&fixture);
    assert_eq!(failures.len(), 1, "exactly the mutated case must diverge");
}

/// A changed input must change the result: the replay really executes the
/// input rather than echoing the recorded text.
#[test]
fn negative_control_changed_input_fails_the_replay() {
    let mut fixture = parsed();
    let cases = fixture["cases"].as_array_mut().expect("cases");
    let case = cases
        .iter_mut()
        .find(|c| {
            c["entry"] == "primitive"
                && c["input"]["primitive"]["type"] == "rect"
                && c["input"]["primitive"]["stroke_width"].as_f64() == Some(3.5)
        })
        .expect("a stroked rect case");
    case["input"]["primitive"]["stroke_width"] = serde_json::json!(1.0);
    let (_, failures) = replay(&fixture);
    assert_eq!(failures.len(), 1);
}

/// Dropping one registration changes the `names` output of later scripts: the
/// scripts really accumulate state.
#[test]
fn negative_control_registry_state_is_cumulative() {
    let mut fixture = parsed();
    let cases = fixture["cases"].as_array_mut().expect("cases");
    let index = cases
        .iter()
        .position(|c| {
            c["entry"] == "theme_registry"
                && c["expected"]
                    .as_str()
                    .is_some_and(|e| e.contains("Keeps-Font"))
        })
        .expect("the Keeps-Font registration script");
    // Make the script a no-op register of an unnamed theme.
    cases[index]["input"]["ops"][0]["theme"]["name"] = Value::String(String::new());
    let (_, failures) = replay(&fixture);
    assert!(
        failures.len() > 1,
        "removing a registration must also change later scripts, got {}",
        failures.len()
    );
}

/// A decoder that ignores a spec field is caught by the consumed-key guard.
#[test]
#[should_panic(expected = "was not decoded")]
fn negative_control_unknown_spec_key_is_rejected() {
    let spec = serde_json::json!({"type": "rect", "width": 1, "height": 1, "bogus": 2});
    let _ = primitive(&spec);
}
