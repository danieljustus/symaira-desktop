//! The press frontmatter contract (Go `internal/pdf/internal/press/frontmatter.go`
//! and `errors.go`): split, strict decode (unknown keys are an error), profile
//! validation and the `meta.json` the Typst templates read.
//!
//! Decoding follows `gopkg.in/yaml.v3` into the same Go types: scalars become
//! string fields from their original text, `yes`/`n` and friends are accepted
//! for booleans, floats truncate into integers, type errors are collected in
//! document order and reported as `line N: ...`.

use std::collections::BTreeMap;

use crate::press::Profile;
use crate::press_yaml::{Kind, Node, Style};

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Address {
    pub name: String,
    pub address: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct PdfOptions {
    pub standard: Vec<String>,
    pub reproducible: Option<bool>,
}

/// A decoded YAML value for the free-form `data:` block.
#[derive(Clone, Debug, PartialEq)]
pub enum Json {
    Null,
    Bool(bool),
    Int(i64),
    Uint(u64),
    Float(f64),
    Str(String),
    Time(String),
    Arr(Vec<Json>),
    Obj(BTreeMap<String, Json>),
    /// `map[interface{}]interface{}`: yaml.v3 yields it for non-string keys and
    /// `encoding/json` refuses it.
    GenericMap,
    /// A DIN address block, rendered in struct field order.
    Raw(Address),
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Frontmatter {
    pub profile: String,
    pub lang: String,
    pub title: String,
    pub subtitle: String,
    pub author: Vec<String>,
    pub date: String,
    pub keywords: Vec<String>,
    pub form: String,
    pub sender: Option<Address>,
    pub recipient: Option<Address>,
    pub betreff: String,
    pub infoblock: BTreeMap<String, String>,
    pub titlepage: Option<bool>,
    pub toc: Option<bool>,
    pub toc_depth: i64,
    pub header_left: String,
    pub header_right: String,
    pub footer_left: String,
    pub footer_right: String,
    pub data: BTreeMap<String, Json>,
    pub meeting_id: String,
    pub participants: Vec<String>,
    pub duration: String,
    pub location: String,
    pub pdf: PdfOptions,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Issue {
    pub severity: &'static str,
    pub field: String,
    pub message: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct ContractError {
    pub reason: String,
    pub detail: String,
    pub issues: Vec<Issue>,
}

impl std::fmt::Display for ContractError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        if !self.issues.is_empty() {
            write!(f, "{} frontmatter issue(s)", self.issues.len())?;
            for is in &self.issues {
                write!(f, "\n  • [{}] {} — {}", is.severity, is.field, is.message)?;
            }
            return Ok(());
        }
        if !self.detail.is_empty() {
            return write!(f, "{}: {}", self.reason, self.detail);
        }
        f.write_str(&self.reason)
    }
}

pub struct Document {
    pub front: Frontmatter,
    pub body: Vec<u8>,
}

/// Split a leading `---` frontmatter block from the body (Go `Split`).
pub fn split(src: &[u8]) -> (Option<Vec<u8>>, Vec<u8>) {
    let norm = replace_all(src, b"\r\n", b"\n");
    if !norm.starts_with(b"---\n") {
        return (None, src.to_vec());
    }
    let rest = String::from_utf8_lossy(&norm[4..]).into_owned();
    let lines: Vec<&str> = rest.split('\n').collect();
    for (i, ln) in lines.iter().enumerate() {
        if *ln == "---" || *ln == "..." {
            let fm = lines[..i].join("\n");
            let body = lines[i + 1..].join("\n");
            let body = body.strip_prefix('\n').unwrap_or(&body).to_owned();
            return (Some(fm.into_bytes()), body.into_bytes());
        }
    }
    (None, src.to_vec())
}

fn replace_all(src: &[u8], from: &[u8], to: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(src.len());
    let mut i = 0;
    while i < src.len() {
        if src[i..].starts_with(from) {
            out.extend_from_slice(to);
            i += from.len();
        } else {
            out.push(src[i]);
            i += 1;
        }
    }
    out
}

/// Split and decode a source document (Go `Parse`).
pub fn parse(src: &[u8]) -> Result<Document, ContractError> {
    let (fm, body) = split(src);
    let mut doc = Document { front: Frontmatter::default(), body };
    let fm = fm.unwrap_or_default();
    let text = String::from_utf8_lossy(&fm).into_owned();
    if text.trim().is_empty() {
        return Ok(doc);
    }
    let node = match crate::press_yaml::parse(&text) {
        Ok(Some(n)) => n,
        // A document with only comments is an empty stream: yaml.v3 returns io.EOF.
        Ok(None) => return Err(ContractError { reason: "invalid frontmatter".into(), detail: "EOF".into(), issues: vec![] }),
        Err(e) => {
            return Err(ContractError {
                reason: "invalid frontmatter".into(),
                detail: format!("line {}: {}", e.line, e.message),
                issues: vec![],
            });
        }
    };
    let mut d = Decoder::default();
    match d.front(&node, &mut doc.front) {
        Err(hard) => Err(ContractError { reason: "invalid frontmatter".into(), detail: hard, issues: vec![] }),
        Ok(()) if !d.terrors.is_empty() => {
            Err(ContractError { reason: "invalid frontmatter".into(), detail: clean_yaml_error(&d.terrors), issues: vec![] })
        }
        Ok(()) => Ok(doc),
    }
}

/// Go `cleanYAMLError` over yaml.v3's `unmarshal errors:` block.
fn clean_yaml_error(errors: &[String]) -> String {
    let mut msg = String::from("yaml: unmarshal errors:");
    for e in errors {
        msg.push_str("\n  ");
        msg.push_str(e);
    }
    let msg = msg.strip_prefix("yaml: unmarshal errors:\n").unwrap_or(&msg).to_owned();
    let msg = msg.replace("\n  ", "; ");
    let msg = msg.strip_prefix("yaml: ").unwrap_or(&msg);
    msg.trim().to_owned()
}

fn din_date(s: &str) -> bool {
    let b = s.as_bytes();
    let digits = |r: std::ops::Range<usize>| b[r].iter().all(u8::is_ascii_digit);
    if b.len() != 10 {
        return false;
    }
    (digits(0..2) && b[2] == b'.' && digits(3..5) && b[5] == b'.' && digits(6..10))
        || (digits(0..4) && b[4] == b'-' && digits(5..7) && b[7] == b'-' && digits(8..10))
}

impl Document {
    /// Every issue the profile finds (Go `Validate`).
    pub fn validate(&self, p: &Profile) -> Vec<Issue> {
        let mut issues = Vec::new();
        let mut add = |sev: &'static str, field: &str, msg: String| issues.push(Issue { severity: sev, field: field.to_owned(), message: msg });
        for f in p.required_fields {
            if self.field_empty(f) {
                add("error", f, format!("profile {} requires {}", go_quote(p.name), go_quote(f)));
            }
        }
        let fr = &self.front;
        if !fr.date.is_empty() && !din_date(&fr.date) {
            add("error", "date", "must be TT.MM.JJJJ or JJJJ-MM-TT with leading zeros (DIN 5008)".into());
        }
        if !fr.form.is_empty() && fr.form != "A" && fr.form != "B" {
            add("error", "form", "DIN 5008 form must be \"A\" or \"B\"".into());
        }
        if p.requires_accessibility() {
            if fr.lang.is_empty() {
                add("error", "lang", "PDF/UA requires a document language (e.g. lang: de)".into());
            }
            if fr.title.is_empty() {
                add("error", "title", "PDF/UA requires a document title in metadata".into());
            }
        }
        issues
    }

    fn field_empty(&self, field: &str) -> bool {
        let f = &self.front;
        match field {
            "title" => f.title.is_empty(),
            "lang" => f.lang.is_empty(),
            "betreff" => f.betreff.is_empty(),
            "date" => f.date.is_empty(),
            "author" => f.author.is_empty(),
            "recipient" => f.recipient.as_ref().is_none_or(|a| a.name.is_empty()),
            "sender" => f.sender.as_ref().is_none_or(|a| a.name.is_empty()),
            "data" => f.data.is_empty(),
            _ => false,
        }
    }
}

/// Go `%q` for the ASCII profile and field names used in messages.
fn go_quote(s: &str) -> String {
    format!("{s:?}")
}

// ---------------------------------------------------------------------------
// Scalar resolution (yaml.v3 resolve.go)

#[derive(Clone, Debug, PartialEq)]
pub enum Rv {
    Null,
    Bool(bool),
    Int(i64),
    Uint(u64),
    Float(f64),
    Str(String),
    Time(String),
}

fn rv_tag(r: &Rv) -> &'static str {
    match r {
        Rv::Null => "!!null",
        Rv::Bool(_) => "!!bool",
        Rv::Int(_) | Rv::Uint(_) => "!!int",
        Rv::Float(_) => "!!float",
        Rv::Str(_) => "!!str",
        Rv::Time(_) => "!!timestamp",
    }
}

/// Go `strconv.ParseInt(s, 0, 64)`.
fn parse_int0(s: &str) -> Option<i64> {
    let (neg, body) = match s.strip_prefix('-') {
        Some(r) => (true, r),
        None => (false, s.strip_prefix('+').unwrap_or(s)),
    };
    let u = parse_uint_body(body)?;
    if neg {
        if u > (i64::MAX as u64) + 1 {
            return None;
        }
        Some((u as i64).wrapping_neg())
    } else {
        i64::try_from(u).ok()
    }
}

fn parse_uint_body(body: &str) -> Option<u64> {
    if body.is_empty() {
        return None;
    }
    let (radix, digits) = if let Some(r) = body.strip_prefix("0x").or_else(|| body.strip_prefix("0X")) {
        (16, r)
    } else if let Some(r) = body.strip_prefix("0b").or_else(|| body.strip_prefix("0B")) {
        (2, r)
    } else if let Some(r) = body.strip_prefix("0o").or_else(|| body.strip_prefix("0O")) {
        (8, r)
    } else if body.len() > 1 && body.starts_with('0') {
        (8, &body[1..])
    } else {
        (10, body)
    };
    if digits.is_empty() || !digits.chars().all(|c| c.is_digit(radix)) {
        return None;
    }
    u64::from_str_radix(digits, radix).ok()
}

fn style_float(s: &str) -> bool {
    // ^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$
    let b = s.as_bytes();
    let mut i = 0;
    if i < b.len() && (b[i] == b'-' || b[i] == b'+') {
        i += 1;
    }
    let st = i;
    while i < b.len() && b[i].is_ascii_digit() {
        i += 1;
    }
    let int_digits = i - st;
    if i < b.len() && b[i] == b'.' {
        i += 1;
        let fs = i;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
        }
        if int_digits == 0 && i == fs {
            return false;
        }
    } else if int_digits == 0 {
        return false;
    }
    if i < b.len() && (b[i] == b'e' || b[i] == b'E') {
        i += 1;
        if i < b.len() && (b[i] == b'-' || b[i] == b'+') {
            i += 1;
        }
        let es = i;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
        }
        if i == es {
            return false;
        }
    }
    i == b.len()
}

