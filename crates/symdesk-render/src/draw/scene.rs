//! Port of Go `internal/draw/scene`: the positioned scene graph, its
//! primitives, bounding-box arithmetic and `json.Marshal` shapes.
//!
//! Every Go behavior that is observable is reproduced bit for bit:
//!
//! * `math.Min`/`math.Max` propagate NaN and order `-0` before `+0`
//!   ([`go_min`], [`go_max`]); plain `<`/`>` comparisons stay plain.
//! * `json.Marshal` fails on NaN/±Inf with `json: unsupported value: …`
//!   ([`JsonResult`]), omits zero values behind `omitempty` (`-0` is zero),
//!   and distinguishes nil from empty slices (`Option<Vec<_>>` where Go
//!   serializes a nil slice as `null`).
//! * `Scene.Validate` formats floats with `%v` ([`go_fmt_v`]).
//!
//! Declared limits: a Go `Primitive` interface value can be nil (skipped by
//! `Scene.Add`, a `null` JSON element, a panic in `Group.Bounds`); a Rust
//! [`Primitive`] cannot be, so those states are not representable and
//! `Scene::add` takes only real primitives. Strings are valid UTF-8.
//!
//! The one host-dependent Go expression is the alphabetic-baseline text
//! offset `t.Y - fontSize*0.95` (arm64 may fuse it into an FMA). This port
//! rounds the product first, like Go on amd64.

use std::collections::BTreeMap;
use std::sync::OnceLock;

use super::ir::{write_f64, write_string};
use super::measure::{FontWeight, Measurer};
use super::theme::{self, Theme};

/// Result of a Go `json.Marshal`; the error text is Go's
/// `json: unsupported value: …`.
pub type JsonResult = Result<String, String>;

/// Go `math.Min`: `-Inf` wins over NaN, otherwise NaN wins; `-0` is smaller
/// than `+0`.
pub fn go_min(x: f64, y: f64) -> f64 {
    if x == f64::NEG_INFINITY || y == f64::NEG_INFINITY {
        return f64::NEG_INFINITY;
    }
    if x.is_nan() || y.is_nan() {
        return f64::NAN;
    }
    if x == 0.0 && y == 0.0 {
        return if x.is_sign_negative() { x } else { y };
    }
    if x < y { x } else { y }
}

/// Go `math.Max`: `+Inf` wins over NaN, otherwise NaN wins; `+0` is larger
/// than `-0`.
pub fn go_max(x: f64, y: f64) -> f64 {
    if x == f64::INFINITY || y == f64::INFINITY {
        return f64::INFINITY;
    }
    if x.is_nan() || y.is_nan() {
        return f64::NAN;
    }
    if x == 0.0 && y == 0.0 {
        return if x.is_sign_negative() { y } else { x };
    }
    if x > y { x } else { y }
}

