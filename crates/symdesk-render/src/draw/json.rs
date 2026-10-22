//! Go `encoding/json` `Decoder` (with `DisallowUnknownFields`) semantics for
//! `ir.Diagram`: first-value syntax scan with Go's exact scanner errors, then a
//! typed decode that reports the first saved error like `decodeState` does.

use std::collections::BTreeMap;

use super::gotext::{quote, quote_json_byte};
use super::ir::{AxisSpec, ChartSpec, DataPoint, Diagram, Edge, Group, Node, Series, Value};

const MAX_NESTING_DEPTH: usize = 10_000;

#[derive(Debug, Clone, PartialEq)]
enum Json {
    Null,
    Bool(bool),
    Num(String),
    Str(String),
    Arr(Vec<Json>),
    Obj(Vec<(String, Json)>),
}

impl Json {
    fn kind(&self) -> &'static str {
        match self {
            Json::Null => "null",
            Json::Bool(_) => "bool",
            Json::Num(_) => "number",
            Json::Str(_) => "string",
            Json::Arr(_) => "array",
            Json::Obj(_) => "object",
        }
    }
}

/// Decodes the first JSON value of `data` into a [`Diagram`], returning the
/// exact error string Go's `json.Decoder.Decode` would.
pub(super) fn decode_diagram(data: &[u8]) -> Result<Diagram, String> {
    let value = Scanner::new(data).first_value()?;
    let mut diagram = Diagram::default();
    let mut ctx = Context::default();
    ctx.decode_diagram(&value, &mut diagram)?;
    Ok(diagram)
}

// ---------------------------------------------------------------------------
// Syntax scan (port of encoding/json scanner.go state machine)

struct Scanner<'a> {
    data: &'a [u8],
    pos: usize,
}

enum Frame {
    Obj(Vec<(String, Json)>, Option<String>),
    Arr(Vec<Json>),
}

fn is_space(c: u8) -> bool {
    matches!(c, b' ' | b'\t' | b'\r' | b'\n')
}

impl<'a> Scanner<'a> {
    fn new(data: &'a [u8]) -> Self {
        Self { data, pos: 0 }
    }

    fn syntax(c: u8, context: &str) -> String {
        format!("invalid character {} {}", quote_json_byte(c), context)
    }

    /// Next byte or Go's `io.ErrUnexpectedEOF` text.
    fn next(&mut self) -> Result<u8, String> {
        let c = self
            .data
            .get(self.pos)
            .copied()
            .ok_or_else(|| "unexpected EOF".to_owned())?;
        self.pos += 1;
        Ok(c)
    }

    fn next_non_space(&mut self) -> Result<u8, String> {
        loop {
            let c = self.next()?;
            if !is_space(c) {
                return Ok(c);
            }
        }
    }

