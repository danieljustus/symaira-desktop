use serde_json::Value;

/// Locates the first differing JSON path so a contract failure is actionable.
pub fn first_difference(path: &str, left: &Value, right: &Value) -> String {
    match (left, right) {
        (Value::Object(a), Value::Object(b)) => {
            for key in a.keys().chain(b.keys()) {
                let (l, r) = (a.get(key).unwrap_or(&Value::Null), b.get(key).unwrap_or(&Value::Null));
                if l != r || a.contains_key(key) != b.contains_key(key) {
                    return first_difference(&format!("{path}/{key}"), l, r);
                }
            }
            format!("{path}: objects equal?")
        }
        (Value::Array(a), Value::Array(b)) if a.len() == b.len() => {
            for (i, (l, r)) in a.iter().zip(b).enumerate() {
                if l != r {
                    return first_difference(&format!("{path}[{i}]"), l, r);
                }
            }
            format!("{path}: arrays equal?")
        }
        (Value::Array(a), Value::Array(b)) => format!("{path}: rust has {} items, go has {}", a.len(), b.len()),
        _ => format!("{path}: rust={left} go={right}"),
    }
}