/// Go `fmt` `%v` of a float64: `%g` with the shortest round-trip digits,
/// switching to exponent form when the decimal exponent is below -4 or at
/// least 6 (`strconv` `%g` with shortest precision; verified against Go:
/// `100000` stays plain, `1e+06` and `1.234567e+06` do not).
pub fn go_fmt_v(value: f64) -> String {
    if value.is_nan() {
        return "NaN".to_owned();
    }
    if value.is_infinite() {
        return if value > 0.0 { "+Inf" } else { "-Inf" }.to_owned();
    }
    if value == 0.0 {
        return if value.is_sign_negative() { "-0" } else { "0" }.to_owned();
    }
    let sign = if value < 0.0 { "-" } else { "" };
    let sci = format!("{:e}", value.abs());
    let (mantissa, exp) = sci.split_once('e').unwrap_or((&sci, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    let digits: String = mantissa.chars().filter(|c| *c != '.').collect();
    if !(-4..6).contains(&exp) {
        let mut out = String::from(sign);
        out.push_str(&digits[..1]);
        if digits.len() > 1 {
            out.push('.');
            out.push_str(&digits[1..]);
        }
        out.push('e');
        out.push(if exp < 0 { '-' } else { '+' });
        if exp.abs() < 10 {
            out.push('0');
        }
        out.push_str(&exp.abs().to_string());
        return out;
    }
    let mut out = String::from(sign);
    if exp < 0 {
        out.push_str("0.");
        for _ in 0..(-exp - 1) {
            out.push('0');
        }
        out.push_str(&digits);
    } else {
        let point = usize::try_from(exp).unwrap_or(0) + 1;
        if digits.len() <= point {
            out.push_str(&digits);
            for _ in 0..(point - digits.len()) {
                out.push('0');
            }
        } else {
            out.push_str(&digits[..point]);
            out.push('.');
            out.push_str(&digits[point..]);
        }
    }
    out
}

fn unsupported(value: f64) -> String {
    let text = if value.is_nan() {
        "NaN"
    } else if value > 0.0 {
        "+Inf"
    } else {
        "-Inf"
    };
    format!("json: unsupported value: {text}")
}

/// A `json.Marshal` struct encoder: tracks the comma state and stops at the
/// first unsupported value, like Go's encoder.
struct Obj {
    out: String,
    first: bool,
}

impl Obj {
    fn new() -> Obj {
        Obj {
            out: String::from("{"),
            first: true,
        }
    }

    fn key(&mut self, key: &str) {
        if !self.first {
            self.out.push(',');
        }
        self.first = false;
        self.out.push('"');
        self.out.push_str(key);
        self.out.push_str("\":");
    }

    /// A float without `omitempty`.
    fn float(&mut self, key: &str, value: f64) -> Result<(), String> {
        self.key(key);
        float_value(&mut self.out, value)
    }

    /// A float with `omitempty` (`0` and `-0` are empty; NaN is not).
    fn float_omit(&mut self, key: &str, value: f64) -> Result<(), String> {
        if value == 0.0 {
            return Ok(());
        }
        self.float(key, value)
    }

    fn string(&mut self, key: &str, value: &str) {
        self.key(key);
        write_string(&mut self.out, value);
    }

    fn string_omit(&mut self, key: &str, value: &str) {
        if !value.is_empty() {
            self.string(key, value);
        }
    }

    fn raw(&mut self, key: &str, value: &str) {
        self.key(key);
        self.out.push_str(value);
    }

    fn finish(mut self) -> String {
        self.out.push('}');
        self.out
    }
}

fn float_value(out: &mut String, value: f64) -> Result<(), String> {
    if !value.is_finite() {
        return Err(unsupported(value));
    }
    write_f64(out, value);
    Ok(())
}

/// Go `scene.Rect`.
#[derive(Debug, Clone, Copy, PartialEq, Default)]
pub struct Rect {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

impl Rect {
    pub fn max_x(&self) -> f64 {
        self.x + self.width
    }

    pub fn max_y(&self) -> f64 {
        self.y + self.height
    }

    /// Go `Rect.IsEmpty`: NaN extents are not empty.
    pub fn is_empty(&self) -> bool {
        self.width <= 0.0 || self.height <= 0.0
    }

    /// Go `Rect.Union`.
    pub fn union(&self, other: Rect) -> Rect {
        if self.is_empty() {
            return other;
        }
        if other.is_empty() {
            return *self;
        }
        let min_x = go_min(self.x, other.x);
        let min_y = go_min(self.y, other.y);
        let max_x = go_max(self.max_x(), other.max_x());
        let max_y = go_max(self.max_y(), other.max_y());
        Rect {
            x: min_x,
            y: min_y,
            width: max_x - min_x,
            height: max_y - min_y,
        }
    }

    /// Go `Rect.Contains` (epsilon 0.01).
    pub fn contains(&self, other: Rect) -> bool {
        if other.is_empty() {
            return true;
        }
        if self.is_empty() {
            return false;
        }
        let eps = 0.01;
        other.x >= self.x - eps
            && other.y >= self.y - eps
            && other.max_x() <= self.max_x() + eps
            && other.max_y() <= self.max_y() + eps
    }

    /// Go `Rect.Pad`.
    pub fn pad(&self, padding: f64) -> Rect {
        if self.is_empty() {
            return Rect {
                x: -padding,
                y: -padding,
                width: padding * 2.0,
                height: padding * 2.0,
            };
        }
        Rect {
            x: self.x - padding,
            y: self.y - padding,
            width: self.width + padding * 2.0,
            height: self.height + padding * 2.0,
        }
    }

    /// Go `json.Marshal(Rect)`.
    pub fn to_json(&self) -> JsonResult {
        let mut obj = Obj::new();
        obj.float("x", self.x)?;
        obj.float("y", self.y)?;
        obj.float("width", self.width)?;
        obj.float("height", self.height)?;
        Ok(obj.finish())
    }

    /// Go `fmt` `%+v` of a `Rect`.
    pub fn go_debug(&self) -> String {
        format!(
            "{{X:{} Y:{} Width:{} Height:{}}}",
            go_fmt_v(self.x),
            go_fmt_v(self.y),
            go_fmt_v(self.width),
            go_fmt_v(self.height)
        )
    }
}

/// Go `measure.Point`.
#[derive(Debug, Clone, Copy, PartialEq, Default)]
pub struct Point {
    pub x: f64,
    pub y: f64,
}

fn point_json(out: &mut String, point: Point) -> Result<(), String> {
    out.push_str("{\"x\":");
    float_value(out, point.x)?;
    out.push_str(",\"y\":");
    float_value(out, point.y)?;
    out.push('}');
    Ok(())
}

/// Go `measure.PathSegment` (`op` is the raw Go string).
#[derive(Debug, Clone, PartialEq, Default)]
pub struct PathSegment {
    pub op: String,
    pub args: Vec<Point>,
}

fn points_json(points: &Option<Vec<Point>>) -> JsonResult {
    let Some(points) = points else {
        return Ok("null".to_owned());
    };
    let mut out = String::from("[");
    for (index, point) in points.iter().enumerate() {
        if index > 0 {
            out.push(',');
        }
        point_json(&mut out, *point)?;
    }
    out.push(']');
    Ok(out)
}

/// Bounding box of points as Go's `Polyline`/`Polygon` compute it, with the
/// stroke added: plain comparisons, so a NaN after the first point is ignored.
fn points_bounds(points: &[Point], stroke_width: f64) -> Rect {
    let Some(first) = points.first() else {
        return Rect::default();
    };
    let (mut min_x, mut min_y) = (first.x, first.y);
    let (mut max_x, mut max_y) = (min_x, min_y);
    for point in &points[1..] {
        if point.x < min_x {
            min_x = point.x;
        }
        if point.x > max_x {
            max_x = point.x;
        }
        if point.y < min_y {
            min_y = point.y;
        }
        if point.y > max_y {
            max_y = point.y;
        }
    }
    let sw = stroke_width / 2.0;
    Rect {
        x: min_x - sw,
        y: min_y - sw,
        width: (max_x - min_x) + stroke_width,
        height: (max_y - min_y) + stroke_width,
    }
}

/// Go `scene.RectElement`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct RectElement {
    pub id: String,
    pub class: String,
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
    pub rx: f64,
    pub ry: f64,
    pub fill: String,
    pub stroke: String,
    pub stroke_width: f64,
    pub dash_array: String,
    pub opacity: f64,
    pub link: String,
}

/// Go `scene.CircleElement`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct CircleElement {
    pub id: String,
    pub class: String,
    pub cx: f64,
    pub cy: f64,
    pub r: f64,
    pub fill: String,
    pub stroke: String,
    pub stroke_width: f64,
    pub opacity: f64,
    pub link: String,
}

/// Go `scene.EllipseElement`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct EllipseElement {
    pub id: String,
    pub class: String,
    pub cx: f64,
    pub cy: f64,
    pub rx: f64,
    pub ry: f64,
    pub fill: String,
    pub stroke: String,
    pub stroke_width: f64,
    pub opacity: f64,
    pub link: String,
}