    fn first_value(&mut self) -> Result<Json, String> {
        let mut stack: Vec<Frame> = Vec::new();
        'outer: loop {
            // --- expect a value ---
            let c = self.next_non_space()?;
            let mut completed = match c {
                b'{' => {
                    if stack.len() + 1 > MAX_NESTING_DEPTH {
                        return Err(Self::syntax(c, "exceeded max depth"));
                    }
                    let c2 = self.next_non_space()?;
                    if c2 == b'}' {
                        Json::Obj(Vec::new())
                    } else {
                        let key = self.read_key(c2)?;
                        stack.push(Frame::Obj(Vec::new(), Some(key)));
                        continue 'outer;
                    }
                }
                b'[' => {
                    if stack.len() + 1 > MAX_NESTING_DEPTH {
                        return Err(Self::syntax(c, "exceeded max depth"));
                    }
                    self.skip_space();
                    if self.data.get(self.pos) == Some(&b']') {
                        self.pos += 1;
                        Json::Arr(Vec::new())
                    } else {
                        if self.pos >= self.data.len() {
                            return Err("unexpected EOF".to_owned());
                        }
                        stack.push(Frame::Arr(Vec::new()));
                        continue 'outer;
                    }
                }
                b'"' => Json::Str(self.string_body()?),
                b'-' | b'0'..=b'9' => self.number(c)?,
                b't' => self.literal("true", Json::Bool(true))?,
                b'f' => self.literal("false", Json::Bool(false))?,
                b'n' => self.literal("null", Json::Null)?,
                _ => return Err(Self::syntax(c, "looking for beginning of value")),
            };

            // --- a value completed: fold it into its parents ---
            loop {
                match stack.last_mut() {
                    None => return Ok(completed),
                    Some(Frame::Obj(entries, key)) => {
                        entries.push((key.take().unwrap_or_default(), completed));
                        let c = self.next_non_space()?;
                        match c {
                            b',' => {
                                let c = self.next_non_space()?;
                                *key = Some(self.read_key(c)?);
                                continue 'outer;
                            }
                            b'}' => {
                                let Some(Frame::Obj(entries, _)) = stack.pop() else {
                                    unreachable!()
                                };
                                completed = Json::Obj(entries);
                            }
                            _ => return Err(Self::syntax(c, "after object key:value pair")),
                        }
                    }
                    Some(Frame::Arr(items)) => {
                        items.push(completed);
                        let c = self.next_non_space()?;
                        match c {
                            b',' => continue 'outer,
                            b']' => {
                                let Some(Frame::Arr(items)) = stack.pop() else {
                                    unreachable!()
                                };
                                completed = Json::Arr(items);
                            }
                            _ => return Err(Self::syntax(c, "after array element")),
                        }
                    }
                }
            }
        }
    }

    fn skip_space(&mut self) {
        while self.data.get(self.pos).is_some_and(|c| is_space(*c)) {
            self.pos += 1;
        }
    }

    /// Reads an object key whose first non-space byte `c` was consumed, then
    /// the following colon.
    fn read_key(&mut self, c: u8) -> Result<String, String> {
        if c != b'"' {
            return Err(Self::syntax(
                c,
                "looking for beginning of object key string",
            ));
        }
        let key = self.string_body()?;
        let colon = self.next_non_space()?;
        if colon != b':' {
            return Err(Self::syntax(colon, "after object key"));
        }
        Ok(key)
    }

    /// Scans a string after its opening quote and returns the unquoted value.
    fn string_body(&mut self) -> Result<String, String> {
        let mut raw: Vec<u8> = Vec::new();
        loop {
            let c = self.next()?;
            match c {
                b'"' => break,
                b'\\' => {
                    let e = self.next()?;
                    match e {
                        b'b' => raw.push(8),
                        b'f' => raw.push(12),
                        b'n' => raw.push(b'\n'),
                        b'r' => raw.push(b'\r'),
                        b't' => raw.push(b'\t'),
                        b'\\' | b'/' | b'"' => raw.push(e),
                        b'u' => {
                            let first = self.hex4()?;
                            let mut rune = first;
                            if (0xD800..0xDC00).contains(&first) {
                                // Look ahead for a following \uXXXX low surrogate.
                                let save = self.pos;
                                if self.data.get(self.pos) == Some(&b'\\')
                                    && self.data.get(self.pos + 1) == Some(&b'u')
                                {
                                    self.pos += 2;
                                    let second = self.hex4()?;
                                    if (0xDC00..0xE000).contains(&second) {
                                        rune =
                                            0x10000 + ((first - 0xD800) << 10) + (second - 0xDC00);
                                    } else {
                                        self.pos = save;
                                        rune = 0xFFFD;
                                    }
                                } else {
                                    rune = 0xFFFD;
                                }
                            } else if (0xDC00..0xE000).contains(&first) {
                                rune = 0xFFFD;
                            }
                            let ch = char::from_u32(rune).unwrap_or('\u{FFFD}');
                            let mut buf = [0u8; 4];
                            raw.extend_from_slice(ch.encode_utf8(&mut buf).as_bytes());
                        }
                        _ => return Err(Self::syntax(e, "in string escape code")),
                    }
                }
                c if c < 0x20 => return Err(Self::syntax(c, "in string literal")),
                c => raw.push(c),
            }
        }
        Ok(String::from_utf8_lossy(&raw).into_owned())
    }

    fn hex4(&mut self) -> Result<u32, String> {
        let mut value = 0u32;
        for _ in 0..4 {
            let c = self.next()?;
            let digit = match c {
                b'0'..=b'9' => c - b'0',
                b'a'..=b'f' => c - b'a' + 10,
                b'A'..=b'F' => c - b'A' + 10,
                _ => return Err(Self::syntax(c, "in \\u hexadecimal character escape")),
            };
            value = value * 16 + u32::from(digit);
        }
        Ok(value)
    }

    fn peek_digit(&self) -> bool {
        self.data.get(self.pos).is_some_and(u8::is_ascii_digit)
    }

    fn digits(&mut self) {
        while self.peek_digit() {
            self.pos += 1;
        }
    }

    fn number(&mut self, first: u8) -> Result<Json, String> {
        let start = self.pos - 1;
        let mut c = first;
        if c == b'-' {
            c = self.next()?;
            if !c.is_ascii_digit() {
                return Err(Self::syntax(c, "in numeric literal"));
            }
        }
        if c != b'0' {
            self.digits();
        }
        if self.data.get(self.pos) == Some(&b'.') {
            self.pos += 1;
            let d = self.next()?;
            if !d.is_ascii_digit() {
                return Err(Self::syntax(d, "after decimal point in numeric literal"));
            }
            self.digits();
        }
        if matches!(self.data.get(self.pos), Some(b'e' | b'E')) {
            self.pos += 1;
            let mut d = self.next()?;
            if d == b'+' || d == b'-' {
                d = self.next()?;
            }
            if !d.is_ascii_digit() {
                return Err(Self::syntax(d, "in exponent of numeric literal"));
            }
            self.digits();
        }
        Ok(Json::Num(
            String::from_utf8_lossy(&self.data[start..self.pos]).into_owned(),
        ))
    }

    fn literal(&mut self, word: &'static str, value: Json) -> Result<Json, String> {
        for expected in word.bytes().skip(1) {
            let c = self.next()?;
            if c != expected {
                return Err(Self::syntax(
                    c,
                    &format!(
                        "in literal {word} (expecting {})",
                        quote_json_byte(expected)
                    ),
                ));
            }
        }
        Ok(value)
    }
}

