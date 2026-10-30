//! Chart scene lowering, preserving Go's primitive ordering and arithmetic.
use super::{ir, scene::*, theme};

fn text(sc: &mut Scene, x: f64, y: f64, value: &str, size: f64, fill: &str, anchor: &str) {
    sc.add(Primitive::Text(TextElement {
        x,
        y,
        text: value.into(),
        font_size: size,
        font_weight: "regular".into(),
        fill: fill.into(),
        anchor: anchor.into(),
        baseline: "middle".into(),
        ..Default::default()
    }));
}
fn line(sc: &mut Scene, x1: f64, y1: f64, x2: f64, y2: f64, color: &str, grid: bool) {
    sc.add(Primitive::Line(LineElement {
        x1,
        y1,
        x2,
        y2,
        stroke: color.into(),
        stroke_width: if grid { 1.0 } else { 1.5 },
        dash_array: if grid { "4 4" } else { "" }.into(),
        ..Default::default()
    }));
}
fn color<'a>(value: &'a str, default: &'a str) -> &'a str {
    if value.is_empty() { default } else { value }
}
fn nice_axis(mut min: f64, mut max: f64) -> (f64, f64, Vec<f64>) {
    if max <= min {
        let span = if min == 0.0 { 1.0 } else { min.abs() };
        min -= span / 2.0;
        max += span / 2.0;
    }
    let raw = (max - min) / 5.0;
    let power = 10_f64.powf(raw.log10().floor());
    let unit = raw / power;
    let step = power
        * if unit > 5.0 {
            10.0
        } else if unit > 2.0 {
            5.0
        } else if unit > 1.0 {
            2.0
        } else {
            1.0
        };
    let a = (min / step).floor() * step;
    let b = (max / step).ceil() * step;
    (
        a,
        b,
        (0..=((b - a) / step).round() as usize)
            .map(|i| a + i as f64 * step)
            .collect(),
    )
}
fn tick(v: f64) -> String {
    go_fmt_v(if v.abs() < 1e-9 { 0.0 } else { v })
}
/// Go `BuildChartScene` (without the outer IR validator).
pub fn build_chart_scene(d: &ir::Diagram) -> Result<Scene, String> {
    let c = d.chart.as_ref().ok_or("chart specification is required")?;
    let th = theme::resolve(&d.theme);
    let palette = th.palette.as_deref().expect("resolved theme has a palette");
    let w = if d.width <= 0.0 { 600.0 } else { d.width };
    let h = if d.height <= 0.0 { 360.0 } else { d.height };
    let mut sc = Scene::new(w, h, &th);
    let series = c.series.as_deref().unwrap_or_default();
    sc.metadata.insert(
        "title".into(),
        if !c.title.is_empty() {
            c.title.clone()
        } else if !d.title.is_empty() {
            d.title.clone()
        } else {
            format!("{} chart", c.r#type)
        },
    );
    sc.metadata.insert(
        "description".into(),
        format!("{} chart with {} series", c.r#type, series.len()),
    );
    let top = if c.title.is_empty() {
        30.0
    } else {
        sc.add(Primitive::Text(TextElement {
            x: w / 2.0,
            y: 35.0,
            text: c.title.clone(),
            font_size: 16.0,
            font_weight: "bold".into(),
            fill: th.text.clone(),
            anchor: "middle".into(),
            baseline: "middle".into(),
            ..Default::default()
        }));
        60.0
    };
    let left = 60.0;
    let right = w - if c.legend { 140.0 } else { 40.0 };
    let bottom = h - 50.0;
    let pw = right - left;
    let ph = bottom - top;
    let points = || {
        series
            .iter()
            .flat_map(|s| s.data.as_deref().unwrap_or_default())
    };
    let mut bounds = points().map(|p| p.y);
    let (mut min, mut max) = if let Some(y) = bounds.next() {
        bounds.fold((y, y), |(a, b), y| (go_min(a, y), go_max(b, y)))
    } else {
        (0.0, 1.0)
    };
    min = c.y_axis.min.unwrap_or(if min > 0.0 { 0.0 } else { min });
    max = c.y_axis.max.unwrap_or(max);
    let (min, max, ticks) = nice_axis(min, max);
    let (mut xmin, mut xmax) = (0.0, 1.0);
    if c.r#type == "scatter" {
        let mut xs = points().map(|p| p.x);
        if let Some(x) = xs.next() {
            (xmin, xmax) = xs.fold((x, x), |(a, b), x| (go_min(a, x), go_max(b, x)));
        }
        xmin = c.x_axis.min.unwrap_or(xmin);
        xmax = c.x_axis.max.unwrap_or(xmax);
        // Go intentionally shadows the outer bounds here; scatter data use the raw bounds.
        let (a, b, xt) = nice_axis(xmin, xmax);
        for v in xt {
            let x = left + (v - a) / (b - a) * pw;
            line(&mut sc, x, top, x, bottom, &th.grid, true);
            text(
                &mut sc,
                x,
                bottom + 18.0,
                &tick(v),
                10.0,
                &th.text_muted,
                "middle",
            );
        }
    }
    for v in ticks {
        let y = bottom - (v - min) / (max - min) * ph;
        line(&mut sc, left, y, right, y, &th.grid, true);
        text(
            &mut sc,
            left - 10.0,
            y,
            &tick(v),
            10.0,
            &th.text_muted,
            "end",
        );
    }
    line(&mut sc, left, top, left, bottom, &th.border, false);
    line(&mut sc, left, bottom, right, bottom, &th.border, false);
    match c.r#type.as_str() {
        "pie" | "donut" => {
            let (cx, cy, r) = (w / 2.0, h / 2.0, go_min(w, h) * 0.35);
            let total: f64 = points().map(|p| go_max(0.0, p.y)).sum();
            if total > 0.0 {
                let mut start = -std::f64::consts::PI / 2.0;
                let mut ci = 0;
                for p in points() {
                    if p.y <= 0.0 {
                        continue;
                    }
                    let angle = (p.y / total) * 2.0 * std::f64::consts::PI;
                    let end = start + angle;
                    let fill = if p.color.is_empty() {
                        let v = palette[ci % palette.len()].clone();
                        ci += 1;
                        v
                    } else {
                        p.color.clone()
                    };
                    let path = format!(
                        "M {:.2} {:.2} A {:.2} {:.2} 0 {} 1 {:.2} {:.2} L {:.2} {:.2} Z",
                        cx + r * start.cos(),
                        cy + r * start.sin(),
                        r,
                        r,
                        usize::from(angle > std::f64::consts::PI),
                        cx + r * end.cos(),
                        cy + r * end.sin(),
                        cx,
                        cy
                    );
                    sc.add(Primitive::Path(PathElement {
                        d: path,
                        fill,
                        stroke: th.background.clone(),
                        stroke_width: 2.0,
                        ..Default::default()
                    }));
                    start = end;
                }
                if c.r#type == "donut" {
                    sc.add(Primitive::Circle(CircleElement {
                        cx,
                        cy,
                        r: r * 0.5,
                        fill: th.background.clone(),
                        ..Default::default()
                    }));
                }
            }
        }
        "scatter" | "line" => {
            for (si, s) in series.iter().enumerate() {
                let data = s.data.as_deref().unwrap_or_default();
                let fill = color(&s.color, &palette[si % palette.len()]);
                let step = pw / go_max(1.0, data.len() as f64 - 1.0);
                let mut pts = vec![];
                for (i, p) in data.iter().enumerate() {
                    let x = if c.r#type == "scatter" {
                        left + (p.x - xmin) / (xmax - xmin) * pw
                    } else {
                        left + i as f64 * step
                    };
                    let y = bottom - (p.y - min) / (max - min) * ph;
                    sc.add(Primitive::Circle(CircleElement {
                        cx: x,
                        cy: y,
                        r: if c.r#type == "scatter" { 5.0 } else { 4.0 },
                        fill: fill.into(),
                        stroke: th.background.clone(),
                        stroke_width: if c.r#type == "scatter" { 0.0 } else { 1.5 },
                        ..Default::default()
                    }));
                    pts.push(Point { x, y });
                    if c.r#type == "line" {
                        let label = c.x_axis.labels.get(i).unwrap_or(&p.label);
                        if !label.is_empty() {
                            text(
                                &mut sc,
                                x,
                                bottom + 18.0,
                                label,
                                11.0,
                                &th.text_muted,
                                "middle",
                            );
                        }
                    }
                }
                if c.r#type == "line" && !data.is_empty() {
                    sc.add(Primitive::Polyline(PolylineElement {
                        points: Some(pts),
                        fill: "none".into(),
                        stroke: fill.into(),
                        stroke_width: 2.5,
                        ..Default::default()
                    }));
                }
            }
        }
        _ => {
            let cats = series
                .iter()
                .map(|s| s.data.as_deref().unwrap_or_default().len())
                .max()
                .unwrap_or(0);
            if cats > 0 {
                let cw = pw / cats as f64;
                let pad = cw * 0.2;
                let bw = (cw - pad * 2.0) / series.len() as f64;
                let base = bottom - ((0.0 - min) / (max - min) * ph);
                for i in 0..cats {
                    for (si, s) in series.iter().enumerate() {
                        let Some(p) = s.data.as_deref().unwrap_or_default().get(i) else {
                            continue;
                        };
                        let fill = color(&p.color, color(&s.color, &palette[si % palette.len()]));
                        let y = bottom - (p.y - min) / (max - min) * ph;
                        sc.add(Primitive::Rect(RectElement {
                            x: left + i as f64 * cw + pad + si as f64 * bw,
                            y: go_min(y, base),
                            width: bw - 2.0,
                            height: (y - base).abs(),
                            rx: 3.0,
                            ry: 3.0,
                            fill: fill.into(),
                            ..Default::default()
                        }));
                        let label = c.x_axis.labels.get(i).unwrap_or(&p.label);
                        if si == 0 && !label.is_empty() {
                            text(
                                &mut sc,
                                left + i as f64 * cw + cw / 2.0,
                                bottom + 18.0,
                                label,
                                11.0,
                                &th.text_muted,
                                "middle",
                            );
                        }
                    }
                }
            }
        }
    }
    if c.legend {
        for (i, s) in series.iter().enumerate() {
            let x = right + 20.0;
            let y = top + 10.0 + i as f64 * 24.0;
            sc.add(Primitive::Rect(RectElement {
                x,
                y,
                width: 12.0,
                height: 12.0,
                rx: 2.0,
                ry: 2.0,
                fill: color(&s.color, &palette[i % palette.len()]).into(),
                ..Default::default()
            }));
            text(&mut sc, x + 18.0, y + 9.0, &s.name, 12.0, &th.text, "start");
        }
    }
    Ok(sc)
}