/// Go `scene.LineElement`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct LineElement {
    pub id: String,
    pub class: String,
    pub x1: f64,
    pub y1: f64,
    pub x2: f64,
    pub y2: f64,
    pub stroke: String,
    pub stroke_width: f64,
    pub dash_array: String,
    pub marker_start: String,
    pub marker_end: String,
    pub opacity: f64,
}

/// Go `scene.PolylineElement`; `points` is `None` for a nil slice.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct PolylineElement {
    pub id: String,
    pub class: String,
    pub points: Option<Vec<Point>>,
    pub fill: String,
    pub stroke: String,
    pub stroke_width: f64,
    pub dash_array: String,
    pub marker_end: String,
    pub opacity: f64,
}

/// Go `scene.PolygonElement`; `points` is `None` for a nil slice.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct PolygonElement {
    pub id: String,
    pub class: String,
    pub points: Option<Vec<Point>>,
    pub fill: String,
    pub stroke: String,
    pub stroke_width: f64,
    pub dash_array: String,
    pub opacity: f64,
}

/// Go `scene.PathElement`; `bounds_hint` is the unexported `boundsHint`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct PathElement {
    pub id: String,
    pub class: String,
    pub d: String,
    pub segments: Vec<PathSegment>,
    pub fill: String,
    pub stroke: String,
    pub stroke_width: f64,
    pub dash_array: String,
    pub marker_end: String,
    pub opacity: f64,
    pub link: String,
    pub bounds_hint: Option<Rect>,
}

