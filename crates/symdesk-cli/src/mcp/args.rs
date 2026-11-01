//! Decoder for the anonymous Go argument structs of the MCP tool handlers
//! (`json.Unmarshal(input, &args)` with a struct of string/int64/bool/[]string
//! fields): field matching ignores case, the last duplicate wins, JSON `null`
//! leaves a field unchanged and the first type mismatch is the error.
use std::collections::BTreeMap;

use serde_json::Value;

use super::{go_json_type, go_raw_fields};

#[derive(Clone, Copy, PartialEq, Eq)]
#[allow(
    dead_code,
    reason = "Bool and Strings serve the dataset and autofill tools"
)]
pub(super) enum Kind {
    Str,
    I64,
    Bool,
    Strings,
}

impl Kind {
    fn go_type(self) -> &'static str {
        match self {
            Self::Str => "string",
            Self::I64 => "int64",
            Self::Bool => "bool",
            Self::Strings => "[]string",
        }
    }
}

pub(super) struct Field {
    pub key: &'static str,
    pub name: &'static str,
    pub kind: Kind,
}

pub(super) const fn field(key: &'static str, name: &'static str, kind: Kind) -> Field {
    Field { key, name, kind }
}

#[derive(Default)]
pub(super) struct Args(BTreeMap<&'static str, Value>);

#[allow(
    dead_code,
    reason = "bool and strings serve the dataset and autofill tools"
)]
impl Args {
    pub(super) fn str(&self, key: &str) -> String {
        self.0
            .get(key)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    }

    pub(super) fn i64(&self, key: &str) -> i64 {
        self.0.get(key).and_then(Value::as_i64).unwrap_or_default()
    }

    pub(super) fn bool(&self, key: &str) -> bool {
        self.0.get(key).and_then(Value::as_bool).unwrap_or_default()
    }

    pub(super) fn strings(&self, key: &str) -> Vec<String> {
        self.0
            .get(key)
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .map(|item| item.as_str().unwrap_or_default().to_owned())
                    .collect()
            })
            .unwrap_or_default()
    }
}

fn struct_type(fields: &[Field]) -> String {
    let declaration = fields
        .iter()
        .map(|f| format!("{} {} \"json:\\\"{}\\\"\"", f.name, f.kind.go_type(), f.key))
        .collect::<Vec<_>>()
        .join("; ");
    format!("struct {{ {declaration} }}")
}

fn mismatch(value_type: &str, key: &str, target: &str) -> String {
    format!("json: cannot unmarshal {value_type} into Go struct field .{key} of type {target}")
}

pub(super) fn decode(raw: Option<&str>, fields: &[Field]) -> Result<Args, String> {
    let raw = raw.ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    let mut args = Args::default();
    if raw == "null" {
        return Ok(args);
    }
    if !raw.starts_with('{') {
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type {}",
            go_json_type(raw),
            struct_type(fields)
        ));
    }
    for (key, token) in go_raw_fields(raw)?.0 {
        let Some(field) = fields
            .iter()
            .find(|field| symdesk_vault::dataset::go_equal_fold(field.key, &key))
        else {
            continue;
        };
        let token = token.get();
        if token == "null" {
            continue;
        }
        let value_type = go_json_type(token);
        let value = match field.kind {
            Kind::Str => {
                if value_type != "string" {
                    return Err(mismatch(value_type, field.key, "string"));
                }
                serde_json::from_str::<Value>(token).map_err(|error| error.to_string())?
            }
            Kind::Bool => {
                if value_type != "bool" {
                    return Err(mismatch(value_type, field.key, "bool"));
                }
                Value::Bool(token == "true")
            }
            Kind::I64 => {
                if value_type != "number" {
                    return Err(mismatch(value_type, field.key, "int64"));
                }
                let Ok(number) = token.parse::<i64>() else {
                    return Err(mismatch(&format!("number {token}"), field.key, "int64"));
                };
                Value::from(number)
            }
            Kind::Strings => {
                if value_type != "array" {
                    return Err(mismatch(value_type, field.key, "[]string"));
                }
                let items: Vec<Box<serde_json::value::RawValue>> =
                    serde_json::from_str(token).map_err(|error| error.to_string())?;
                let mut out = Vec::new();
                for item in &items {
                    let item = item.get();
                    if item == "null" {
                        out.push(Value::String(String::new()));
                        continue;
                    }
                    if go_json_type(item) != "string" {
                        return Err(mismatch(go_json_type(item), field.key, "string"));
                    }
                    out.push(
                        serde_json::from_str::<Value>(item).map_err(|error| error.to_string())?,
                    );
                }
                Value::Array(out)
            }
        };
        args.0.insert(field.key, value);
    }
    Ok(args)
}