fn parse_timestamp(s: &str) -> Option<String> {
    let b = s.as_bytes();
    let mut i = 0;
    while i < b.len() && b[i].is_ascii_digit() {
        i += 1;
    }
    if i != 4 || i == b.len() || b[i] != b'-' {
        return None;
    }
    let num = |t: &str, max: usize| -> Option<u32> {
        if t.is_empty() || t.len() > max || !t.bytes().all(|c| c.is_ascii_digit()) {
            return None;
        }
        t.parse().ok()
    };
    let (date, rest) = match s.find(['T', 't', ' ']) {
        Some(p) => (&s[..p], Some((s.as_bytes()[p], &s[p + 1..]))),
        None => (s, None),
    };
    let mut parts = date.split('-');
    let year: i32 = parts.next()?.parse().ok()?;
    let month = num(parts.next()?, 2)?;
    let day = num(parts.next()?, 2)?;
    if parts.next().is_some() || !(1..=12).contains(&month) || day == 0 {
        return None;
    }
    let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
    let dim = [31, if leap { 29 } else { 28 }, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month as usize - 1];
    if day > dim {
        return None;
    }
    let date_out = format!("{year:04}-{month:02}-{day:02}");
    let Some((sep, rest)) = rest else {
        return Some(format!("{date_out}T00:00:00Z"));
    };
    // time[.fraction][zone]
    let (time, zone) = if sep == b' ' {
        (rest, "")
    } else if let Some(t) = rest.strip_suffix('Z') {
        (t, "Z")
    } else if let Some(p) = rest.rfind(['+', '-']).filter(|p| *p > 0) {
        (&rest[..p], &rest[p..])
    } else {
        return None;
    };
    if sep == b' ' && time.contains(['Z', '+']) {
        return None;
    }
    let (hms, frac) = match time.split_once('.') {
        Some((h, f)) => (h, Some(f)),
        None => (time, None),
    };
    let mut hp = hms.split(':');
    let (h, mi, se) = (num(hp.next()?, 2)?, num(hp.next()?, 2)?, num(hp.next()?, 2)?);
    if hp.next().is_some() || h > 23 || mi > 59 || se > 59 {
        return None;
    }
    let mut out = format!("{date_out}T{h:02}:{mi:02}:{se:02}");
    if let Some(f) = frac {
        if f.is_empty() || f.len() > 9 || !f.bytes().all(|c| c.is_ascii_digit()) {
            return None;
        }
        let trimmed = f.trim_end_matches('0');
        if !trimmed.is_empty() {
            out.push('.');
            out.push_str(trimmed);
        }
    }
    if zone.is_empty() || zone == "Z" {
        out.push('Z');
    } else {
        let z = &zone[1..];
        let (zh, zm) = z.split_once(':')?;
        let (zh, zm) = (num(zh, 2)?, num(zm, 2)?);
        if zh > 23 || zm > 59 || zone.len() != 6 {
            return None;
        }
        if zh == 0 && zm == 0 {
            out.push('Z');
        } else {
            out.push_str(&format!("{}{zh:02}:{zm:02}", &zone[..1]));
        }
    }
    Some(out)
}

