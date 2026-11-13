//! Go `fmt` / `encoding/json` renderings that the AI event stream and its
//! provider diagnostics expose byte for byte.

use serde_json::Value;

/// Go `%v` for a `float64`: shortest round-trip digits in `%g` form with the
/// exponent threshold at 21 (`strconv.FormatFloat(v, 'g', -1, 64)` as `fmt`
/// applies it for `%v`).
#[must_use]
pub fn float_v(value: f64) -> String {
    if value == 0.0 {
        return "0".to_owned();
    }
    if value.is_nan() {
        return "NaN".to_owned();
    }
    if value.is_infinite() {
        return if value > 0.0 { "+Inf" } else { "-Inf" }.to_owned();
    }
    let scientific = format!("{value:e}");
    let (mantissa, exponent) = scientific.split_once('e').unwrap_or((&scientific, "0"));
    let exponent: i32 = exponent.parse().unwrap_or(0);
    if (-4..21).contains(&exponent) {
        return format!("{value}");
    }
    format!(
        "{mantissa}e{}{:02}",
        if exponent < 0 { '-' } else { '+' },
        exponent.abs()
    )
}

/// Go `%v` of a value decoded by `encoding/json` into `any`: maps print with
/// sorted keys as `map[k:v k:v]`, slices as `[a b]`, `null` as `<nil>`, and
/// every number is a `float64`.
#[must_use]
pub fn value_v(value: &Value) -> String {
    match value {
        Value::Null => "<nil>".to_owned(),
        Value::Bool(flag) => flag.to_string(),
        Value::Number(number) => float_v(number.as_f64().unwrap_or(0.0)),
        Value::String(text) => text.clone(),
        Value::Array(items) => {
            let parts: Vec<String> = items.iter().map(value_v).collect();
            format!("[{}]", parts.join(" "))
        }
        Value::Object(map) => {
            let mut keys: Vec<&String> = map.keys().collect();
            keys.sort();
            let parts: Vec<String> = keys
                .iter()
                .map(|key| format!("{key}:{}", value_v(&map[key.as_str()])))
                .collect();
            format!("map[{}]", parts.join(" "))
        }
    }
}

/// What a failed `json.NewDecoder(body).Decode(&map)` leaves printed with
/// `%v`: the decoded object, or the empty map when the body is not an object.
#[must_use]
pub fn error_body_v(body: &[u8]) -> String {
    match serde_json::from_slice::<Value>(body) {
        Ok(value @ Value::Object(_)) => value_v(&value),
        _ => "map[]".to_owned(),
    }
}

/// `encoding/json` replaces every invalid UTF-8 byte with U+FFFD; Rust's lossy
/// conversion would merge a truncated sequence into one.
#[must_use]
pub fn lossy(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len());
    let mut rest = bytes;
    loop {
        match std::str::from_utf8(rest) {
            Ok(valid) => {
                out.push_str(valid);
                return out;
            }
            Err(error) => {
                let (valid, tail) = rest.split_at(error.valid_up_to());
                out.push_str(std::str::from_utf8(valid).unwrap_or_default());
                out.push('\u{FFFD}');
                rest = &tail[1..];
            }
        }
    }
}

/// The text Go's JSON scanner reports for a syntactically invalid document, as
/// far as the agent's tool-call arguments can reach it: a truncated document is
/// "unexpected end of JSON input", anything else names the offending byte.
#[must_use]
pub fn json_syntax_error(raw: &str) -> String {
    match serde_json::from_str::<&serde_json::value::RawValue>(raw) {
        Ok(_) => String::new(),
        Err(error) if error.classify() == serde_json::error::Category::Eof => {
            "unexpected end of JSON input".to_owned()
        }
        Err(error) => {
            let offending = raw
                .lines()
                .nth(error.line().saturating_sub(1))
                .and_then(|line| line.chars().nth(error.column().saturating_sub(1)))
                .unwrap_or('?');
            format!("invalid character {offending:?} looking for beginning of value")
        }
    }
}

/// `json.Compact` with HTML escaping off: drops insignificant whitespace
/// outside strings and keeps everything else verbatim. The input must be valid.
#[must_use]
pub fn compact_json(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len());
    let mut quoted = false;
    let mut escaped = false;
    for character in raw.chars() {
        if quoted {
            out.push(character);
            if escaped {
                escaped = false;
            } else if character == '\\' {
                escaped = true;
            } else if character == '"' {
                quoted = false;
            }
        } else if matches!(character, ' ' | '\t' | '\n' | '\r') {
        } else {
            if character == '"' {
                quoted = true;
            }
            out.push(character);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn float_v_matches_go_percent_v() {
        for (value, want) in [
            (0.0, "0"),
            (500.0, "500"),
            (1.5, "1.5"),
            (1e20, "100000000000000000000"),
            (1e21, "1e+21"),
            (0.0001, "0.0001"),
            (0.00001, "1e-05"),
        ] {
            assert_eq!(float_v(value), want);
        }
    }

    #[test]
    fn value_v_sorts_map_keys_and_prints_nil() {
        let value = json!({"type": "error", "error": {"message": "slow", "code": 429, "x": null}});
        assert_eq!(
            value_v(&value),
            "map[error:map[code:429 message:slow x:<nil>] type:error]"
        );
        assert_eq!(error_body_v(b"not json"), "map[]");
        assert_eq!(error_body_v(b"[1,2]"), "map[]");
    }

    #[test]
    fn compact_and_syntax_error_follow_go() {
        assert_eq!(
            compact_json("{ \"a\" : [1, 2 ,\"x y\"] }"),
            "{\"a\":[1,2,\"x y\"]}"
        );
        assert_eq!(
            json_syntax_error("{\"dir\":"),
            "unexpected end of JSON input"
        );
        assert_eq!(json_syntax_error("{\"dir\":\"a\"}"), "");
    }

    #[test]
    fn lossy_replaces_each_invalid_byte() {
        let truncated = &"é€".as_bytes()[..4];
        assert_eq!(lossy(truncated), "é\u{FFFD}\u{FFFD}");
    }
}