/// Go `scene.TextElement`; `font_weight`, `anchor` and `baseline` keep the
/// raw Go string (any value is accepted and serialized).
#[derive(Debug, Clone, PartialEq, Default)]
pub struct TextElement {
    pub id: String,
    pub class: String,
    pub x: f64,
    pub y: f64,
    pub text: String,
    pub font_size: f64,
    pub font_family: String,
    pub font_weight: String,
    pub fill: String,
    pub anchor: String,
    pub baseline: String,
    pub text_length: f64,
    pub opacity: f64,
    pub link: String,
}

/// Go `scene.GroupElement`; `children` is `None` for a nil slice.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct GroupElement {
    pub id: String,
    pub class: String,
    pub transform: String,
    pub children: Option<Vec<Primitive>>,
    pub link: String,
}

/// The Go `Primitive` interface as a closed enum.
#[derive(Debug, Clone, PartialEq)]
pub enum Primitive {
    Rect(RectElement),
    Circle(CircleElement),
    Ellipse(EllipseElement),
    Line(LineElement),
    Polyline(PolylineElement),
    Polygon(PolygonElement),
    Path(PathElement),
    Text(TextElement),
    Group(GroupElement),
}

pub(super) fn default_measurer() -> &'static Measurer {
    static DEFAULT: OnceLock<Measurer> = OnceLock::new();
    DEFAULT.get_or_init(|| Measurer::new().expect("embedded Inter fonts must load"))
}

fn text_bounds(t: &TextElement) -> Rect {
    let mut font_size = t.font_size;
    if font_size <= 0.0 {
        font_size = 14.0;
    }
    let mut w = t.text_length;
    if w <= 0.0 {
        w = default_measurer()
            .measure_text(&t.text, font_size, FontWeight::from_go(&t.font_weight))
            .0;
    }
    let h = font_size * 1.2;
    let ascent = font_size * 0.95;
    let x = match t.anchor.as_str() {
        "middle" => t.x - w / 2.0,
        "end" => t.x - w,
        _ => t.x,
    };
    let y = match t.baseline.as_str() {
        "middle" | "central" => t.y - h / 2.0,
        "hanging" | "top" => t.y,
        _ => t.y - ascent,
    };
    Rect {
        x,
        y,
        width: w,
        height: h,
    }
}