fn resolve_plain(s: &str) -> Rv {
    match s {
        "" | "~" | "null" | "Null" | "NULL" => return Rv::Null,
        "true" | "True" | "TRUE" => return Rv::Bool(true),
        "false" | "False" | "FALSE" => return Rv::Bool(false),
        ".nan" | ".NaN" | ".NAN" => return Rv::Float(f64::NAN),
        ".inf" | ".Inf" | ".INF" | "+.inf" | "+.Inf" | "+.INF" => return Rv::Float(f64::INFINITY),
        "-.inf" | "-.Inf" | "-.INF" => return Rv::Float(f64::NEG_INFINITY),
        _ => {}
    }
    let first = s.as_bytes()[0];
    if !(first.is_ascii_digit() || first == b'+' || first == b'-' || first == b'.') {
        return Rv::Str(s.to_owned());
    }
    if first.is_ascii_digit() || first == b'+' || first == b'-' {
        if let Some(t) = parse_timestamp(s) {
            return Rv::Time(t);
        }
    }
    let plain = s.replace('_', "");
    if let Some(i) = parse_int0(&plain) {
        return Rv::Int(i);
    }
    if !plain.starts_with('-') {
        if let Some(u) = parse_uint_body(plain.strip_prefix('+').unwrap_or(&plain)) {
            return Rv::Uint(u);
        }
    }
    if style_float(&plain) {
        if let Ok(f) = plain.parse::<f64>() {
            return Rv::Float(f);
        }
    }
    if let Some(r) = plain.strip_prefix("0b") {
        if let Ok(i) = i64::from_str_radix(r, 2) {
            return Rv::Int(i);
        }
    }
    if let Some(r) = plain.strip_prefix("-0b") {
        if let Ok(i) = i64::from_str_radix(r, 2) {
            return Rv::Int(-i);
        }
    }
    Rv::Str(s.to_owned())
}

