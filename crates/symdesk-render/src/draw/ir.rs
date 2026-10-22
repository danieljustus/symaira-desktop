//! SymDraw diagram IR (Go `internal/draw/ir`): model, validation and the
//! exact `encoding/json` serialization of the Go structs.

use std::collections::BTreeMap;

use super::gotext::{quote, trim_space};

/// Dynamically typed value of `Custom map[string]any`.
///
/// Go accepts `custom` values nested up to 10 000 levels, so every traversal
/// of this type (`Drop`, `Clone`, `PartialEq`, `Debug`, JSON output) is
/// iterative or depth-bounded: none consumes native stack per nesting level.
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

/// Nesting level at which the `Debug` output elides a value.
const DEBUG_MAX_DEPTH: usize = 64;

impl Value {
    fn fmt_depth(&self, f: &mut std::fmt::Formatter<'_>, depth: usize) -> std::fmt::Result {
        if depth >= DEBUG_MAX_DEPTH {
            return f.write_str("..");
        }
        let depth = depth + 1;
        match self {
            Value::Null => f.write_str("Null"),
            Value::Bool(b) => f.debug_tuple("Bool").field(b).finish(),
            Value::Num(n) => f.debug_tuple("Num").field(n).finish(),
            Value::Int(n) => f.debug_tuple("Int").field(n).finish(),
            Value::Str(s) => f.debug_tuple("Str").field(s).finish(),
            Value::Arr(items) => f
                .debug_tuple("Arr")
                .field(&DebugList(items, depth))
                .finish(),
            Value::Map(map) => f.debug_tuple("Map").field(&DebugMap(map, depth)).finish(),
            Value::Struct(fields) => f
                .debug_tuple("Struct")
                .field(&DebugFields(fields, depth))
                .finish(),
        }
    }

    /// Takes the direct children out of `self`, leaving it childless.
    fn take_children(&mut self, out: &mut Vec<Value>) {
        match self {
            Value::Arr(items) => out.append(items),
            Value::Map(map) => out.extend(std::mem::take(map).into_values()),
            Value::Struct(fields) => out.extend(std::mem::take(fields).into_iter().map(|f| f.1)),
            _ => {}
        }
    }
}

struct Nested<'a>(&'a Value, usize);

impl std::fmt::Debug for Nested<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.0.fmt_depth(f, self.1)
    }
}

struct DebugList<'a>(&'a [Value], usize);

impl std::fmt::Debug for DebugList<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_list()
            .entries(self.0.iter().map(|v| Nested(v, self.1)))
            .finish()
    }
}

struct DebugMap<'a>(&'a BTreeMap<String, Value>, usize);

impl std::fmt::Debug for DebugMap<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_map()
            .entries(self.0.iter().map(|(k, v)| (k, Nested(v, self.1))))
            .finish()
    }
}

struct DebugFields<'a>(&'a [(&'static str, Value)], usize);

impl std::fmt::Debug for DebugFields<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_list()
            .entries(self.0.iter().map(|(k, v)| (k, Nested(v, self.1))))
            .finish()
    }
}

impl std::fmt::Debug for Value {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.fmt_depth(f, 0)
    }
}

impl Drop for Value {
    /// Iterative: dropping a 10 000-deep `Arr`/`Map` chain must not recurse.
    fn drop(&mut self) {
        let mut stack = Vec::new();
        self.take_children(&mut stack);
        while let Some(mut value) = stack.pop() {
            value.take_children(&mut stack);
        }
    }
}

enum CloneFrame<'a> {
    Arr(std::slice::Iter<'a, Value>, Vec<Value>),
    Map(
        std::collections::btree_map::Iter<'a, String, Value>,
        BTreeMap<String, Value>,
        Option<&'a String>,
    ),
    Struct(
        std::slice::Iter<'a, (&'static str, Value)>,
        Vec<(&'static str, Value)>,
        Option<&'static str>,
    ),
}

impl Clone for Value {
    /// Iterative deep clone (explicit stack instead of native recursion).
    fn clone(&self) -> Self {
        let mut stack: Vec<CloneFrame<'_>> = Vec::new();
        let mut next = Some(self);
        let mut completed: Option<Value> = None;
        loop {
            if let Some(value) = next.take() {
                match value {
                    Value::Null => completed = Some(Value::Null),
                    Value::Bool(b) => completed = Some(Value::Bool(*b)),
                    Value::Num(n) => completed = Some(Value::Num(*n)),
                    Value::Int(n) => completed = Some(Value::Int(*n)),
                    Value::Str(s) => completed = Some(Value::Str(s.clone())),
                    Value::Arr(items) => {
                        stack.push(CloneFrame::Arr(
                            items.iter(),
                            Vec::with_capacity(items.len()),
                        ));
                    }
                    Value::Map(map) => {
                        stack.push(CloneFrame::Map(map.iter(), BTreeMap::new(), None));
                    }
                    Value::Struct(fields) => stack.push(CloneFrame::Struct(
                        fields.iter(),
                        Vec::with_capacity(fields.len()),
                        None,
                    )),
                }
            }
            if let Some(done) = completed.take() {
                match stack.last_mut() {
                    None => return done,
                    Some(CloneFrame::Arr(_, out)) => out.push(done),
                    Some(CloneFrame::Map(_, out, key)) => {
                        if let Some(key) = key.take() {
                            out.insert(key.clone(), done);
                        }
                    }
                    Some(CloneFrame::Struct(_, out, name)) => {
                        if let Some(name) = name.take() {
                            out.push((name, done));
                        }
                    }
                }
            }
            let Some(top) = stack.last_mut() else {
                return Value::Null;
            };
            match top {
                CloneFrame::Arr(iter, out) => match iter.next() {
                    Some(item) => next = Some(item),
                    None => completed = Some(Value::Arr(std::mem::take(out))),
                },
                CloneFrame::Map(iter, out, key) => match iter.next() {
                    Some((k, item)) => {
                        *key = Some(k);
                        next = Some(item);
                    }
                    None => completed = Some(Value::Map(std::mem::take(out))),
                },
                CloneFrame::Struct(iter, out, name) => match iter.next() {
                    Some((k, item)) => {
                        *name = Some(k);
                        next = Some(item);
                    }
                    None => completed = Some(Value::Struct(std::mem::take(out))),
                },
            }
            if completed.is_some() {
                stack.pop();
            }
        }
    }
}