fn path_bounds(p: &PathElement) -> Rect {
    if let Some(hint) = p.bounds_hint {
        return hint;
    }
    if p.segments.is_empty() {
        return Rect::default();
    }
    let (mut min_x, mut min_y, mut max_x, mut max_y) = (0.0, 0.0, 0.0, 0.0);
    let mut first = true;
    for segment in &p.segments {
        for point in &segment.args {
            if first {
                (min_x, max_x) = (point.x, point.x);
                (min_y, max_y) = (point.y, point.y);
                first = false;
            } else {
                if point.x < min_x {
                    min_x = point.x;
                }
                if point.x > max_x {
                    max_x = point.x;
                }
                if point.y < min_y {
                    min_y = point.y;
                }
                if point.y > max_y {
                    max_y = point.y;
                }
            }
        }
    }
    let sw = p.stroke_width / 2.0;
    Rect {
        x: min_x - sw,
        y: min_y - sw,
        width: (max_x - min_x) + p.stroke_width,
        height: (max_y - min_y) + p.stroke_width,
    }
}

impl Primitive {
    /// Go `Primitive.Type`.
    pub fn type_name(&self) -> &'static str {
        match self {
            Primitive::Rect(_) => "rect",
            Primitive::Circle(_) => "circle",
            Primitive::Ellipse(_) => "ellipse",
            Primitive::Line(_) => "line",
            Primitive::Polyline(_) => "polyline",
            Primitive::Polygon(_) => "polygon",
            Primitive::Path(_) => "path",
            Primitive::Text(_) => "text",
            Primitive::Group(_) => "group",
        }
    }

    /// Go `Primitive.ElementID`.
    pub fn element_id(&self) -> &str {
        match self {
            Primitive::Rect(p) => &p.id,
            Primitive::Circle(p) => &p.id,
            Primitive::Ellipse(p) => &p.id,
            Primitive::Line(p) => &p.id,
            Primitive::Polyline(p) => &p.id,
            Primitive::Polygon(p) => &p.id,
            Primitive::Path(p) => &p.id,
            Primitive::Text(p) => &p.id,
            Primitive::Group(p) => &p.id,
        }
    }

    /// Go `Primitive.Bounds`.
    pub fn bounds(&self) -> Rect {
        match self {
            Primitive::Rect(r) => {
                let sw = r.stroke_width / 2.0;
                Rect {
                    x: r.x - sw,
                    y: r.y - sw,
                    width: r.width + r.stroke_width,
                    height: r.height + r.stroke_width,
                }
            }
            Primitive::Circle(c) => {
                let sw = c.stroke_width / 2.0;
                let r = c.r + sw;
                Rect {
                    x: c.cx - r,
                    y: c.cy - r,
                    width: r * 2.0,
                    height: r * 2.0,
                }
            }
            Primitive::Ellipse(e) => {
                let sw = e.stroke_width / 2.0;
                let rx = e.rx + sw;
                let ry = e.ry + sw;
                Rect {
                    x: e.cx - rx,
                    y: e.cy - ry,
                    width: rx * 2.0,
                    height: ry * 2.0,
                }
            }
            Primitive::Line(l) => {
                let min_x = go_min(l.x1, l.x2);
                let min_y = go_min(l.y1, l.y2);
                let max_x = go_max(l.x1, l.x2);
                let max_y = go_max(l.y1, l.y2);
                let sw = l.stroke_width / 2.0;
                Rect {
                    x: min_x - sw,
                    y: min_y - sw,
                    width: (max_x - min_x) + l.stroke_width,
                    height: (max_y - min_y) + l.stroke_width,
                }
            }
            Primitive::Polyline(p) => {
                points_bounds(p.points.as_deref().unwrap_or(&[]), p.stroke_width)
            }
            Primitive::Polygon(p) => {
                points_bounds(p.points.as_deref().unwrap_or(&[]), p.stroke_width)
            }
            Primitive::Path(p) => path_bounds(p),
            Primitive::Text(t) => text_bounds(t),
            Primitive::Group(g) => {
                let children = g.children.as_deref().unwrap_or(&[]);
                let Some((first, rest)) = children.split_first() else {
                    return Rect::default();
                };
                rest.iter()
                    .fold(first.bounds(), |acc, child| acc.union(child.bounds()))
            }
        }
    }

    /// Go `json.Marshal` of the concrete primitive.
    pub fn to_json(&self) -> JsonResult {
        let mut o = Obj::new();
        match self {
            Primitive::Rect(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.float("x", p.x)?;
                o.float("y", p.y)?;
                o.float("width", p.width)?;
                o.float("height", p.height)?;
                o.float_omit("rx", p.rx)?;
                o.float_omit("ry", p.ry)?;
                o.string_omit("fill", &p.fill);
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.string_omit("dash_array", &p.dash_array);
                o.float_omit("opacity", p.opacity)?;
                o.string_omit("link", &p.link);
            }
            Primitive::Circle(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.float("cx", p.cx)?;
                o.float("cy", p.cy)?;
                o.float("r", p.r)?;
                o.string_omit("fill", &p.fill);
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.float_omit("opacity", p.opacity)?;
                o.string_omit("link", &p.link);
            }
            Primitive::Ellipse(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.float("cx", p.cx)?;
                o.float("cy", p.cy)?;
                o.float("rx", p.rx)?;
                o.float("ry", p.ry)?;
                o.string_omit("fill", &p.fill);
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.float_omit("opacity", p.opacity)?;
                o.string_omit("link", &p.link);
            }
            Primitive::Line(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.float("x1", p.x1)?;
                o.float("y1", p.y1)?;
                o.float("x2", p.x2)?;
                o.float("y2", p.y2)?;
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.string_omit("dash_array", &p.dash_array);
                o.string_omit("marker_start", &p.marker_start);
                o.string_omit("marker_end", &p.marker_end);
                o.float_omit("opacity", p.opacity)?;
            }
            Primitive::Polyline(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.raw("points", &points_json(&p.points)?);
                o.string_omit("fill", &p.fill);
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.string_omit("dash_array", &p.dash_array);
                o.string_omit("marker_end", &p.marker_end);
                o.float_omit("opacity", p.opacity)?;
            }
            Primitive::Polygon(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.raw("points", &points_json(&p.points)?);
                o.string_omit("fill", &p.fill);
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.string_omit("dash_array", &p.dash_array);
                o.float_omit("opacity", p.opacity)?;
            }
            Primitive::Path(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.string_omit("d", &p.d);
                if !p.segments.is_empty() {
                    o.raw("segments", &segments_json(&p.segments)?);
                }
                o.string_omit("fill", &p.fill);
                o.string_omit("stroke", &p.stroke);
                o.float_omit("stroke_width", p.stroke_width)?;
                o.string_omit("dash_array", &p.dash_array);
                o.string_omit("marker_end", &p.marker_end);
                o.float_omit("opacity", p.opacity)?;
                o.string_omit("link", &p.link);
            }
            Primitive::Text(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.float("x", p.x)?;
                o.float("y", p.y)?;
                o.string("text", &p.text);
                o.float("font_size", p.font_size)?;
                o.string_omit("font_family", &p.font_family);
                o.string_omit("font_weight", &p.font_weight);
                o.string_omit("fill", &p.fill);
                o.string_omit("anchor", &p.anchor);
                o.string_omit("baseline", &p.baseline);
                o.float_omit("text_length", p.text_length)?;
                o.float_omit("opacity", p.opacity)?;
                o.string_omit("link", &p.link);
            }
            Primitive::Group(p) => {
                o.string_omit("id", &p.id);
                o.string_omit("class", &p.class);
                o.string_omit("transform", &p.transform);
                match &p.children {
                    None => o.raw("children", "null"),
                    Some(children) => {
                        let mut list = String::from("[");
                        for (index, child) in children.iter().enumerate() {
                            if index > 0 {
                                list.push(',');
                            }
                            list.push_str(&child.to_json()?);
                        }
                        list.push(']');
                        o.raw("children", &list);
                    }
                }
                o.string_omit("link", &p.link);
            }
        }
        Ok(o.finish())
    }
}