/// Go-style short tag and resolved value of a scalar node.
fn resolve(node: &Node) -> Result<(String, Rv), String> {
    let Kind::Scalar { text, style } = &node.kind else { unreachable!() };
    if let Some(tag) = &node.tag {
        let short = match tag.as_str() {
            "!!str" | "!!int" | "!!float" | "!!bool" | "!!null" | "!!binary" | "!!timestamp" => tag.clone(),
            t if t.starts_with('!') && !t.starts_with("!!") => return Ok(("!!str".into(), Rv::Str(text.clone()))),
            other => other.to_owned(),
        };
        return match short.as_str() {
            "!!str" => Ok((short, Rv::Str(text.clone()))),
            "!!binary" => {
                let cleaned: String = text.chars().filter(|c| !c.is_whitespace()).collect();
                match decode_b64(&cleaned) {
                    Some(b) => Ok((short, Rv::Str(String::from_utf8_lossy(&b).into_owned()))),
                    None => Err(format!("yaml: !!binary value contains invalid base64 data")),
                }
            }
            _ => {
                let r = resolve_plain(text);
                if rv_tag(&r) == short || (short == "!!float" && matches!(r, Rv::Int(_) | Rv::Uint(_))) {
                    Ok((short.clone(), if short == "!!float" { if let Rv::Int(i) = r { Rv::Float(i as f64) } else { r } } else { r }))
                } else {
                    Err(format!("yaml: cannot decode {} `{}` as a {}", rv_tag(&r), text, short))
                }
            }
        };
    }
    if *style != Style::Plain {
        return Ok(("!!str".into(), Rv::Str(text.clone())));
    }
    let r = resolve_plain(text);
    Ok((rv_tag(&r).to_owned(), r))
}

fn decode_b64(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::new();
    let mut buf = 0u32;
    let mut bits = 0;
    for c in s.trim_end_matches('=').chars() {
        let v = match c {
            'A'..='Z' => c as u32 - 65,
            'a'..='z' => c as u32 - 71,
            '0'..='9' => c as u32 + 4,
            '+' => 62,
            '/' => 63,
            _ => return None,
        };
        buf = (buf << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((buf >> bits) as u8);
            buf &= (1 << bits) - 1;
        }
    }
    Some(out)
}

// ---------------------------------------------------------------------------
// Decoding into the Go types

#[derive(Default)]
struct Decoder {
    terrors: Vec<String>,
}

fn node_tag(n: &Node) -> String {
    match &n.kind {
        Kind::Seq(_) => "!!seq".into(),
        Kind::Map(_) => "!!map".into(),
        Kind::Scalar { .. } => resolve(n).map(|(t, _)| t).unwrap_or_else(|_| "!!str".into()),
    }
}