impl PartialEq for Value {
    /// Iterative structural equality (derived `PartialEq` semantics, `f64`
    /// compared with `==`).
    fn eq(&self, other: &Self) -> bool {
        let mut stack = vec![(self, other)];
        while let Some((a, b)) = stack.pop() {
            match (a, b) {
                (Value::Null, Value::Null) => {}
                (Value::Bool(x), Value::Bool(y)) if x == y => {}
                (Value::Num(x), Value::Num(y)) if x == y => {}
                (Value::Int(x), Value::Int(y)) if x == y => {}
                (Value::Str(x), Value::Str(y)) if x == y => {}
                (Value::Arr(x), Value::Arr(y)) if x.len() == y.len() => {
                    stack.extend(x.iter().zip(y));
                }
                (Value::Map(x), Value::Map(y)) if x.len() == y.len() => {
                    for ((kx, vx), (ky, vy)) in x.iter().zip(y) {
                        if kx != ky {
                            return false;
                        }
                        stack.push((vx, vy));
                    }
                }
                (Value::Struct(x), Value::Struct(y)) if x.len() == y.len() => {
                    for ((nx, vx), (ny, vy)) in x.iter().zip(y) {
                        if nx != ny {
                            return false;
                        }
                        stack.push((vx, vy));
                    }
                }
                _ => return false,
            }
        }
        true
    }
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

enum WriteFrame<'a> {
    Arr(std::slice::Iter<'a, Value>),
    Map(std::collections::btree_map::Iter<'a, String, Value>),
    Struct(std::slice::Iter<'a, (&'static str, Value)>),
}

enum WriteStep<'a> {
    Item(Option<&'a str>, &'a Value),
    Close(char),
}

fn write_map(out: &mut String, map: &BTreeMap<String, Value>) {
    out.push('{');
    let mut stack = vec![WriteFrame::Map(map.iter())];
    write_frames(out, &mut stack);
}

/// Writes a scalar completely, or its opening bracket plus a pushed frame.
fn open_value<'a>(out: &mut String, value: &'a Value, stack: &mut Vec<WriteFrame<'a>>) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(true) => out.push_str("true"),
        Value::Bool(false) => out.push_str("false"),
        Value::Num(n) => write_f64(out, *n),
        Value::Int(n) => out.push_str(&n.to_string()),
        Value::Str(s) => write_string(out, s),
        Value::Arr(items) => {
            out.push('[');
            stack.push(WriteFrame::Arr(items.iter()));
        }
        Value::Map(map) => {
            out.push('{');
            stack.push(WriteFrame::Map(map.iter()));
        }
        Value::Struct(fields) => {
            out.push('{');
            stack.push(WriteFrame::Struct(fields.iter()));
        }
    }
}

/// Drives the frames to completion with an explicit stack: the nesting depth
/// of `custom` values (up to 10 000 in Go) never reaches the native stack.
fn write_frames<'a>(out: &mut String, stack: &mut Vec<WriteFrame<'a>>) {
    // Whether the next item of each open frame is its first one.
    let mut first: Vec<bool> = vec![true; stack.len()];
    while let Some(top) = stack.last_mut() {
        let step = match top {
            WriteFrame::Arr(iter) => match iter.next() {
                Some(item) => WriteStep::Item(None, item),
                None => WriteStep::Close(']'),
            },
            WriteFrame::Map(iter) => match iter.next() {
                Some((key, item)) => WriteStep::Item(Some(key.as_str()), item),
                None => WriteStep::Close('}'),
            },
            WriteFrame::Struct(iter) => match iter.next() {
                Some((name, item)) => WriteStep::Item(Some(name), item),
                None => WriteStep::Close('}'),
            },
        };
        match step {
            WriteStep::Item(key, item) => {
                if let Some(is_first) = first.last_mut() {
                    if !*is_first {
                        out.push(',');
                    }
                    *is_first = false;
                }
                if let Some(key) = key {
                    write_string(out, key);
                    out.push(':');
                }
                let depth = stack.len();
                open_value(out, item, stack);
                if stack.len() > depth {
                    first.push(true);
                }
            }
            WriteStep::Close(bracket) => {
                out.push(bracket);
                stack.pop();
                first.pop();
            }
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
