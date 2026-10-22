//! SymDraw diagram IR (Go `internal/draw/ir`): model, validation and the
//! exact `encoding/json` serialization of the Go structs.

use std::collections::BTreeMap;

use super::gotext::{quote, trim_space};

/// Dynamically typed value of `Custom map[string]any`.
#[derive(Debug, Clone, PartialEq)]
pub enum Value {
    Null,
    Bool(bool),
    Num(f64),
    Int(i64),
    Str(String),
    Arr(Vec<Value>),
    /// `map[string]any`: keys serialize sorted.
    Map(BTreeMap<String, Value>),
    /// A Go struct without JSON tags: fields serialize in declaration order.
    Struct(Vec<(&'static str, Value)>),
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct NodeStyle {
    pub fill: String,
    pub stroke: String,
    pub stroke_width: Option<f64>,
    pub text_color: String,
    pub opacity: Option<f64>,
    pub dash_array: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct Node {
    pub id: String,
    pub label: String,
    pub shape: String,
    pub note: String,
    pub icon: String,
    pub style: NodeStyle,
    pub width: f64,
    pub height: f64,
    pub x: f64,
    pub y: f64,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct Edge {
    pub from: String,
    pub to: String,
    pub label: String,
    pub style: String,
    pub arrow: String,
    pub color: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct GroupStyle {
    pub fill: String,
    pub stroke: String,
    pub stroke_width: Option<f64>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct Group {
    pub id: String,
    pub label: String,
    /// `None` is a Go nil slice (serialized as `null`).
    pub members: Option<Vec<String>>,
    pub style: GroupStyle,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct DataPoint {
    pub x: f64,
    pub y: f64,
    pub label: String,
    pub color: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct Series {
    pub name: String,
    pub data: Option<Vec<DataPoint>>,
    pub color: String,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct AxisSpec {
    pub title: String,
    pub labels: Vec<String>,
    pub min: Option<f64>,
    pub max: Option<f64>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct ChartSpec {
    pub r#type: String,
    pub title: String,
    pub series: Option<Vec<Series>>,
    pub x_axis: AxisSpec,
    pub y_axis: AxisSpec,
    pub legend: bool,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct Diagram {
    pub version: String,
    pub kind: String,
    pub title: String,
    pub direction: String,
    pub theme: String,
    pub width: f64,
    pub height: f64,
    pub nodes: Vec<Node>,
    pub edges: Vec<Edge>,
    pub groups: Vec<Group>,
    pub chart: Option<ChartSpec>,
    pub custom: BTreeMap<String, Value>,
}

/// Go `ir.ValidationError`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ValidationError {
    pub field: String,
    pub message: String,
}

fn invalid(field: impl Into<String>, message: impl Into<String>) -> ValidationError {
    ValidationError {
        field: field.into(),
        message: message.into(),
    }
}

/// Go `ir.Validate`.
pub fn validate(d: &Diagram) -> Result<(), ValidationError> {
    match d.kind.as_str() {
        "graph" | "sequence" | "timeline" | "tree" | "chart" | "custom" => {}
        "" => return Err(invalid("kind", "diagram kind is required")),
        other => {
            return Err(invalid(
                "kind",
                format!("unsupported kind {}", quote(other)),
            ));
        }
    }
    if !d.direction.is_empty() && !matches!(d.direction.as_str(), "TD" | "TB" | "BT" | "LR" | "RL")
    {
        return Err(invalid(
            "direction",
            format!("unsupported direction {}", quote(&d.direction)),
        ));
    }

    let mut node_ids = std::collections::HashSet::with_capacity(d.nodes.len());
    for (i, node) in d.nodes.iter().enumerate() {
        let id = trim_space(&node.id);
        if id.is_empty() {
            return Err(invalid(format!("nodes[{i}].id"), "node id cannot be empty"));
        }
        if !node_ids.insert(id) {
            return Err(invalid(
                format!("nodes[{i}].id"),
                format!("duplicate node id {}", quote(id)),
            ));
        }
        if !node.shape.is_empty()
            && !matches!(
                node.shape.as_str(),
                "rect"
                    | "round"
                    | "circle"
                    | "cylinder"
                    | "diamond"
                    | "pill"
                    | "stadium"
                    | "subroutine"
                    | "hexagon"
                    | "asymmetric"
            )
        {
            return Err(invalid(
                format!("nodes[{i}].shape"),
                format!("unsupported shape {}", quote(&node.shape)),
            ));
        }
        if node.width < 0.0 || node.height < 0.0 {
            return Err(invalid(
                format!("nodes[{i}]"),
                "width and height cannot be negative",
            ));
        }
    }

    for (i, edge) in d.edges.iter().enumerate() {
        let from = trim_space(&edge.from);
        let to = trim_space(&edge.to);
        if from.is_empty() {
            return Err(invalid(
                format!("edges[{i}].from"),
                "edge from cannot be empty",
            ));
        }
        if to.is_empty() {
            return Err(invalid(format!("edges[{i}].to"), "edge to cannot be empty"));
        }
        if !node_ids.is_empty() {
            if !node_ids.contains(from) {
                return Err(invalid(
                    format!("edges[{i}].from"),
                    format!("referenced node {} does not exist", quote(from)),
                ));
            }
            if !node_ids.contains(to) {
                return Err(invalid(
                    format!("edges[{i}].to"),
                    format!("referenced node {} does not exist", quote(to)),
                ));
            }
        }
        if !edge.style.is_empty()
            && !matches!(edge.style.as_str(), "solid" | "dashed" | "dotted" | "thick")
        {
            return Err(invalid(
                format!("edges[{i}].style"),
                format!("unsupported edge style {}", quote(&edge.style)),
            ));
        }
        if !edge.arrow.is_empty()
            && !matches!(
                edge.arrow.as_str(),
                "none" | "single" | "double" | "cross" | "circle"
            )
        {
            return Err(invalid(
                format!("edges[{i}].arrow"),
                format!("unsupported arrow type {}", quote(&edge.arrow)),
            ));
        }
    }

    for (i, group) in d.groups.iter().enumerate() {
        let members = group.members.as_deref().unwrap_or(&[]);
        if members.is_empty() {
            return Err(invalid(
                format!("groups[{i}].members"),
                "group must have at least one member",
            ));
        }
        if !node_ids.is_empty() {
            for member in members {
                if !node_ids.contains(member.as_str()) {
                    return Err(invalid(
                        format!("groups[{i}].members"),
                        format!("grouped node {} does not exist", quote(member)),
                    ));
                }
            }
        }
    }

    if d.kind == "chart" || d.chart.is_some() {
        let Some(chart) = &d.chart else {
            return Err(invalid(
                "chart",
                "chart specification is required for chart diagram kind",
            ));
        };
        if !matches!(
            chart.r#type.as_str(),
            "bar" | "line" | "pie" | "scatter" | "area" | "donut"
        ) {
            return Err(invalid(
                "chart.type",
                format!("unsupported chart type {}", quote(&chart.r#type)),
            ));
        }
        if chart.series.as_deref().unwrap_or(&[]).is_empty() {
            return Err(invalid(
                "chart.series",
                "chart must have at least one series",
            ));
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// encoding/json encoder

impl Diagram {
    /// Exact bytes of Go `json.Marshal(*ir.Diagram)`.
    pub fn to_json(&self) -> String {
        let mut out = String::new();
        let mut o = ObjWriter::begin(&mut out);
        o.str_omit("version", &self.version);
        o.str_always("kind", &self.kind);
        o.str_omit("title", &self.title);
        o.str_omit("direction", &self.direction);
        o.str_omit("theme", &self.theme);
        o.f64_omit("width", self.width);
        o.f64_omit("height", self.height);
        o.list_omit("nodes", &self.nodes, write_node);
        o.list_omit("edges", &self.edges, write_edge);
        o.list_omit("groups", &self.groups, write_group);
        if let Some(chart) = &self.chart {
            o.field("chart", |out| write_chart(out, chart));
        }
        if !self.custom.is_empty() {
            o.field("custom", |out| write_map(out, &self.custom));
        }
        o.end();
        out
    }
}

struct ObjWriter<'a> {
    out: &'a mut String,
    first: bool,
}

impl<'a> ObjWriter<'a> {
    fn begin(out: &'a mut String) -> Self {
        out.push('{');
        Self { out, first: true }
    }

    fn end(self) {
        self.out.push('}');
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

    fn field(&mut self, key: &str, write: impl FnOnce(&mut String)) {
        self.key(key);
        write(self.out);
    }

    fn str_always(&mut self, key: &str, value: &str) {
        self.key(key);
        write_string(self.out, value);
    }

    fn str_omit(&mut self, key: &str, value: &str) {
        if !value.is_empty() {
            self.str_always(key, value);
        }
    }

    fn f64_always(&mut self, key: &str, value: f64) {
        self.key(key);
        write_f64(self.out, value);
    }

    fn f64_omit(&mut self, key: &str, value: f64) {
        if value != 0.0 {
            self.f64_always(key, value);
        }
    }

    fn f64_ptr_omit(&mut self, key: &str, value: Option<f64>) {
        if let Some(value) = value {
            self.f64_always(key, value);
        }
    }

    fn list_omit<T>(&mut self, key: &str, items: &[T], write: fn(&mut String, &T)) {
        if !items.is_empty() {
            self.key(key);
            write_list(self.out, items, write);
        }
    }
}

fn write_list<T>(out: &mut String, items: &[T], write: fn(&mut String, &T)) {
    out.push('[');
    for (i, item) in items.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        write(out, item);
    }
    out.push(']');
}

fn write_node(out: &mut String, n: &Node) {
    let mut o = ObjWriter::begin(out);
    o.str_always("id", &n.id);
    o.str_omit("label", &n.label);
    o.str_omit("shape", &n.shape);
    o.str_omit("note", &n.note);
    o.str_omit("icon", &n.icon);
    o.field("style", |out| {
        let mut s = ObjWriter::begin(out);
        s.str_omit("fill", &n.style.fill);
        s.str_omit("stroke", &n.style.stroke);
        s.f64_ptr_omit("stroke_width", n.style.stroke_width);
        s.str_omit("text_color", &n.style.text_color);
        s.f64_ptr_omit("opacity", n.style.opacity);
        s.str_omit("dash_array", &n.style.dash_array);
        s.end();
    });
    o.f64_omit("width", n.width);
    o.f64_omit("height", n.height);
    o.f64_omit("x", n.x);
    o.f64_omit("y", n.y);
    o.end();
}

fn write_edge(out: &mut String, e: &Edge) {
    let mut o = ObjWriter::begin(out);
    o.str_always("from", &e.from);
    o.str_always("to", &e.to);
    o.str_omit("label", &e.label);
    o.str_omit("style", &e.style);
    o.str_omit("arrow", &e.arrow);
    o.str_omit("color", &e.color);
    o.end();
}

fn write_group(out: &mut String, g: &Group) {
    let mut o = ObjWriter::begin(out);
    o.str_omit("id", &g.id);
    o.str_omit("label", &g.label);
    o.field("members", |out| match &g.members {
        None => out.push_str("null"),
        Some(members) => {
            out.push('[');
            for (i, member) in members.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_string(out, member);
            }
            out.push(']');
        }
    });
    o.field("style", |out| {
        let mut s = ObjWriter::begin(out);
        s.str_omit("fill", &g.style.fill);
        s.str_omit("stroke", &g.style.stroke);
        s.f64_ptr_omit("stroke_width", g.style.stroke_width);
        s.end();
    });
    o.end();
}

fn write_axis(out: &mut String, a: &AxisSpec) {
    let mut o = ObjWriter::begin(out);
    o.str_omit("title", &a.title);
    if !a.labels.is_empty() {
        o.field("labels", |out| {
            out.push('[');
            for (i, label) in a.labels.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_string(out, label);
            }
            out.push(']');
        });
    }
    o.f64_ptr_omit("min", a.min);
    o.f64_ptr_omit("max", a.max);
    o.end();
}

fn write_series(out: &mut String, s: &Series) {
    let mut o = ObjWriter::begin(out);
    o.str_always("name", &s.name);
    o.field("data", |out| match &s.data {
        None => out.push_str("null"),
        Some(points) => write_list(out, points, write_point),
    });
    o.str_omit("color", &s.color);
    o.end();
}

fn write_point(out: &mut String, p: &DataPoint) {
    let mut o = ObjWriter::begin(out);
    o.f64_omit("x", p.x);
    o.f64_always("y", p.y);
    o.str_omit("label", &p.label);
    o.str_omit("color", &p.color);
    o.end();
}

fn write_chart(out: &mut String, c: &ChartSpec) {
    let mut o = ObjWriter::begin(out);
    o.str_always("type", &c.r#type);
    o.str_omit("title", &c.title);
    o.field("series", |out| match &c.series {
        None => out.push_str("null"),
        Some(series) => write_list(out, series, write_series),
    });
    o.field("x_axis", |out| write_axis(out, &c.x_axis));
    o.field("y_axis", |out| write_axis(out, &c.y_axis));
    if c.legend {
        o.field("legend", |out| out.push_str("true"));
    }
    o.end();
}

fn write_map(out: &mut String, map: &BTreeMap<String, Value>) {
    out.push('{');
    for (i, (key, value)) in map.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        write_string(out, key);
        out.push(':');
        write_value(out, value);
    }
    out.push('}');
}

fn write_value(out: &mut String, value: &Value) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(true) => out.push_str("true"),
        Value::Bool(false) => out.push_str("false"),
        Value::Num(n) => write_f64(out, *n),
        Value::Int(n) => out.push_str(&n.to_string()),
        Value::Str(s) => write_string(out, s),
        Value::Arr(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_value(out, item);
            }
            out.push(']');
        }
        Value::Map(map) => write_map(out, map),
        Value::Struct(fields) => {
            out.push('{');
            for (i, (name, value)) in fields.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_string(out, name);
                out.push(':');
                write_value(out, value);
            }
            out.push('}');
        }
    }
}

/// Go `encodeState.string` with `escapeHTML = true`.
fn write_string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' | '>' | '&' => out.push_str(&format!("\\u{:04x}", c as u32)),
            '\u{2028}' | '\u{2029}' => out.push_str(&format!("\\u{:04x}", c as u32)),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// Go `floatEncoder` for float64.
fn write_f64(out: &mut String, value: f64) {
    let abs = value.abs();
    if value.is_sign_negative() {
        out.push('-');
    }
    if abs == 0.0 {
        out.push('0');
        return;
    }
    // Shortest round-trip digits and decimal exponent: d.ddd e X.
    let sci = format!("{abs:e}");
    let (mantissa, exp) = sci.split_once('e').unwrap_or((&sci, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    let digits: String = mantissa.chars().filter(|c| *c != '.').collect();
    if !(1e-6..1e21).contains(&abs) {
        out.push_str(&digits[..1]);
        if digits.len() > 1 {
            out.push('.');
            out.push_str(&digits[1..]);
        }
        out.push('e');
        out.push(if exp < 0 { '-' } else { '+' });
        let magnitude = exp.abs();
        if magnitude < 10 && exp >= 0 {
            out.push('0');
        }
        out.push_str(&magnitude.to_string());
        return;
    }
    let point = exp + 1; // digits before the decimal point
    if point <= 0 {
        out.push_str("0.");
        for _ in 0..(-point) {
            out.push('0');
        }
        out.push_str(&digits);
    } else if point as usize >= digits.len() {
        out.push_str(&digits);
        for _ in 0..(point as usize - digits.len()) {
            out.push('0');
        }
    } else {
        out.push_str(&digits[..point as usize]);
        out.push('.');
        out.push_str(&digits[point as usize..]);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn num(v: f64) -> String {
        let mut s = String::new();
        write_f64(&mut s, v);
        s
    }

    #[test]
    fn float_formatting_matches_go() {
        assert_eq!(num(1500.0), "1500");
        assert_eq!(num(0.1), "0.1");
        assert_eq!(num(1e21), "1e+21");
        assert_eq!(num(1e-7), "1e-7");
        assert_eq!(num(0.000001), "0.000001");
        assert_eq!(num(1.5e300), "1.5e+300");
        assert_eq!(
            num(123456789012345678901234567890.0),
            "1.2345678901234568e+29"
        );
        assert_eq!(num(100000000000000000000.0), "100000000000000000000");
        assert_eq!(num(-0.0), "-0");
        assert_eq!(num(4.9e-324), "5e-324");
        assert_eq!(num(5e-7), "5e-7");
        assert_eq!(num(12.5), "12.5");
    }
}
