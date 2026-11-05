//! Go `json.NewDecoder(...).DisallowUnknownFields().Decode(&request)` for the
//! request structs of `internal/selfhost`.
//!
//! The Go server validates the first JSON value of the (limit+1 byte) body as a
//! whole before it assigns any field, so an incomplete document reports
//! `unexpected EOF` even when an earlier member is already wrong. Members are
//! then applied in wire order: keys match case-insensitively, the last
//! duplicate wins, `null` leaves a field alone and the first type or
//! unknown-field error is the one reported. Trailing data is ignored.

use std::collections::BTreeMap;

use serde::de::{Deserializer as _, MapAccess, Visitor};
use serde_json::Value;

/// Why a body could not be decoded.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Failure {
    /// No JSON value at all (`io.EOF`).
    Empty,
    /// The value was cut off (`io.ErrUnexpectedEOF`).
    Incomplete,
    /// Go's own wording.
    Message(String),
}

impl Failure {
    /// The text after `invalid JSON: `.
    pub(crate) fn text(&self) -> String {
        match self {
            Self::Empty => "EOF".to_owned(),
            Self::Incomplete => "unexpected EOF".to_owned(),
            Self::Message(message) => message.clone(),
        }
    }
}

#[derive(Clone, Copy)]
pub(crate) enum Kind {
    Int,
    Str,
    Bool,
    StrList,
}

/// A request struct: its Go type name and JSON fields.
pub(crate) struct Spec {
    pub name: &'static str,
    /// `DisallowUnknownFields`.
    pub strict: bool,
    pub fields: &'static [(&'static str, Kind)],
}

pub(crate) const AI_TRANSFORM: Spec = Spec {
    name: "aiTransformRequest",
    strict: true,
    fields: &[("text", Kind::Str), ("intent", Kind::Str)],
};
pub(crate) const AI_ASK: Spec = Spec {
    name: "aiAskRequest",
    strict: true,
    fields: &[("query", Kind::Str), ("notebook", Kind::Str)],
};
pub(crate) const WORKER_FAIL: Spec = Spec {
    name: "failRequest",
    strict: true,
    fields: &[
        ("job_id", Kind::Str),
        ("worker_id", Kind::Str),
        ("error", Kind::Str),
        ("retry", Kind::Bool),
    ],
};
pub(crate) const WORKER_COMPLETE: Spec = Spec {
    name: "completionRequest",
    strict: true,
    fields: &[
        ("job_id", Kind::Str),
        ("worker_id", Kind::Str),
        ("text", Kind::Str),
        ("engine", Kind::Str),
        ("model", Kind::Str),
    ],
};
pub(crate) const SHARE_CREATE: Spec = Spec {
    name: "",
    strict: false,
    fields: &[("path", Kind::Str), ("expiry", Kind::Int)],
};
pub(crate) const COMMAND: Spec = Spec {
    name: "commandRequest",
    strict: true,
    fields: &[("arguments", Kind::StrList), ("stdin", Kind::Str)],
};

/// Assigned field values by JSON name; a `StrList` set to `null` is absent.
#[derive(Default)]
pub(crate) struct Decoded {
    values: BTreeMap<&'static str, Value>,
}

impl Decoded {
    pub(crate) fn text(&self, name: &str) -> String {
        match self.values.get(name) {
            Some(Value::String(text)) => text.clone(),
            _ => String::new(),
        }
    }

    pub(crate) fn int(&self, name: &str) -> i64 {
        self.values
            .get(name)
            .and_then(Value::as_i64)
            .unwrap_or_default()
    }

    pub(crate) fn flag(&self, name: &str) -> bool {
        matches!(self.values.get(name), Some(Value::Bool(true)))
    }

    pub(crate) fn list(&self, name: &str) -> Option<Vec<String>> {
        match self.values.get(name) {
            Some(Value::Array(items)) => Some(
                items
                    .iter()
                    .map(|item| item.as_str().unwrap_or_default().to_owned())
                    .collect(),
            ),
            _ => None,
        }
    }

    pub(crate) fn is_set(&self, name: &str) -> bool {
        self.values.contains_key(name)
    }
}

fn is_space(byte: u8) -> bool {
    matches!(byte, b' ' | b'\t' | b'\r' | b'\n')
}

struct Members(Vec<(String, Value)>);

impl<'de> Visitor<'de> for Members {
    type Value = Option<Vec<(String, Value)>>;

    fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("a JSON value")
    }

    fn visit_map<A: MapAccess<'de>>(mut self, mut map: A) -> Result<Self::Value, A::Error> {
        while let Some(entry) = map.next_entry::<String, Value>()? {
            self.0.push(entry);
        }
        Ok(Some(self.0))
    }
}