// ---------------------------------------------------------------------------
// Typed decode (decodeState semantics; the first saved error wins)

#[derive(Default)]
struct Context {
    structure: &'static str,
    fields: Vec<&'static str>,
}

type Res = Result<(), String>;

fn fold_key(s: &str) -> String {
    s.chars()
        .map(|c| match c {
            '\u{212A}' => 'K',
            '\u{17F}' => 'S',
            c => c.to_ascii_uppercase(),
        })
        .collect()
}

/// Go struct field lookup: exact JSON name first, then folded name.
fn lookup(names: &[&'static str], key: &str) -> Option<&'static str> {
    if let Some(name) = names.iter().find(|name| **name == key) {
        return Some(name);
    }
    let folded = fold_key(key);
    names.iter().find(|name| fold_key(name) == folded).copied()
}

impl Context {
    fn type_error(&self, value: &str, ty: &str) -> String {
        if self.structure.is_empty() && self.fields.is_empty() {
            format!("json: cannot unmarshal {value} into Go value of type {ty}")
        } else {
            format!(
                "json: cannot unmarshal {value} into Go struct field {}.{} of type {ty}",
                self.structure,
                self.fields.join(".")
            )
        }
    }

    /// Runs `body` with the error context of entering `field` of `structure`.
    fn in_field<T>(
        &mut self,
        structure: &'static str,
        field: &'static str,
        body: impl FnOnce(&mut Self) -> T,
    ) -> T {
        let saved = self.structure;
        self.structure = structure;
        self.fields.push(field);
        let out = body(self);
        self.fields.pop();
        self.structure = saved;
        out
    }

    fn object<'a>(&self, j: &'a Json, ty: &str) -> Result<Option<&'a [(String, Json)]>, String> {
        match j {
            Json::Null => Ok(None),
            Json::Obj(entries) => Ok(Some(entries)),
            other => Err(self.type_error(other.kind(), ty)),
        }
    }

    fn string(&self, j: &Json, out: &mut String, ty: &str) -> Res {
        match j {
            Json::Null => Ok(()),
            Json::Str(s) => {
                out.clone_from(s);
                Ok(())
            }
            other => Err(self.type_error(other.kind(), ty)),
        }
    }

    fn float(&self, j: &Json, out: &mut f64) -> Res {
        match j {
            Json::Null => Ok(()),
            Json::Num(raw) => {
                *out = self.parse_number(raw)?;
                Ok(())
            }
            other => Err(self.type_error(other.kind(), "float64")),
        }
    }

    fn parse_number(&self, raw: &str) -> Result<f64, String> {
        match raw.parse::<f64>() {
            Ok(n) if n.is_finite() => Ok(n),
            _ => Err(self.type_error(&format!("number {raw}"), "float64")),
        }
    }

    fn opt_float(&self, j: &Json, out: &mut Option<f64>) -> Res {
        match j {
            Json::Null => {
                *out = None;
                Ok(())
            }
            other => {
                let mut value = out.unwrap_or(0.0);
                let result = self.float(other, &mut value);
                *out = Some(value);
                result
            }
        }
    }

    fn boolean(&self, j: &Json, out: &mut bool) -> Res {
        match j {
            Json::Null => Ok(()),
            Json::Bool(b) => {
                *out = *b;
                Ok(())
            }
            other => Err(self.type_error(other.kind(), "bool")),
        }
    }

    /// Decodes a JSON array into a Go slice with element-wise merge into the
    /// existing elements (Go reuses elements already present).
    fn slice<T: Default>(
        &mut self,
        j: &Json,
        out: &mut Vec<T>,
        ty: &str,
        mut element: impl FnMut(&mut Self, &Json, &mut T) -> Res,
    ) -> Res {
        match j {
            Json::Null => {
                out.clear();
                Ok(())
            }
            Json::Arr(items) => {
                if items.is_empty() {
                    out.clear();
                    return Ok(());
                }
                for (i, item) in items.iter().enumerate() {
                    if i >= out.len() {
                        out.push(T::default());
                    }
                    element(self, item, &mut out[i])?;
                }
                out.truncate(items.len());
                Ok(())
            }
            other => Err(self.type_error(other.kind(), ty)),
        }
    }

    /// Like [`Self::slice`] for fields where nil and empty differ on output.
    fn nil_slice<T: Default>(
        &mut self,
        j: &Json,
        out: &mut Option<Vec<T>>,
        ty: &str,
        element: impl FnMut(&mut Self, &Json, &mut T) -> Res,
    ) -> Res {
        match j {
            Json::Null => {
                *out = None;
                Ok(())
            }
            Json::Arr(items) if items.is_empty() => {
                *out = Some(Vec::new());
                Ok(())
            }
            Json::Arr(_) => {
                let mut vec = out.take().unwrap_or_default();
                let result = self.slice(j, &mut vec, ty, element);
                *out = Some(vec);
                result
            }
            other => Err(self.type_error(other.kind(), ty)),
        }
    }

    fn unknown(key: &str) -> String {
        format!("json: unknown field {}", quote(key))
    }

    fn decode_diagram(&mut self, j: &Json, d: &mut Diagram) -> Res {
        const NAMES: &[&str] = &[
            "version",
            "kind",
            "title",
            "direction",
            "theme",
            "width",
            "height",
            "nodes",
            "edges",
            "groups",
            "chart",
            "custom",
        ];
        let Some(entries) = self.object(j, "ir.Diagram")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("Diagram", name, |ctx| match name {
                "version" => ctx.string(value, &mut d.version, "string"),
                "kind" => ctx.string(value, &mut d.kind, "ir.DiagramKind"),
                "title" => ctx.string(value, &mut d.title, "string"),
                "direction" => ctx.string(value, &mut d.direction, "ir.Direction"),
                "theme" => ctx.string(value, &mut d.theme, "string"),
                "width" => ctx.float(value, &mut d.width),
                "height" => ctx.float(value, &mut d.height),
                "nodes" => ctx.slice(value, &mut d.nodes, "[]ir.Node", Self::decode_node),
                "edges" => ctx.slice(value, &mut d.edges, "[]ir.Edge", Self::decode_edge),
                "groups" => ctx.slice(value, &mut d.groups, "[]ir.Group", Self::decode_group),
                "chart" => match value {
                    Json::Null => {
                        d.chart = None;
                        Ok(())
                    }
                    other => {
                        ctx.decode_chart(other, d.chart.get_or_insert_with(ChartSpec::default))
                    }
                },
                _ => ctx.custom(value, &mut d.custom),
            })?;
        }
        Ok(())
    }

    fn custom(&mut self, j: &Json, out: &mut BTreeMap<String, Value>) -> Res {
        match j {
            Json::Null => {
                out.clear();
                Ok(())
            }
            Json::Obj(entries) => {
                for (key, value) in entries {
                    out.insert(key.clone(), self.any(value)?);
                }
                Ok(())
            }
            other => Err(self.type_error(other.kind(), "map[string]interface {}")),
        }
    }

    fn any(&self, j: &Json) -> Result<Value, String> {
        Ok(match j {
            Json::Null => Value::Null,
            Json::Bool(b) => Value::Bool(*b),
            Json::Num(raw) => Value::Num(self.parse_number(raw)?),
            Json::Str(s) => Value::Str(s.clone()),
            Json::Arr(items) => Value::Arr(
                items
                    .iter()
                    .map(|item| self.any(item))
                    .collect::<Result<_, _>>()?,
            ),
            Json::Obj(entries) => {
                let mut map = BTreeMap::new();
                for (key, value) in entries {
                    map.insert(key.clone(), self.any(value)?);
                }
                Value::Map(map)
            }
        })
    }

    fn decode_node(&mut self, j: &Json, n: &mut Node) -> Res {
        const NAMES: &[&str] = &[
            "id", "label", "shape", "note", "icon", "style", "width", "height", "x", "y",
        ];
        let Some(entries) = self.object(j, "ir.Node")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("Node", name, |ctx| match name {
                "id" => ctx.string(value, &mut n.id, "string"),
                "label" => ctx.string(value, &mut n.label, "string"),
                "shape" => ctx.string(value, &mut n.shape, "ir.NodeShape"),
                "note" => ctx.string(value, &mut n.note, "string"),
                "icon" => ctx.string(value, &mut n.icon, "string"),
                "style" => ctx.decode_node_style(value, &mut n.style),
                "width" => ctx.float(value, &mut n.width),
                "height" => ctx.float(value, &mut n.height),
                "x" => ctx.float(value, &mut n.x),
                _ => ctx.float(value, &mut n.y),
            })?;
        }
        Ok(())
    }

    fn decode_node_style(&mut self, j: &Json, s: &mut super::ir::NodeStyle) -> Res {
        const NAMES: &[&str] = &[
            "fill",
            "stroke",
            "stroke_width",
            "text_color",
            "opacity",
            "dash_array",
        ];
        let Some(entries) = self.object(j, "ir.NodeStyle")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("NodeStyle", name, |ctx| match name {
                "fill" => ctx.string(value, &mut s.fill, "string"),
                "stroke" => ctx.string(value, &mut s.stroke, "string"),
                "stroke_width" => ctx.opt_float(value, &mut s.stroke_width),
                "text_color" => ctx.string(value, &mut s.text_color, "string"),
                "opacity" => ctx.opt_float(value, &mut s.opacity),
                _ => ctx.string(value, &mut s.dash_array, "string"),
            })?;
        }
        Ok(())
    }

    fn decode_edge(&mut self, j: &Json, e: &mut Edge) -> Res {
        const NAMES: &[&str] = &["from", "to", "label", "style", "arrow", "color"];
        let Some(entries) = self.object(j, "ir.Edge")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("Edge", name, |ctx| match name {
                "from" => ctx.string(value, &mut e.from, "string"),
                "to" => ctx.string(value, &mut e.to, "string"),
                "label" => ctx.string(value, &mut e.label, "string"),
                "style" => ctx.string(value, &mut e.style, "ir.EdgeStyle"),
                "arrow" => ctx.string(value, &mut e.arrow, "ir.ArrowType"),
                _ => ctx.string(value, &mut e.color, "string"),
            })?;
        }
        Ok(())
    }

    fn decode_group(&mut self, j: &Json, g: &mut Group) -> Res {
        const NAMES: &[&str] = &["id", "label", "members", "style"];
        let Some(entries) = self.object(j, "ir.Group")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("Group", name, |ctx| match name {
                "id" => ctx.string(value, &mut g.id, "string"),
                "label" => ctx.string(value, &mut g.label, "string"),
                "members" => ctx.nil_slice(
                    value,
                    &mut g.members,
                    "[]string",
                    |ctx, j, out: &mut String| ctx.string(j, out, "string"),
                ),
                _ => ctx.decode_group_style(value, &mut g.style),
            })?;
        }
        Ok(())
    }

    fn decode_group_style(&mut self, j: &Json, s: &mut super::ir::GroupStyle) -> Res {
        const NAMES: &[&str] = &["fill", "stroke", "stroke_width"];
        let Some(entries) = self.object(j, "ir.GroupStyle")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("GroupStyle", name, |ctx| match name {
                "fill" => ctx.string(value, &mut s.fill, "string"),
                "stroke" => ctx.string(value, &mut s.stroke, "string"),
                _ => ctx.opt_float(value, &mut s.stroke_width),
            })?;
        }
        Ok(())
    }

    fn decode_chart(&mut self, j: &Json, c: &mut ChartSpec) -> Res {
        const NAMES: &[&str] = &["type", "title", "series", "x_axis", "y_axis", "legend"];
        let Some(entries) = self.object(j, "ir.ChartSpec")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("ChartSpec", name, |ctx| match name {
                "type" => ctx.string(value, &mut c.r#type, "ir.ChartType"),
                "title" => ctx.string(value, &mut c.title, "string"),
                "series" => ctx.nil_slice(value, &mut c.series, "[]ir.Series", Self::decode_series),
                "x_axis" => ctx.decode_axis(value, &mut c.x_axis),
                "y_axis" => ctx.decode_axis(value, &mut c.y_axis),
                _ => ctx.boolean(value, &mut c.legend),
            })?;
        }
        Ok(())
    }

    fn decode_series(&mut self, j: &Json, s: &mut Series) -> Res {
        const NAMES: &[&str] = &["name", "data", "color"];
        let Some(entries) = self.object(j, "ir.Series")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("Series", name, |ctx| match name {
                "name" => ctx.string(value, &mut s.name, "string"),
                "data" => ctx.nil_slice(value, &mut s.data, "[]ir.DataPoint", Self::decode_point),
                _ => ctx.string(value, &mut s.color, "string"),
            })?;
        }
        Ok(())
    }

    fn decode_point(&mut self, j: &Json, p: &mut DataPoint) -> Res {
        const NAMES: &[&str] = &["x", "y", "label", "color"];
        let Some(entries) = self.object(j, "ir.DataPoint")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("DataPoint", name, |ctx| match name {
                "x" => ctx.float(value, &mut p.x),
                "y" => ctx.float(value, &mut p.y),
                "label" => ctx.string(value, &mut p.label, "string"),
                _ => ctx.string(value, &mut p.color, "string"),
            })?;
        }
        Ok(())
    }

    fn decode_axis(&mut self, j: &Json, a: &mut AxisSpec) -> Res {
        const NAMES: &[&str] = &["title", "labels", "min", "max"];
        let Some(entries) = self.object(j, "ir.AxisSpec")? else {
            return Ok(());
        };
        for (key, value) in entries {
            let Some(name) = lookup(NAMES, key) else {
                return Err(Self::unknown(key));
            };
            self.in_field("AxisSpec", name, |ctx| match name {
                "title" => ctx.string(value, &mut a.title, "string"),
                "labels" => ctx.slice(
                    value,
                    &mut a.labels,
                    "[]string",
                    |ctx, j, out: &mut String| ctx.string(j, out, "string"),
                ),
                "min" => ctx.opt_float(value, &mut a.min),
                _ => ctx.opt_float(value, &mut a.max),
            })?;
        }
        Ok(())
    }
}
