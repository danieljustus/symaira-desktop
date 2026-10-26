//! Deterministic SVG serialization of the production scene graph.
//! Supports text elements and unhinted embedded-Inter text outlines.
use super::measure::{FontWeight, Measurer};
use super::scene::*;

#[derive(Debug, Default, Clone)]
pub struct SvgOptions {
    pub text_as_paths: bool,
    pub font_hash: String,
    pub generator: String,
}

fn number(v: f64) -> String {
    if v.is_nan() {
        "NaN".into()
    } else if v == f64::INFINITY {
        "+Inf".into()
    } else if v == f64::NEG_INFINITY {
        "-Inf".into()
    } else {
        format!("{v:.2}")
    }
}
fn escape(s: &str) -> String {
    let mut out = String::new();
    for c in s.chars() {
        out.push_str(match c {
            '&' => "&amp;",
            '<' => "&lt;",
            '>' => "&gt;",
            '\'' => "&#39;",
            '"' => "&#34;",
            '\t' => "&#x9;",
            '\n' => "&#xA;",
            '\r' => "&#xD;",
            c if !(c == '\u{85}' || c >= ' ') || c == '\u{fffe}' || c == '\u{ffff}' => "\u{fffd}",
            _ => {
                out.push(c);
                continue;
            }
        });
    }
    out
}
fn attr(out: &mut String, key: &str, value: &str) {
    out.push(' ');
    out.push_str(key);
    out.push_str("=\"");
    out.push_str(&escape(value));
    out.push('"');
}
fn num(out: &mut String, key: &str, value: f64) {
    attr(out, key, &number(value));
}
fn optional(out: &mut String, key: &str, value: &str) {
    if !value.is_empty() {
        attr(out, key, value);
    }
}
fn opacity(out: &mut String, value: f64) {
    if value > 0.0 && value < 1.0 {
        num(out, "opacity", value);
    }
}
#[allow(clippy::too_many_arguments)]
fn common(
    out: &mut String,
    id: &str,
    class: &str,
    fill: &str,
    stroke: &str,
    width: f64,
    dash: &str,
    alpha: f64,
) {
    optional(out, "id", id);
    optional(out, "class", class);
    if !fill.is_empty() {
        attr(out, "fill", fill);
    } else if !stroke.is_empty() {
        attr(out, "fill", "none");
    }
    optional(out, "stroke", stroke);
    if width > 0.0 {
        num(out, "stroke-width", width);
    }
    optional(out, "stroke-dasharray", dash);
    opacity(out, alpha);
}
fn marker(out: &mut String, key: &str, id: &str) {
    if !id.is_empty() {
        attr(out, key, &format!("url(#{id})"));
    }
}
fn points(out: &mut String, pts: &Option<Vec<Point>>) {
    let text = pts
        .as_deref()
        .unwrap_or_default()
        .iter()
        .map(|p| format!("{},{}", number(p.x), number(p.y)))
        .collect::<Vec<_>>()
        .join(" ");
    attr(out, "points", &text);
}
/// Go measure.SegmentsToSVGPath: one separator between segments, no trimming.
pub fn segments_to_svg_path(segments: &[PathSegment]) -> String {
    let mut out = String::new();
    for (index, s) in segments.iter().enumerate() {
        if index > 0 {
            out.push(' ');
        }
        out.push_str(&s.op);
        for (i, p) in s.args.iter().enumerate() {
            if i > 0 {
                out.push(' ');
            }
            out.push_str(&number(p.x));
            out.push(' ');
            out.push_str(&number(p.y));
        }
    }
    out
}
fn link(p: &Primitive) -> &str {
    match p {
        Primitive::Rect(e) => &e.link,
        Primitive::Circle(e) => &e.link,
        Primitive::Ellipse(e) => &e.link,
        Primitive::Path(e) => &e.link,
        Primitive::Text(e) => &e.link,
        Primitive::Group(e) => &e.link,
        _ => "",
    }
}
fn linked(out: &mut String, p: &Primitive, indent: &str, m: &Measurer, paths: bool) {
    out.push_str(indent);
    if !link(p).is_empty() {
        out.push_str("<a");
        attr(out, "href", link(p));
        out.push_str(">\n");
        primitive(out, p, &format!("{indent}  "), m, paths);
        out.push('\n');
        out.push_str(indent);
        out.push_str("</a>");
    } else {
        primitive(out, p, indent, m, paths);
    }
}
fn primitive(out: &mut String, p: &Primitive, indent: &str, m: &Measurer, paths: bool) {
    macro_rules! common {
        ($e:expr,$dash:expr) => {
            common(
                out,
                &$e.id,
                &$e.class,
                &$e.fill,
                &$e.stroke,
                $e.stroke_width,
                $dash,
                $e.opacity,
            )
        };
    }
    match p {
        Primitive::Rect(e) => {
            out.push_str("<rect");
            common!(e, &e.dash_array);
            num(out, "x", e.x);
            num(out, "y", e.y);
            num(out, "width", e.width);
            num(out, "height", e.height);
            if e.rx > 0.0 {
                num(out, "rx", e.rx);
            }
            if e.ry > 0.0 {
                num(out, "ry", e.ry);
            }
            out.push_str(" />");
        }
        Primitive::Circle(e) => {
            out.push_str("<circle");
            common!(e, "");
            num(out, "cx", e.cx);
            num(out, "cy", e.cy);
            num(out, "r", e.r);
            out.push_str(" />");
        }
        Primitive::Ellipse(e) => {
            out.push_str("<ellipse");
            common!(e, "");
            num(out, "cx", e.cx);
            num(out, "cy", e.cy);
            num(out, "rx", e.rx);
            num(out, "ry", e.ry);
            out.push_str(" />");
        }
        Primitive::Line(e) => {
            out.push_str("<line");
            common(
                out,
                &e.id,
                &e.class,
                "",
                &e.stroke,
                e.stroke_width,
                &e.dash_array,
                e.opacity,
            );
            num(out, "x1", e.x1);
            num(out, "y1", e.y1);
            num(out, "x2", e.x2);
            num(out, "y2", e.y2);
            marker(out, "marker-start", &e.marker_start);
            marker(out, "marker-end", &e.marker_end);
            out.push_str(" />");
        }
        Primitive::Polyline(e) => {
            out.push_str("<polyline");
            common!(e, &e.dash_array);
            points(out, &e.points);
            marker(out, "marker-end", &e.marker_end);
            out.push_str(" />");
        }
        Primitive::Polygon(e) => {
            out.push_str("<polygon");
            common!(e, &e.dash_array);
            points(out, &e.points);
            out.push_str(" />");
        }
        Primitive::Path(e) => {
            out.push_str("<path");
            common!(e, &e.dash_array);
            attr(
                out,
                "d",
                &if e.d.is_empty() {
                    segments_to_svg_path(&e.segments)
                } else {
                    e.d.clone()
                },
            );
            marker(out, "marker-end", &e.marker_end);
            out.push_str(" />");
        }
        Primitive::Group(e) => {
            out.push_str("<g");
            optional(out, "id", &e.id);
            optional(out, "class", &e.class);
            optional(out, "transform", &e.transform);
            out.push_str(">\n");
            for child in e.children.as_deref().unwrap_or_default() {
                linked(out, child, &format!("{indent}  "), m, paths);
                out.push('\n');
            }
            out.push_str(indent);
            out.push_str("</g>");
        }
        Primitive::Text(e) => {
            if paths {
                let size = if e.font_size <= 0.0 {
                    14.0
                } else {
                    e.font_size
                };
                let weight = FontWeight::from_go(&e.font_weight);
                let width = if e.text_length <= 0.0 {
                    m.measure_text(&e.text, size, weight).0
                } else {
                    e.text_length
                };
                let x = match e.anchor.as_str() {
                    "middle" => e.x - width / 2.0,
                    "end" => e.x - width,
                    _ => e.x,
                };
                let metrics = m
                    .measure_text_detailed("Ag", size, weight)
                    .expect("embedded font metrics");
                let y = match e.baseline.as_str() {
                    "middle" | "central" => e.y + metrics.line_height / 2.0 - metrics.descent,
                    "hanging" | "top" => e.y + metrics.ascent,
                    _ => e.y,
                };
                let segments = m.outlines(&e.text, size, weight, x, y);
                if !segments.is_empty() {
                    out.push_str("<path");
                    attr(
                        out,
                        "fill",
                        if e.fill.is_empty() {
                            "#F5F4F0"
                        } else {
                            &e.fill
                        },
                    );
                    attr(out, "d", &segments_to_svg_path(&segments));
                    optional(out, "id", &e.id);
                    optional(out, "class", &e.class);
                    opacity(out, e.opacity);
                    out.push_str(" />");
                    return;
                }
            }
            out.push_str("<text");
            optional(out, "id", &e.id);
            optional(out, "class", &e.class);
            let size = if e.font_size <= 0.0 {
                14.0
            } else {
                e.font_size
            };
            num(out, "x", e.x);
            num(out, "y", e.y);
            attr(
                out,
                "font-family",
                if e.font_family.is_empty() {
                    "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
                } else {
                    &e.font_family
                },
            );
            num(out, "font-size", size);
            if e.font_weight == "bold" {
                attr(out, "font-weight", "bold");
            }
            attr(
                out,
                "fill",
                if e.fill.is_empty() {
                    "#F5F4F0"
                } else {
                    &e.fill
                },
            );
            if e.anchor != "start" {
                optional(out, "text-anchor", &e.anchor);
            }
            if e.baseline != "alphabetic" {
                optional(out, "dominant-baseline", &e.baseline);
            }
            let width = if e.text_length <= 0.0 {
                m.measure_text(&e.text, size, FontWeight::from_go(&e.font_weight))
                    .0
            } else {
                e.text_length
            };
            if width > 0.0 {
                num(out, "textLength", width);
            }
            opacity(out, e.opacity);
            out.push('>');
            out.push_str(&escape(&e.text));
            out.push_str("</text>");
        }
    }
}
/// Go EmitSVG; None preserves its nil-scene error.
pub fn emit_svg(scene: Option<&Scene>, opts: &SvgOptions) -> Result<String, String> {
    let sc = scene.ok_or("scene is nil")?;
    let m = default_measurer();
    let mut out = String::from(
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\" xmlns:xlink=\"http://www.w3.org/1999/xlink\"",
    );
    attr(
        &mut out,
        "viewBox",
        &format!(
            "{} {} {} {}",
            number(sc.view_box.x),
            number(sc.view_box.y),
            number(sc.view_box.width),
            number(sc.view_box.height)
        ),
    );
    num(&mut out, "width", sc.width);
    num(&mut out, "height", sc.height);
    out.push_str(">\n  <metadata>\n");
    let hash = if !opts.font_hash.is_empty() {
        &opts.font_hash
    } else {
        sc.metadata
            .get("font-hash")
            .filter(|v| !v.is_empty())
            .map(String::as_str)
            .unwrap_or(m.font_hash())
    };
    out.push_str(&format!("    <symdraw:font-hash>{}</symdraw:font-hash>\n    <symdraw:generator>{}</symdraw:generator>\n",escape(hash),escape(if opts.generator.is_empty(){"symdraw"}else{&opts.generator})));
    for (key, value) in &sc.metadata {
        if key != "font-hash" && key != "generator" {
            out.push_str("    <symdraw:meta");
            attr(&mut out, "name", key);
            out.push('>');
            out.push_str(&escape(value));
            out.push_str("</symdraw:meta>\n");
        }
    }
    out.push_str("  </metadata>\n");
    if !sc.markers.is_empty() {
        out.push_str("  <defs>\n");
        for e in &sc.markers {
            out.push_str("    <marker");
            attr(&mut out, "id", &e.id);
            attr(
                &mut out,
                "viewBox",
                &format!(
                    "{} {} {} {}",
                    number(e.view_box.x),
                    number(e.view_box.y),
                    number(e.view_box.width),
                    number(e.view_box.height)
                ),
            );
            num(&mut out, "refX", e.ref_x);
            num(&mut out, "refY", e.ref_y);
            num(&mut out, "markerWidth", e.marker_width);
            num(&mut out, "markerHeight", e.marker_height);
            attr(&mut out, "orient", &e.orient);
            out.push_str(">\n");
            if let Some(p) = &e.path {
                out.push_str("      ");
                primitive(&mut out, p, "      ", m, opts.text_as_paths);
                out.push('\n');
            }
            out.push_str("    </marker>\n");
        }
        out.push_str("  </defs>\n");
    }
    if !sc.background.is_empty() && sc.background != "none" && sc.background != "transparent" {
        out.push_str("  <rect");
        num(&mut out, "x", sc.view_box.x);
        num(&mut out, "y", sc.view_box.y);
        num(&mut out, "width", sc.view_box.width);
        num(&mut out, "height", sc.view_box.height);
        attr(&mut out, "fill", &sc.background);
        out.push_str(" />\n");
    }
    for p in &sc.primitives {
        linked(&mut out, p, "  ", m, opts.text_as_paths);
        out.push('\n');
    }
    out.push_str("</svg>\n");
    Ok(out)
}