impl Decoder {
    fn type_error(&mut self, n: &Node, target: &str) {
        let tag = node_tag(n);
        let line = n.line;
        if let Kind::Scalar { text, .. } = &n.kind {
            let value = if text.len() > 10 { format!(" `{}...`", &text[..text.char_indices().map(|(i, _)| i).take_while(|i| *i <= 7).last().unwrap_or(0)]) } else { format!(" `{text}`") };
            self.terrors.push(format!("line {line}: cannot unmarshal {tag}{value} into {target}"));
        } else {
            self.terrors.push(format!("line {line}: cannot unmarshal {tag} into {target}"));
        }
    }

    fn is_null(n: &Node) -> bool {
        matches!(&n.kind, Kind::Scalar { .. }) && matches!(resolve(n), Ok((_, Rv::Null)))
    }

    fn unique_keys(&mut self, pairs: &[(Node, Node)]) {
        for i in (0..pairs.len() * 2).step_by(2) {
            let ni = &pairs[i / 2].0;
            for j in ((i + 2)..pairs.len() * 2).step_by(2) {
                let nj = &pairs[j / 2].0;
                if let (Kind::Scalar { text: a, .. }, Kind::Scalar { text: b, .. }) = (&ni.kind, &nj.kind) {
                    if a == b {
                        self.terrors.push(format!("line {}: mapping key {:?} already defined at line {}", nj.line, b, ni.line));
                    }
                }
            }
        }
    }

    fn string(&mut self, n: &Node, out: &mut String) -> Result<(), String> {
        match &n.kind {
            Kind::Scalar { text, .. } => {
                let (_, r) = resolve(n)?;
                match r {
                    Rv::Null => out.clear(),
                    _ if n.tag.as_deref() == Some("!!binary") => {
                        if let Rv::Str(s) = r {
                            *out = s;
                        }
                    }
                    _ => *out = text.clone(),
                }
                Ok(())
            }
            _ => {
                self.type_error(n, "string");
                Ok(())
            }
        }
    }

    fn boolean(&mut self, n: &Node) -> Result<Option<Option<bool>>, String> {
        // Some(None) = decoded to nil; Some(Some(b)) = value; None = type error recorded.
        match &n.kind {
            Kind::Scalar { text, .. } => match resolve(n)?.1 {
                Rv::Null => Ok(Some(None)),
                Rv::Bool(b) => Ok(Some(Some(b))),
                Rv::Str(s) => match s.as_str() {
                    "y" | "Y" | "yes" | "Yes" | "YES" | "on" | "On" | "ON" => Ok(Some(Some(true))),
                    "n" | "N" | "no" | "No" | "NO" | "off" | "Off" | "OFF" => Ok(Some(Some(false))),
                    _ => {
                        let _ = text;
                        self.type_error(n, "bool");
                        Ok(None)
                    }
                },
                _ => {
                    self.type_error(n, "bool");
                    Ok(None)
                }
            },
            _ => {
                self.type_error(n, "bool");
                Ok(None)
            }
        }
    }

    fn int(&mut self, n: &Node, out: &mut i64) -> Result<(), String> {
        match &n.kind {
            Kind::Scalar { .. } => match resolve(n)?.1 {
                Rv::Null => *out = 0,
                Rv::Int(i) => *out = i,
                Rv::Uint(_) => self.type_error(n, "int"),
                Rv::Float(f) if f <= i64::MAX as f64 && f >= i64::MIN as f64 && !f.is_nan() => *out = f as i64,
                _ => self.type_error(n, "int"),
            },
            _ => self.type_error(n, "int"),
        }
        Ok(())
    }

    /// `StringList.UnmarshalYAML`.
    fn string_list(&mut self, n: &Node, out: &mut Vec<String>) -> Result<(), String> {
        match &n.kind {
            Kind::Scalar { text, .. } => {
                if Self::is_null(n) && n.tag.is_none() {
                    out.clear();
                    return Ok(());
                }
                if text.is_empty() {
                    out.clear();
                } else {
                    *out = vec![text.clone()];
                }
                Ok(())
            }
            Kind::Seq(items) => {
                let mut v = Vec::new();
                for it in items {
                    let mut s = String::new();
                    self.string(it, &mut s)?;
                    v.push(s);
                }
                *out = v;
                Ok(())
            }
            Kind::Map(_) => Err("expected a string or list of strings, got !!map".into()),
        }
    }

