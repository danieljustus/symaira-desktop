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
    assert!(!cases.is_empty());
    for c in cases {
        let id = c["id"].as_str().unwrap();
        let input = c["input"].as_str().unwrap();
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
fn chart_lowering_residual_is_explicit() {
    let d = parse_source(
        r#"{"kind":"chart","chart":{"type":"bar","series":[{"name":"x","data":[{"x":1,"y":2}]}]}}"#,
    )
    .unwrap();
    assert_eq!(
        build_scene(&d).unwrap_err(),
        "chart scene lowering is not ported"
    );
}