fn segments_json(segments: &[PathSegment]) -> JsonResult {
    let mut out = String::from("[");
    for (index, segment) in segments.iter().enumerate() {
        if index > 0 {
            out.push(',');
        }
        out.push_str("{\"op\":");
        write_string(&mut out, &segment.op);
        if !segment.args.is_empty() {
            out.push_str(",\"args\":[");
            for (arg_index, point) in segment.args.iter().enumerate() {
                if arg_index > 0 {
                    out.push(',');
                }
                point_json(&mut out, *point)?;
            }
            out.push(']');
        }
        out.push('}');
    }
    out.push(']');
    Ok(out)
}

/// Go `scene.MarkerDef`; `path` is `None` for a nil interface (`null`).
#[derive(Debug, Clone, PartialEq, Default)]
pub struct MarkerDef {
    pub id: String,
    pub view_box: Rect,
    pub ref_x: f64,
    pub ref_y: f64,
    pub marker_width: f64,
    pub marker_height: f64,
    pub orient: String,
    pub path: Option<Primitive>,
}

impl MarkerDef {
    /// Go `json.Marshal(MarkerDef)`.
    pub fn to_json(&self) -> JsonResult {
        let mut o = Obj::new();
        o.string("id", &self.id);
        o.raw("view_box", &self.view_box.to_json()?);
        o.float("ref_x", self.ref_x)?;
        o.float("ref_y", self.ref_y)?;
        o.float("marker_width", self.marker_width)?;
        o.float("marker_height", self.marker_height)?;
        o.string("orient", &self.orient);
        match &self.path {
            None => o.raw("path", "null"),
            Some(path) => o.raw("path", &path.to_json()?),
        }
        Ok(o.finish())
    }
}