    fn address(&mut self, n: &Node) -> Result<Option<Address>, String> {
        match &n.kind {
            Kind::Map(pairs) => {
                self.unique_keys(pairs);
                let mut a = Address::default();
                for (k, v) in pairs {
                    let key = self.key_text(k)?;
                    match key.as_str() {
                        "name" => self.string(v, &mut a.name)?,
                        "address" => self.string_list(v, &mut a.address)?,
                        _ => self.terrors.push(format!("line {}: field {key} not found in type press.Address", k.line)),
                    }
                }
                Ok(Some(a))
            }
            _ if Self::is_null(n) => Ok(None),
            _ => {
                self.type_error(n, "press.Address");
                Ok(None)
            }
        }
    }

    fn key_text(&mut self, k: &Node) -> Result<String, String> {
        match &k.kind {
            Kind::Scalar { text, .. } => Ok(text.clone()),
            _ => {
                self.type_error(k, "string");
                Ok(String::new())
            }
        }
    }

    fn front(&mut self, n: &Node, f: &mut Frontmatter) -> Result<(), String> {
        let pairs = match &n.kind {
            Kind::Map(p) => p,
            _ if Self::is_null(n) => return Ok(()),
            _ => {
                self.type_error(n, "press.Frontmatter");
                return Ok(());
            }
        };
        self.unique_keys(pairs);
        for (k, v) in pairs {
            let key = self.key_text(k)?;
            match key.as_str() {
                "profile" => self.string(v, &mut f.profile)?,
                "lang" => self.string(v, &mut f.lang)?,
                "title" => self.string(v, &mut f.title)?,
                "subtitle" => self.string(v, &mut f.subtitle)?,
                "author" => self.string_list(v, &mut f.author)?,
                "date" => self.string(v, &mut f.date)?,
                "keywords" => self.string_list(v, &mut f.keywords)?,
                "form" => self.string(v, &mut f.form)?,
                "sender" => f.sender = self.address(v)?,
                "recipient" => f.recipient = self.address(v)?,
                "betreff" => self.string(v, &mut f.betreff)?,
                "infoblock" => self.infoblock(v, &mut f.infoblock)?,
                "titlepage" => {
                    if let Some(b) = self.boolean(v)? {
                        f.titlepage = b;
                    }
                }
                "toc" => {
                    if let Some(b) = self.boolean(v)? {
                        f.toc = b;
                    }
                }
                "toc-depth" => self.int(v, &mut f.toc_depth)?,
                "header-left" => self.string(v, &mut f.header_left)?,
                "header-right" => self.string(v, &mut f.header_right)?,
                "footer-left" => self.string(v, &mut f.footer_left)?,
                "footer-right" => self.string(v, &mut f.footer_right)?,
                "data" => self.data(v, &mut f.data)?,
                "meeting_id" => self.string(v, &mut f.meeting_id)?,
                "participants" => self.string_list(v, &mut f.participants)?,
                "duration" => self.string(v, &mut f.duration)?,
                "location" => self.string(v, &mut f.location)?,
                "pdf" => self.pdf(v, &mut f.pdf)?,
                _ => self.terrors.push(format!("line {}: field {key} not found in type press.Frontmatter", k.line)),
            }
        }
        Ok(())
    }

    fn pdf(&mut self, n: &Node, out: &mut PdfOptions) -> Result<(), String> {
        match &n.kind {
            Kind::Map(pairs) => {
                self.unique_keys(pairs);
                for (k, v) in pairs {
                    let key = self.key_text(k)?;
                    match key.as_str() {
                        "standard" => self.string_list(v, &mut out.standard)?,
                        "reproducible" => {
                            if let Some(b) = self.boolean(v)? {
                                out.reproducible = b;
                            }
                        }
                        _ => self.terrors.push(format!("line {}: field {key} not found in type press.PDFOptions", k.line)),
                    }
                }
            }
            _ if Self::is_null(n) => {}
            _ => self.type_error(n, "press.PDFOptions"),
        }
        Ok(())
    }

    fn infoblock(&mut self, n: &Node, out: &mut BTreeMap<String, String>) -> Result<(), String> {
        match &n.kind {
            Kind::Map(pairs) => {
                self.unique_keys(pairs);
                let mut m = BTreeMap::new();
                for (k, v) in pairs {
                    let key = self.key_text(k)?;
                    let mut s = String::new();
                    self.string(v, &mut s)?;
                    m.insert(key, s);
                }
                *out = m;
            }
            _ if Self::is_null(n) => {}
            _ => self.type_error(n, "map[string]string"),
        }
        Ok(())
    }

    fn data(&mut self, n: &Node, out: &mut BTreeMap<String, Json>) -> Result<(), String> {
        match &n.kind {
            Kind::Map(pairs) => {
                self.unique_keys(pairs);
                let mut m = BTreeMap::new();
                for (k, v) in pairs {
                    let key = self.key_text(k)?;
                    let j = self.generic(v)?;
                    m.insert(key, j);
                }
                *out = m;
            }
            _ if Self::is_null(n) => {}
            _ => self.type_error(n, "map[string]interface {}"),
        }
        Ok(())
    }

