//! `encoding/json`-compatible decoding helpers: case-insensitive keys, `null`
//! is a no-op, unknown keys are ignored, Go's type-error wording.

use serde_json::Value;

pub(crate) fn kind(value: &Value) -> String {
    match value {
        Value::Null => "null".to_owned(),
        Value::Bool(_) => "bool".to_owned(),
        Value::Number(number) => format!("number {number}"),
        Value::String(_) => "string".to_owned(),
        Value::Array(_) => "array".to_owned(),
        Value::Object(_) => "object".to_owned(),
    }
}

pub(crate) fn syntax_error(error: &serde_json::Error) -> String {
    if error.is_eof() {
        "unexpected end of JSON input".to_owned()
    } else {
        // Go prints `invalid character 'x' ...`; the exact byte-level
        // diagnostics of encoding/json are a declared difference.
        error.to_string()
    }
}

pub(crate) struct Fields<'a> {
    pub(crate) values: Vec<Option<&'a Value>>,
}

pub(crate) fn fields<'a>(value: &'a Value, names: &[&str], ty: &str) -> Result<Fields<'a>, String> {
    let mut values = vec![None; names.len()];
    match value {
        Value::Null => {}
        Value::Object(map) => {
            for (key, item) in map {
                let index = names
                    .iter()
                    .position(|name| *name == key)
                    .or_else(|| names.iter().position(|name| name.eq_ignore_ascii_case(key)));
                if let Some(index) = index {
                    values[index] = Some(item);
                }
            }
        }
        other => {
            return Err(format!(
                "json: cannot unmarshal {} into Go value of type {ty}",
                kind(other).split(' ').next().unwrap_or("value")
            ));
        }
    }
    Ok(Fields { values })
}

pub(crate) fn type_error(value: &Value, path: &str, go_type: &str) -> String {
    format!(
        "json: cannot unmarshal {} into Go struct field {path} of type {go_type}",
        kind(value)
    )
}

pub(crate) fn as_int(value: Option<&Value>, path: &str) -> Result<i64, String> {
    match value {
        None | Some(Value::Null) => Ok(0),
        Some(Value::Number(number)) => number
            .as_i64()
            .ok_or_else(|| type_error(&Value::Number(number.clone()), path, "int")),
        Some(other) => Err(type_error(other, path, "int")),
    }
}

pub(crate) fn as_string(value: Option<&Value>, path: &str) -> Result<String, String> {
    match value {
        None | Some(Value::Null) => Ok(String::new()),
        Some(Value::String(text)) => Ok(text.clone()),
        Some(other) => Err(type_error(other, path, "string")),
    }
}

pub(crate) fn as_strings(value: Option<&Value>, path: &str) -> Result<Option<Vec<String>>, String> {
    match value {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Array(items)) => items
            .iter()
            .map(|item| as_string(Some(item), path))
            .collect::<Result<Vec<_>, _>>()
            .map(Some),
        Some(other) => Err(type_error(other, path, "[]string")),
    }
}