fn kind_of(value: &Value) -> &'static str {
    match value {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

/// Go `Decoder.Decode` of the first value of `data` into `spec`.
pub(crate) fn decode(spec: &Spec, data: &[u8]) -> Result<Decoded, Failure> {
    if data.iter().all(|byte| is_space(*byte)) {
        return Err(Failure::Empty);
    }
    match symdesk_vault::go_json_validate_syntax(data) {
        Ok(()) => {}
        Err(message) if message == "unexpected end of JSON input" => {
            return Err(Failure::Incomplete);
        }
        // A complete first value followed by more data is fine for a Decoder.
        Err(message) if message.contains("after top-level value") => {}
        Err(message) => return Err(Failure::Message(message)),
    }
    // The first value only, with members in wire order (duplicates kept).
    let start = data.iter().position(|byte| !is_space(*byte)).unwrap_or(0);
    let mut stream = serde_json::Deserializer::from_slice(&data[start..]);
    let first = stream.deserialize_any(MembersOrValue);
    let members = match first {
        Ok(Parsed::Object(members)) => members,
        Ok(Parsed::Other(Value::Null)) => return Ok(Decoded::default()),
        Ok(Parsed::Other(other)) => {
            return Err(Failure::Message(format!(
                "json: cannot unmarshal {} into Go value of type selfhost.{}",
                kind_of(&other),
                spec.name
            )));
        }
        Err(error) => return Err(Failure::Message(error.to_string())),
    };
    let mut decoded = Decoded::default();
    let mut first_error: Option<String> = None;
    let mut note = |message: String| {
        first_error.get_or_insert(message);
    };
    for (key, item) in members {
        let field = spec
            .fields
            .iter()
            .find(|(name, _)| *name == key)
            .or_else(|| {
                spec.fields
                    .iter()
                    .find(|(name, _)| name.eq_ignore_ascii_case(&key))
            });
        let Some((name, kind)) = field else {
            if !spec.strict {
                continue;
            }
            note(format!(
                "json: unknown field {}",
                serde_json::to_string(&key).unwrap_or_default()
            ));
            continue;
        };
        let mismatch = |value: &Value, go_type: &str| {
            format!(
                "json: cannot unmarshal {} into Go struct field {}.{name} of type {go_type}",
                kind_of(value),
                spec.name
            )
        };
        match (kind, &item) {
            (Kind::StrList, Value::Null) => {
                decoded.values.remove(name);
            }
            (_, Value::Null) => {}
            (Kind::Str, Value::String(_)) | (Kind::Bool, Value::Bool(_)) => {
                decoded.values.insert(name, item);
            }
            (Kind::Int, Value::Number(number)) if number.is_i64() => {
                decoded.values.insert(name, item);
            }
            (Kind::Int, other) => note(mismatch(other, "int")),
            (Kind::Str, other) => note(mismatch(other, "string")),
            (Kind::Bool, other) => note(mismatch(other, "bool")),
            (Kind::StrList, Value::Array(elements)) => {
                let mut list = Vec::with_capacity(elements.len());
                for element in elements {
                    match element {
                        Value::String(_) => list.push(element.clone()),
                        Value::Null => list.push(Value::String(String::new())),
                        other => {
                            note(mismatch(other, "string"));
                            list.push(Value::String(String::new()));
                        }
                    }
                }
                decoded.values.insert(name, Value::Array(list));
            }
            (Kind::StrList, other) => note(mismatch(other, "[]string")),
        }
    }
    match first_error {
        Some(message) => Err(Failure::Message(message)),
        None => Ok(decoded),
    }
}

enum Parsed {
    Object(Vec<(String, Value)>),
    Other(Value),
}

struct MembersOrValue;

impl<'de> Visitor<'de> for MembersOrValue {
    type Value = Parsed;

    fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("a JSON value")
    }

    fn visit_map<A: MapAccess<'de>>(self, map: A) -> Result<Self::Value, A::Error> {
        Ok(Parsed::Object(
            Members(Vec::new()).visit_map(map)?.unwrap_or_default(),
        ))
    }

    fn visit_unit<E>(self) -> Result<Self::Value, E> {
        Ok(Parsed::Other(Value::Null))
    }

    fn visit_bool<E>(self, value: bool) -> Result<Self::Value, E> {
        Ok(Parsed::Other(Value::Bool(value)))
    }

    fn visit_i64<E>(self, value: i64) -> Result<Self::Value, E> {
        Ok(Parsed::Other(Value::from(value)))
    }

    fn visit_u64<E>(self, value: u64) -> Result<Self::Value, E> {
        Ok(Parsed::Other(Value::from(value)))
    }

    fn visit_f64<E>(self, value: f64) -> Result<Self::Value, E> {
        Ok(Parsed::Other(Value::from(value)))
    }

    fn visit_str<E>(self, value: &str) -> Result<Self::Value, E> {
        Ok(Parsed::Other(Value::String(value.to_owned())))
    }

    fn visit_seq<A: serde::de::SeqAccess<'de>>(self, mut seq: A) -> Result<Self::Value, A::Error> {
        while seq.next_element::<serde::de::IgnoredAny>()?.is_some() {}
        Ok(Parsed::Other(Value::Array(Vec::new())))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_wording() {
        let text = |data: &[u8]| decode(&AI_TRANSFORM, data).err().map(|f| f.text());
        assert_eq!(text(b""), Some("EOF".to_owned()));
        assert_eq!(text(b"  "), Some("EOF".to_owned()));
        assert_eq!(text(b"{"), Some("unexpected EOF".to_owned()));
        assert_eq!(text(br#"{"x":1"#), Some("unexpected EOF".to_owned()));
        assert_eq!(
            text(br#"{"text":"a","x":1}"#),
            Some("json: unknown field \"x\"".to_owned())
        );
        assert_eq!(
            text(br#"{"text":5}"#),
            Some(
                "json: cannot unmarshal number into Go struct field aiTransformRequest.text of type string"
                    .to_owned()
            )
        );
        assert_eq!(
            text(b"[1]"),
            Some(
                "json: cannot unmarshal array into Go value of type selfhost.aiTransformRequest"
                    .to_owned()
            )
        );
        assert_eq!(
            text(b"{]"),
            Some("invalid character ']' looking for beginning of object key string".to_owned())
        );
        // The cut-off document wins over an earlier unknown field.
        assert_eq!(text(br#"{"x":"aaa"#), Some("unexpected EOF".to_owned()));
        let decoded = decode(&AI_TRANSFORM, br#"{"TEXT":"a","text":"b"} trailing"#).unwrap();
        assert_eq!(decoded.text("text"), "b");
        assert!(decode(&AI_TRANSFORM, b"null").is_ok());
    }
}