    fn generic(&mut self, n: &Node) -> Result<Json, String> {
        Ok(match &n.kind {
            Kind::Scalar { .. } => match resolve(n)?.1 {
                Rv::Null => Json::Null,
                Rv::Bool(b) => Json::Bool(b),
                Rv::Int(i) => Json::Int(i),
                Rv::Uint(u) => Json::Uint(u),
                Rv::Float(f) => Json::Float(f),
                Rv::Str(s) => Json::Str(s),
                Rv::Time(t) => Json::Time(t),
            },
            Kind::Seq(items) => {
                let mut v = Vec::new();
                for it in items {
                    v.push(self.generic(it)?);
                }
                Json::Arr(v)
            }
            Kind::Map(pairs) => {
                self.unique_keys(pairs);
                let mut all_str = true;
                for (k, _) in pairs {
                    match &k.kind {
                        Kind::Scalar { .. } => {
                            if !matches!(resolve(k)?.1, Rv::Str(_)) {
                                all_str = false;
                            }
                        }
                        _ => all_str = false,
                    }
                }
                if !all_str {
                    for (_, v) in pairs {
                        self.generic(v)?;
                    }
                    return Ok(Json::GenericMap);
                }
                let mut m = BTreeMap::new();
                let mut merged: Vec<(String, Json)> = Vec::new();
                for (k, v) in pairs {
                    let Kind::Scalar { text, .. } = &k.kind else { continue };
                    if text == "<<" && k.tag.is_none() {
                        match &v.kind {
                            Kind::Map(_) => {
                                if let Json::Obj(o) = self.generic(v)? {
                                    merged.extend(o);
                                }
                            }
                            Kind::Seq(items) => {
                                for it in items {
                                    if let Json::Obj(o) = self.generic(it)? {
                                        merged.extend(o);
                                    }
                                }
                            }
                            _ => return Err("yaml: map merge requires map or sequence of maps as the value".into()),
                        }
                        continue;
                    }
                    let j = self.generic(v)?;
                    m.insert(text.clone(), j);
                }
                for (k, v) in merged {
                    m.entry(k).or_insert(v);
                }
                Json::Obj(m)
            }
        })
    }
}

// ---------------------------------------------------------------------------
// JSON (Go encoding/json, MarshalIndent with two spaces, HTML escaping on)

fn json_string(s: &str, out: &mut String) {
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
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

fn go_float(f: f64) -> Result<String, String> {
    if f.is_nan() {
        return Err("json: unsupported value: NaN".into());
    }
    if f.is_infinite() {
        return Err(format!("json: unsupported value: {}", if f > 0.0 { "+Inf" } else { "-Inf" }));
    }
    let abs = f.abs();
    if abs != 0.0 && (abs < 1e-6 || abs >= 1e21) {
        let e = format!("{f:e}");
        let (mant, exp) = e.split_once('e').unwrap_or((&e, "0"));
        let (sign, digits) = match exp.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', exp),
        };
        let digits = if digits.len() == 1 && sign == '-' { digits.to_owned() } else if digits.len() == 1 { format!("0{digits}") } else { digits.to_owned() };
        // Go prints at least two exponent digits and then strips a leading zero of "e-0N".
        let digits = if sign == '-' { digits.trim_start_matches('0').to_owned() } else { digits };
        let digits = if digits.is_empty() { "0".to_owned() } else { digits };
        return Ok(format!("{mant}e{sign}{digits}"));
    }
    Ok(format!("{f}"))
}

fn write_json(j: &Json, indent: usize, out: &mut String) -> Result<(), String> {
    let pad = |n: usize| "  ".repeat(n);
    match j {
        Json::Null => out.push_str("null"),
        Json::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Json::Int(i) => out.push_str(&i.to_string()),
        Json::Uint(u) => out.push_str(&u.to_string()),
        Json::Float(f) => out.push_str(&go_float(*f)?),
        Json::Str(s) | Json::Time(s) => json_string(s, out),
        Json::Arr(a) => {
            if a.is_empty() {
                out.push_str("[]");
            } else {
                out.push_str("[\n");
                for (i, it) in a.iter().enumerate() {
                    out.push_str(&pad(indent + 1));
                    write_json(it, indent + 1, out)?;
                    if i + 1 < a.len() {
                        out.push(',');
                    }
                    out.push('\n');
                }
                out.push_str(&pad(indent));
                out.push(']');
            }
        }
        Json::Obj(m) => write_obj(m.iter().map(|(k, v)| (k.as_str(), v)).collect(), indent, out)?,
        Json::GenericMap => return Err("json: unsupported type: map[interface {}]interface {}".into()),
        Json::Raw(a) => {
            let name = Json::Str(a.name.clone());
            let list = strs(&a.address);
            let mut entries = vec![("name", &name)];
            if !a.address.is_empty() {
                entries.push(("address", &list));
            }
            write_obj(entries, indent, out)?;
        }
    }
    Ok(())
}