/// Go `scene.Scene`. An empty `primitives` is Go's nil slice (`null`).
#[derive(Debug, Clone, PartialEq)]
pub struct Scene {
    pub width: f64,
    pub height: f64,
    pub view_box: Rect,
    pub theme: Theme,
    pub background: String,
    pub metadata: BTreeMap<String, String>,
    pub markers: Vec<MarkerDef>,
    pub primitives: Vec<Primitive>,
}

impl Scene {
    /// Go `scene.NewScene`: an unnamed theme becomes `symaira-dark`.
    pub fn new(width: f64, height: f64, th: &Theme) -> Scene {
        let th = if th.name.is_empty() {
            theme::resolve("symaira-dark")
        } else {
            th.clone()
        };
        Scene {
            width,
            height,
            view_box: Rect {
                x: 0.0,
                y: 0.0,
                width,
                height,
            },
            background: th.background.clone(),
            markers: default_markers(&th),
            theme: th,
            metadata: BTreeMap::new(),
            primitives: Vec::new(),
        }
    }

    /// Go `Scene.Add` for a non-nil primitive.
    pub fn add(&mut self, primitive: Primitive) {
        self.primitives.push(primitive);
    }

    /// Go `Scene.AddMarker`.
    pub fn add_marker(&mut self, marker: MarkerDef) {
        self.markers.push(marker);
    }

    fn fallback_bounds(&self) -> Rect {
        Rect {
            x: 0.0,
            y: 0.0,
            width: self.width,
            height: self.height,
        }
    }

    /// Go `Scene.ComputeBounds`.
    pub fn compute_bounds(&self) -> Rect {
        let mut bounds: Option<Rect> = None;
        for primitive in &self.primitives {
            let pb = primitive.bounds();
            if pb.is_empty() {
                continue;
            }
            bounds = Some(match bounds {
                None => pb,
                Some(b) => b.union(pb),
            });
        }
        bounds.unwrap_or_else(|| self.fallback_bounds())
    }

    /// Go `Scene.FitToBounds`.
    pub fn fit_to_bounds(&mut self, padding: f64) {
        let mut padded = self.compute_bounds().pad(padding);
        if padded.width <= 0.0 {
            padded.width = 100.0;
        }
        if padded.height <= 0.0 {
            padded.height = 100.0;
        }
        self.view_box = padded;
        self.width = padded.width;
        self.height = padded.height;
    }

