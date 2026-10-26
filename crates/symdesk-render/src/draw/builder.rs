//! IR-to-scene lowering for graph, tree, timeline and custom diagrams.
use super::{
    ir, layout,
    measure::{FontWeight, NodeMeasureOptions},
    scene::*,
    theme,
};
use std::collections::BTreeMap;

fn fallback<'a>(s: &'a str, default: &'a str) -> &'a str {
    if s.is_empty() { default } else { s }
}
#[allow(clippy::too_many_arguments)]
fn text(
    sc: &mut Scene,
    x: f64,
    y: f64,
    value: &str,
    size: f64,
    color: &str,
    anchor: &str,
    baseline: &str,
    id: String,
    bold: bool,
) {
    sc.add(Primitive::Text(TextElement {
        x,
        y,
        text: value.into(),
        font_size: size,
        fill: color.into(),
        anchor: anchor.into(),
        baseline: baseline.into(),
        id,
        font_weight: if bold { "bold" } else { "regular" }.into(),
        ..Default::default()
    }));
}
/// Unsupported chart lowering remains explicit rather than silently emitting a graph.
pub fn build_scene(d: &ir::Diagram) -> Result<Scene, String> {
    ir::validate(d).map_err(|e| {
        format!(
            "validate ir diagram: invalid ir field {}: {}",
            super::gotext::quote(&e.field),
            e.message
        )
    })?;
    if d.kind == "chart" || d.chart.is_some() {
        return Err("chart scene lowering is not ported".into());
    }
    if d.kind == "sequence" {
        return sequence_scene(d);
    }
    let th = theme::resolve(&d.theme);
    let mut sc = Scene::new(
        if d.width <= 0.0 { 800.0 } else { d.width },
        if d.height <= 0.0 { 600.0 } else { d.height },
        &th,
    );
    let m = default_measurer();
    let opts = NodeMeasureOptions::default();
    let positioned = d.nodes.iter().any(|n| n.x != 0.0 || n.y != 0.0);
    let lay = if d.kind == "graph" && !positioned {
        Some(layout::layout_graph(m, d).map_err(|e| format!("layout graph: {e}"))?)
    } else {
        None
    };
    let mut boxes = BTreeMap::new();
    let (mut cur_x, mut cur_y) = (60.0, 60.0);
    for node in &d.nodes {
        let size = m.measure_node(&node.label, &node.shape, opts);
        let (mut w, mut h) = (
            if node.width > 0.0 {
                node.width
            } else {
                size.width
            },
            if node.height > 0.0 {
                node.height
            } else {
                size.height
            },
        );
        let (mut x, mut y) = (node.x, node.y);
        if let Some(l) = &lay {
            if let Some(b) = l.nodes.get(&node.id) {
                x = b.x;
                y = b.y;
                w = b.width;
                h = b.height;
            }
        } else if !positioned {
            if d.direction == "LR" || d.direction == "RL" {
                x = cur_x;
                y = 100.0;
                cur_x += w + 60.0;
            } else {
                x = 100.0;
                y = cur_y;
                cur_y += h + 60.0;
            }
        }
        boxes.insert(
            node.id.clone(),
            Rect {
                x,
                y,
                width: w,
                height: h,
            },
        );
        let fill = fallback(&node.style.fill, &th.surface).to_owned();
        let stroke = fallback(&node.style.stroke, &th.border).to_owned();
        let sw = node.style.stroke_width.unwrap_or(1.5);
        let cx = x + w / 2.0;
        let cy = y + h / 2.0;
        let coords = match node.shape.as_str() {
            "diamond" => vec![(cx, y), (x + w, cy), (cx, y + h), (x, cy)],
            "hexagon" => vec![
                (x + h * 0.25, y),
                (x + w - h * 0.25, y),
                (x + w, cy),
                (x + w - h * 0.25, y + h),
                (x + h * 0.25, y + h),
                (x, cy),
            ],
            "asymmetric" => vec![
                (x, y),
                (x + w - 10.0, y),
                (x + w, y + h / 2.0),
                (x + w - 10.0, y + h),
                (x, y + h),
            ],
            _ => vec![],
        };
        if !coords.is_empty() {
            sc.add(Primitive::Polygon(PolygonElement {
                id: node.id.clone(),
                points: Some(coords.into_iter().map(|(x, y)| Point { x, y }).collect()),
                fill,
                stroke,
                stroke_width: sw,
                ..Default::default()
            }));
        } else if node.shape == "circle" {
            let r = w / 2.0;
            sc.add(Primitive::Circle(CircleElement {
                id: node.id.clone(),
                cx: x + r,
                cy: y + r,
                r,
                fill,
                stroke,
                stroke_width: sw,
                link: node.note.clone(),
                ..Default::default()
            }));
        } else {
            let radius = match node.shape.as_str() {
                "cylinder" => 12.0,
                "pill" | "stadium" => h / 2.0,
                "round" => 8.0,
                _ => 0.0,
            };
            sc.add(Primitive::Rect(RectElement {
                id: node.id.clone(),
                x,
                y,
                width: w,
                height: h,
                rx: radius,
                ry: radius,
                fill,
                stroke,
                stroke_width: sw,
                link: node.note.clone(),
                ..Default::default()
            }));
        }
        if !size.lines.is_empty() {
            let metrics = m
                .measure_text_detailed("Ag", opts.font_size, FontWeight::Regular)
                .map_err(|e| e.to_string())?;
            let total = size.lines.len() as f64 * metrics.line_height
                + (size.lines.len() - 1) as f64 * opts.line_spacing;
            let start = y + (h - total) / 2.0 + metrics.ascent;
            for (i, line) in size.lines.iter().enumerate() {
                text(
                    &mut sc,
                    cx,
                    start + i as f64 * (metrics.line_height + opts.line_spacing),
                    line,
                    opts.font_size,
                    fallback(&node.style.text_color, &th.text),
                    "middle",
                    "alphabetic",
                    format!("{}-text-{i}", node.id),
                    false,
                );
            }
        }
    }
    for (i, e) in d.edges.iter().enumerate() {
        let (Some(a), Some(b)) = (boxes.get(&e.from), boxes.get(&e.to)) else {
            continue;
        };
        let color = fallback(&e.color, &th.edge);
        let sw = if e.style == "thick" { 3.0 } else { 1.5 };
        let dash = match e.style.as_str() {
            "dashed" => "6 4",
            "dotted" => "2 3",
            _ => "",
        };
        let pts = if let Some(l) = &lay {
            l.routes[i].points.clone()
        } else if d.direction == "LR" {
            vec![
                (a.x + a.width, a.y + a.height / 2.0),
                (b.x, b.y + b.height / 2.0),
            ]
        } else {
            vec![
                (a.x + a.width / 2.0, a.y + a.height),
                (b.x + b.width / 2.0, b.y),
            ]
        };
        if pts.len() < 2 {
            continue;
        }
        let marker = if lay.is_some() || matches!(e.arrow.as_str(), "" | "single" | "double") {
            "arrow-end"
        } else {
            ""
        };
        if pts.len() == 2 {
            sc.add(Primitive::Line(LineElement {
                id: format!("edge-{i}"),
                x1: pts[0].0,
                y1: pts[0].1,
                x2: pts[1].0,
                y2: pts[1].1,
                stroke: color.into(),
                stroke_width: sw,
                dash_array: dash.into(),
                marker_end: marker.into(),
                ..Default::default()
            }));
        } else {
            sc.add(Primitive::Polyline(PolylineElement {
                id: format!("edge-{i}"),
                points: Some(pts.iter().map(|&(x, y)| Point { x, y }).collect()),
                fill: "none".into(),
                stroke: color.into(),
                stroke_width: sw,
                dash_array: dash.into(),
                marker_end: marker.into(),
                ..Default::default()
            }));
        }
        if !e.label.is_empty() {
            let (x, y) = if lay.is_some() {
                pts[pts.len() / 2]
            } else {
                ((pts[0].0 + pts[1].0) / 2.0, (pts[0].1 + pts[1].1) / 2.0)
            };
            text(
                &mut sc,
                x + 6.0,
                y - 4.0,
                &e.label,
                11.0,
                &th.text_muted,
                "start",
                "middle",
                if lay.is_some() {
                    format!("edge-{i}-label")
                } else {
                    String::new()
                },
                false,
            );
        }
    }
    for (i, g) in d.groups.iter().enumerate() {
        let mut bounds = None;
        for id in g.members.as_deref().unwrap_or_default() {
            if let Some(b) = boxes.get(id) {
                bounds = Some(bounds.map_or(*b, |a: Rect| a.union(*b)));
            }
        }
        if let Some(b) = bounds {
            let b = b.pad(20.0);
            sc.add(Primitive::Rect(RectElement {
                id: format!("group-{i}"),
                x: b.x,
                y: b.y,
                width: b.width,
                height: b.height,
                rx: 6.0,
                ry: 6.0,
                fill: th.group_background.clone(),
                stroke: th.group_border.clone(),
                stroke_width: 1.0,
                dash_array: "4 4".into(),
                ..Default::default()
            }));
            if !g.label.is_empty() {
                text(
                    &mut sc,
                    b.x + 12.0,
                    b.y + 16.0,
                    &g.label,
                    12.0,
                    &th.text_muted,
                    "start",
                    "middle",
                    String::new(),
                    true,
                );
            }
        }
    }
    sc.fit_to_bounds(30.0);
    Ok(sc)
}
fn sequence_scene(d: &ir::Diagram) -> Result<Scene, String> {
    let lay = layout::layout_sequence(d).map_err(|e| format!("sequence layout: {e}"))?;
    // The layout decoder has already validated the actor/message payload shapes.
    fn field(v: &ir::Value, key: &str) -> String {
        match v {
            ir::Value::Map(m) => m
                .iter()
                .filter(|(k, _)| k.eq_ignore_ascii_case(key))
                .filter_map(|(_, v)| {
                    if let ir::Value::Str(s) = v {
                        Some(s.clone())
                    } else {
                        None
                    }
                })
                .next_back()
                .unwrap_or_default(),
            ir::Value::Struct(m) => m
                .iter()
                .find(|(k, _)| k.eq_ignore_ascii_case(key))
                .and_then(|(_, v)| {
                    if let ir::Value::Str(s) = v {
                        Some(s.clone())
                    } else {
                        None
                    }
                })
                .unwrap_or_default(),
            _ => String::new(),
        }
    }
    let th = theme::resolve(&d.theme);
    let mut sc = Scene::new(lay.width, lay.height, &th);
    if let Some(ir::Value::Arr(actors)) = d.custom.get("actors") {
        for a in actors {
            let x = *lay.actor_x.get(&field(a, "ID")).unwrap_or(&0.0);
            sc.add(Primitive::Rect(RectElement {
                x: x - 70.0,
                y: 20.0,
                width: 140.0,
                height: 40.0,
                rx: 8.0,
                ry: 8.0,
                fill: th.surface.clone(),
                stroke: th.border.clone(),
                ..Default::default()
            }));
            text(
                &mut sc,
                x,
                45.0,
                &field(a, "Label"),
                14.0,
                &th.text,
                "middle",
                "middle",
                String::new(),
                false,
            );
            sc.add(Primitive::Line(LineElement {
                x1: x,
                y1: 70.0,
                x2: x,
                y2: lay.height - 40.0,
                stroke: th.border_subtle.clone(),
                stroke_width: 1.0,
                dash_array: "4 4".into(),
                ..Default::default()
            }));
        }
    }
    if let Some(ir::Value::Arr(messages)) = d.custom.get("messages") {
        for (i, a) in messages.iter().enumerate() {
            let from = *lay.actor_x.get(&field(a, "From")).unwrap_or(&0.0);
            let mut to = *lay.actor_x.get(&field(a, "To")).unwrap_or(&0.0);
            if to == 0.0 {
                to = from;
            }
            let y = lay.message_y[i];
            sc.add(Primitive::Line(LineElement {
                x1: from,
                y1: y,
                x2: to,
                y2: y,
                stroke: th.edge.clone(),
                stroke_width: 1.5,
                ..Default::default()
            }));
            let label = field(a, "Text");
            if !label.is_empty() {
                text(
                    &mut sc,
                    (from + to) / 2.0,
                    y - 8.0,
                    &label,
                    12.0,
                    &th.text_muted,
                    "middle",
                    "middle",
                    String::new(),
                    false,
                );
            }
        }
    }
    Ok(sc)
}