fn write_obj(entries: Vec<(&str, &Json)>, indent: usize, out: &mut String) -> Result<(), String> {
    if entries.is_empty() {
        out.push_str("{}");
        return Ok(());
    }
    let pad = |n: usize| "  ".repeat(n);
    out.push_str("{\n");
    let n = entries.len();
    for (i, (k, v)) in entries.into_iter().enumerate() {
        out.push_str(&pad(indent + 1));
        json_string(k, out);
        out.push_str(": ");
        write_json(v, indent + 1, out)?;
        if i + 1 < n {
            out.push(',');
        }
        out.push('\n');
    }
    out.push_str(&pad(indent));
    out.push('}');
    Ok(())
}

fn strs(v: &[String]) -> Json {
    Json::Arr(v.iter().cloned().map(Json::Str).collect())
}

/// Address keeps its struct field order (name, address), so it is rendered
/// ahead of time rather than through the sorted-key object.
fn address_json(a: &Address) -> Json {
    Json::Raw(a.clone())
}

/// `json.MarshalIndent(front, "", "  ")`: struct field order, `omitempty` rules.
pub fn meta_json(f: &Frontmatter) -> Result<String, String> {
    // Field order is the struct order, not sorted, so build the entries list directly.
    let mut owned: Vec<(&'static str, Json)> = Vec::new();
    owned.push(("profile", Json::Str(f.profile.clone())));
    if !f.lang.is_empty() {
        owned.push(("lang", Json::Str(f.lang.clone())));
    }
    owned.push(("title", Json::Str(f.title.clone())));
    owned.push(("subtitle", Json::Str(f.subtitle.clone())));
    if !f.author.is_empty() {
        owned.push(("author", strs(&f.author)));
    }
    owned.push(("date", Json::Str(f.date.clone())));
    if !f.keywords.is_empty() {
        owned.push(("keywords", strs(&f.keywords)));
    }
    owned.push(("form", Json::Str(f.form.clone())));
    if let Some(a) = &f.sender {
        owned.push(("sender", address_json(a)));
    }
    if let Some(a) = &f.recipient {
        owned.push(("recipient", address_json(a)));
    }
    owned.push(("betreff", Json::Str(f.betreff.clone())));
    if !f.infoblock.is_empty() {
        owned.push(("infoblock", Json::Obj(f.infoblock.iter().map(|(k, v)| (k.clone(), Json::Str(v.clone()))).collect())));
    }
    if let Some(b) = f.titlepage {
        owned.push(("titlepage", Json::Bool(b)));
    }
    if let Some(b) = f.toc {
        owned.push(("toc", Json::Bool(b)));
    }
    if f.toc_depth != 0 {
        owned.push(("toc_depth", Json::Int(f.toc_depth)));
    }
    owned.push(("header_left", Json::Str(f.header_left.clone())));
    owned.push(("header_right", Json::Str(f.header_right.clone())));
    owned.push(("footer_left", Json::Str(f.footer_left.clone())));
    owned.push(("footer_right", Json::Str(f.footer_right.clone())));
    if !f.data.is_empty() {
        owned.push(("data", Json::Obj(f.data.clone())));
    }
    if !f.meeting_id.is_empty() {
        owned.push(("meeting_id", Json::Str(f.meeting_id.clone())));
    }
    if !f.participants.is_empty() {
        owned.push(("participants", strs(&f.participants)));
    }
    if !f.duration.is_empty() {
        owned.push(("duration", Json::Str(f.duration.clone())));
    }
    if !f.location.is_empty() {
        owned.push(("location", Json::Str(f.location.clone())));
    }
    let mut pdf = Vec::new();
    if !f.pdf.standard.is_empty() {
        pdf.push(("standard", strs(&f.pdf.standard)));
    }
    if let Some(b) = f.pdf.reproducible {
        pdf.push(("reproducible", Json::Bool(b)));
    }
    let mut pdf_text = String::new();
    write_obj(pdf.iter().map(|(k, v)| (*k, v)).collect(), 1, &mut pdf_text)?;
    let mut out = String::from("{\n");
    for (i, (k, v)) in owned.iter().enumerate() {
        out.push_str("  ");
        json_string(k, &mut out);
        out.push_str(": ");
        write_json(v, 1, &mut out)?;
        out.push_str(",\n");
        let _ = i;
    }
    out.push_str("  \"pdf\": ");
    out.push_str(&pdf_text);
    out.push_str("\n}");
    Ok(out)
}