    /// Go `Scene.Validate`; the error is the exact Go message.
    pub fn validate(&self) -> Result<(), String> {
        if !self.width.is_finite() || self.width < 0.0 {
            return Err(format!("invalid scene width: {}", go_fmt_v(self.width)));
        }
        if !self.height.is_finite() || self.height < 0.0 {
            return Err(format!("invalid scene height: {}", go_fmt_v(self.height)));
        }
        if self.view_box.width < 0.0 || self.view_box.height < 0.0 {
            return Err("scene viewBox has negative dimensions".to_owned());
        }
        for (index, primitive) in self.primitives.iter().enumerate() {
            let pb = primitive.bounds();
            if pb.x.is_nan() || pb.y.is_nan() || pb.width.is_nan() || pb.height.is_nan() {
                return Err(format!(
                    "scene primitive [{index}] ({}) has NaN bounds: {}",
                    primitive.type_name(),
                    pb.go_debug()
                ));
            }
        }
        Ok(())
    }

    /// Go `json.Marshal(*Scene)`.
    pub fn to_json(&self) -> JsonResult {
        let mut o = Obj::new();
        o.float("width", self.width)?;
        o.float("height", self.height)?;
        o.raw("view_box", &self.view_box.to_json()?);
        o.raw("theme", &self.theme.to_json());
        o.string_omit("background", &self.background);
        if !self.metadata.is_empty() {
            let mut map = String::from("{");
            for (index, (key, value)) in self.metadata.iter().enumerate() {
                if index > 0 {
                    map.push(',');
                }
                write_string(&mut map, key);
                map.push(':');
                write_string(&mut map, value);
            }
            map.push('}');
            o.raw("metadata", &map);
        }
        if !self.markers.is_empty() {
            o.raw("markers", &markers_json(&self.markers)?);
        }
        if self.primitives.is_empty() {
            o.raw("primitives", "null");
        } else {
            let mut list = String::from("[");
            for (index, primitive) in self.primitives.iter().enumerate() {
                if index > 0 {
                    list.push(',');
                }
                list.push_str(&primitive.to_json()?);
            }
            list.push(']');
            o.raw("primitives", &list);
        }
        Ok(o.finish())
    }
}

/// `json.Marshal([]MarkerDef)`.
pub fn markers_json(markers: &[MarkerDef]) -> JsonResult {
    let mut out = String::from("[");
    for (index, marker) in markers.iter().enumerate() {
        if index > 0 {
            out.push(',');
        }
        out.push_str(&marker.to_json()?);
    }
    out.push(']');
    Ok(out)
}

/// Go `scene.DefaultMarkers`.
pub fn default_markers(th: &Theme) -> Vec<MarkerDef> {
    let edge = if th.edge.is_empty() {
        "#B8B4A8"
    } else {
        &th.edge
    };
    let primary = if th.primary.is_empty() {
        "#E5C397"
    } else {
        &th.primary
    };
    let unit_box = Rect {
        x: 0.0,
        y: 0.0,
        width: 10.0,
        height: 10.0,
    };
    let triangle = |points: [(f64, f64); 3]| {
        Some(Primitive::Polygon(PolygonElement {
            points: Some(points.iter().map(|&(x, y)| Point { x, y }).collect()),
            fill: edge.to_owned(),
            ..PolygonElement::default()
        }))
    };
    vec![
        MarkerDef {
            id: "arrow-end".to_owned(),
            view_box: unit_box,
            ref_x: 9.0,
            ref_y: 5.0,
            marker_width: 8.0,
            marker_height: 8.0,
            orient: "auto".to_owned(),
            path: triangle([(0.0, 1.5), (9.0, 5.0), (0.0, 8.5)]),
        },
        MarkerDef {
            id: "arrow-start".to_owned(),
            view_box: unit_box,
            ref_x: 1.0,
            ref_y: 5.0,
            marker_width: 8.0,
            marker_height: 8.0,
            orient: "auto".to_owned(),
            path: triangle([(10.0, 1.5), (1.0, 5.0), (10.0, 8.5)]),
        },
        MarkerDef {
            id: "dot-end".to_owned(),
            view_box: unit_box,
            ref_x: 5.0,
            ref_y: 5.0,
            marker_width: 6.0,
            marker_height: 6.0,
            orient: "auto".to_owned(),
            path: Some(Primitive::Circle(CircleElement {
                cx: 5.0,
                cy: 5.0,
                r: 4.0,
                fill: primary.to_owned(),
                ..CircleElement::default()
            })),
        },
    ]
}
