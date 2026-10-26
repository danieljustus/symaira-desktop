use symdesk_render::draw::{
    builder::build_scene,
    decode_unvalidated,
    emit::{SvgOptions, emit_svg},
    parse_source,
};
#[test]
fn go_scene_lowering_svg_contract() {
    let fixture: serde_json::Value =
        serde_json::from_str(include_str!("../../../testdata/port/render/emit.json")).unwrap();
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(fixture["schema_version"], 1);
    assert_eq!(fixture["chart_residual"], 0);
    assert_eq!(fixture["fma_withheld"], 3);
    assert_eq!(cases.len(), 1350);
    for c in cases {
        let id = c["id"].as_str().unwrap();
        let input = c["input"].as_str().unwrap();
        if c["mode"] == "primitives" {
            let input: serde_json::Value = serde_json::from_str(input).unwrap();
            let s = direct_scene(input["background"].as_str().unwrap_or_default());
            let actual: serde_json::Value = serde_json::from_str(&s.to_json().unwrap()).unwrap();
            assert_eq!(actual, input, "direct scene inputs differ");
            let svg = emit_svg(
                Some(&s),
                &SvgOptions {
                    font_hash: "override<&".into(),
                    generator: "generator\"".into(),
                },
            )
            .unwrap();
            assert_eq!(svg, c["expected"].as_str().unwrap(), "{id}");
            continue;
        }
        let d = if c["mode"] == "ir" {
            decode_unvalidated(input.as_bytes()).unwrap()
        } else {
            parse_source(input).unwrap()
        };
        let result = build_scene(&d).and_then(|s| emit_svg(Some(&s), &SvgOptions::default()));
        match result {
            Ok(svg) => {
                assert_eq!(c["error"], "", "{id}");
                assert_eq!(svg, c["expected"].as_str().unwrap(), "{id}");
            }
            Err(err) => assert_eq!(err, c["error"].as_str().unwrap(), "{id}"),
        }
    }
}
#[test]
fn svg_nil_scene_contract() {
    assert_eq!(
        emit_svg(None, &SvgOptions::default()).unwrap_err(),
        "scene is nil"
    );
}
#[test]
fn chart_lowering_is_accepted() {
    let d = parse_source(
        r#"{"kind":"chart","chart":{"type":"bar","series":[{"name":"x","data":[{"x":1,"y":2}]}]}}"#,
    )
    .unwrap();
    assert!(!build_scene(&d).unwrap().primitives.is_empty());
}

fn direct_scene(background: &str) -> symdesk_render::draw::scene::Scene {
    use symdesk_render::draw::{scene::*, theme};
    let mut s = Scene::new(160.0, 90.0, &theme::resolve("symaira-light"));
    s.background = background.into();
    s.metadata = [
        ("z", "<&\"'\t\r\n\0"),
        ("a\"", "日本語"),
        ("generator", "ignored"),
        ("font-hash", "custom"),
    ]
    .into_iter()
    .map(|(k, v)| (k.into(), v.into()))
    .collect();
    s.add(Primitive::Group(GroupElement {
        id: "group".into(),
        class: "c".into(),
        transform: "translate(1,2)".into(),
        link: "https://example.invalid/?a=1&b=2".into(),
        children: Some(vec![
            Primitive::Ellipse(EllipseElement {
                id: "ellipse".into(),
                cx: 1.25,
                cy: 2.0,
                rx: 3.0,
                ry: 4.0,
                stroke: "red".into(),
                stroke_width: 0.5,
                opacity: 0.25,
                link: "<link>".into(),
                ..Default::default()
            }),
            Primitive::Path(PathElement {
                id: "path".into(),
                segments: vec![
                    PathSegment {
                        op: "M".into(),
                        args: vec![Point { x: 1.0, y: 2.0 }],
                    },
                    PathSegment {
                        op: "L".into(),
                        args: vec![Point { x: 3.0, y: 4.0 }],
                    },
                ],
                fill: "none".into(),
                stroke: "blue".into(),
                marker_end: "arrow-end".into(),
                dash_array: "2 3".into(),
                opacity: 0.5,
                ..Default::default()
            }),
            Primitive::Text(TextElement {
                id: "text".into(),
                class: "bold".into(),
                text: "<&\"'\n".into(),
                font_weight: "bold".into(),
                anchor: "end".into(),
                baseline: "top".into(),
                text_length: 42.0,
                opacity: 0.75,
                ..Default::default()
            }),
            Primitive::Line(LineElement {
                x1: 1.0,
                y1: 2.0,
                x2: 3.0,
                y2: 4.0,
                marker_start: "arrow-start".into(),
                marker_end: "arrow-end".into(),
                ..Default::default()
            }),
        ]),
    }));
    s
}
