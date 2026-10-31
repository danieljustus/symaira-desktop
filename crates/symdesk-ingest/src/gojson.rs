//! Ordered JSON values with Go `encoding/json` error semantics.
//!
//! `serde_json::Value` loses duplicate keys and their order; Go decodes object
//! members in wire order (a later duplicate overwrites or merges into the
//! earlier one) and reports the first type error it meets after the complete
//! syntax has been validated. Syntax errors come from the shared Go scanner
//! port in `symdesk-vault`.

use symdesk_vault::go_json_validate_syntax;

#[derive(Clone, Debug, PartialEq)]
pub enum Val {
    Null,
    Bool(bool),
    /// The literal number text.
    Num(String),
    Str(String),
    Arr(Vec<Val>),
    Obj(Vec<(String, Val)>),
}

impl Val {
    /// Go's `UnmarshalTypeError.Value` word for a JSON value.
    #[must_use]
    pub fn word(&self) -> &'static str {
        match self {
            Val::Null => "null",
            Val::Bool(_) => "bool",
            Val::Num(_) => "number",
            Val::Str(_) => "string",
            Val::Arr(_) => "array",
            Val::Obj(_) => "object",
        }
    }
}

/// Parses a complete document: Go's syntax errors first, then the ordered tree.
pub fn parse(data: &[u8]) -> Result<Val, String> {
    go_json_validate_syntax(data)?;
    let mut p = Parser { data, at: 0 };
    p.space();
    Ok(p.value())
}

struct Parser<'a> {
    data: &'a [u8],
    at: usize,
}

impl Parser<'_> {
    fn space(&mut self) {
        while matches!(self.data.get(self.at), Some(b' ' | b'\t' | b'\n' | b'\r')) {
            self.at += 1;
        }
    }

    fn value(&mut self) -> Val {
        match self.data[self.at] {
            b'{' => {
                self.at += 1;
                let mut members = Vec::new();
                self.space();
                if self.data[self.at] == b'}' {
                    self.at += 1;
                    return Val::Obj(members);
                }
                loop {
                    self.space();
                    self.at += 1;
                    let key = self.string();
                    self.space();
                    self.at += 1; // ':'
                    self.space();
                    let value = self.value();
                    members.push((key, value));
                    self.space();
                    let c = self.data[self.at];
                    self.at += 1;
                    if c == b'}' {
                        return Val::Obj(members);
                    }
                }
            }
            b'[' => {
                self.at += 1;
                let mut items = Vec::new();
                self.space();
                if self.data[self.at] == b']' {
                    self.at += 1;
                    return Val::Arr(items);
                }
                loop {
                    self.space();
                    items.push(self.value());
                    self.space();
                    let c = self.data[self.at];
                    self.at += 1;
                    if c == b']' {
                        return Val::Arr(items);
                    }
                }
            }
            b'"' => {
                self.at += 1;
                Val::Str(self.string())
            }
            b't' => {
                self.at += 4;
                Val::Bool(true)
            }
            b'f' => {
                self.at += 5;
                Val::Bool(false)
            }
            b'n' => {
                self.at += 4;
                Val::Null
            }
            _ => {
                let start = self.at;
                while self
                    .data
                    .get(self.at)
                    .is_some_and(|b| matches!(b, b'0'..=b'9' | b'-' | b'+' | b'.' | b'e' | b'E'))
                {
                    self.at += 1;
                }
                Val::Num(String::from_utf8_lossy(&self.data[start..self.at]).into_owned())
            }
        }
    }

    /// Reads the rest of a string literal (opening quote consumed), applying
    /// Go's `unquote`: invalid UTF-8 and lone surrogates become U+FFFD.
    fn string(&mut self) -> String {
        let mut out: Vec<u8> = Vec::new();
        loop {
            let b = self.data[self.at];
            self.at += 1;
            match b {
                b'"' => break,
                b'\\' => {
                    let e = self.data[self.at];
                    self.at += 1;
                    match e {
                        b'b' => out.push(8),
                        b'f' => out.push(12),
                        b'n' => out.push(b'\n'),
                        b'r' => out.push(b'\r'),
                        b't' => out.push(b'\t'),
                        b'u' => {
                            let mut r = self.hex4();
                            if (0xD800..0xDC00).contains(&r)
                                && self.data.get(self.at) == Some(&b'\\')
                                && self.data.get(self.at + 1) == Some(&b'u')
                            {
                                let save = self.at;
                                self.at += 2;
                                let low = self.hex4();
                                if (0xDC00..0xE000).contains(&low) {
                                    r = 0x10000 + ((r - 0xD800) << 10) + (low - 0xDC00);
                                } else {
                                    self.at = save;
                                }
                            }
                            let c = char::from_u32(r).unwrap_or('\u{fffd}');
                            let mut buf = [0u8; 4];
                            out.extend_from_slice(c.encode_utf8(&mut buf).as_bytes());
                        }
                        other => out.push(other),
                    }
                }
                other => out.push(other),
            }
        }
        symdesk_core::gofmt::lossy(&out)
    }

    fn hex4(&mut self) -> u32 {
        let text = std::str::from_utf8(&self.data[self.at..self.at + 4]).unwrap_or("0");
        self.at += 4;
        u32::from_str_radix(text, 16).unwrap_or(0xfffd)
    }
}

/// Go folds struct-field keys case-insensitively.
#[must_use]
pub fn key_matches(key: &str, field: &str) -> bool {
    key.eq_ignore_ascii_case(field)
}

/// A float64 target: `Ok(value)` or the Go number-overflow text.
#[allow(clippy::result_unit_err)]
pub fn float64(literal: &str) -> Result<f64, ()> {
    match literal.parse::<f64>() {
        Ok(v) if v.is_finite() => Ok(v),
        _ => Err(()),
    }
}
