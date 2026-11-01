//! Saved-view row selection (Go `internal/service/views.go`): the filter,
//! relational and set operators, flexible number/date parsing, formulas and
//! rollups that `views exec|embed|export-csv` evaluate over indexed
//! properties.
//!
//! Declared differences from Go: Go iterates property and computed-column maps
//! in unspecified order (formula substitution and computed columns that read
//! other computed columns are order-dependent there); Rust uses key order.
//! `strconv.ParseFloat` hexadecimal floats are not accepted.

use std::collections::BTreeMap;

use serde_json::Value;
use symdesk_index::go_percent_v;
use symdesk_vault::{Filter, FilterGroup, dataset::go_equal_fold};

pub type Row = BTreeMap<String, Value>;

/// Go `fmt.Sprintf("%v", value)` of a decoded value (`<nil>` for null).
pub fn percent_v(value: &Value) -> String {
    go_percent_v(value)
}

/// Go `strconv.ParseFloat(strings.TrimSpace(s), 64)` without hex floats.
pub fn parse_number_flexible(text: &str) -> Option<f64> {
    let text = text.trim();
    if text.is_empty() {
        return None;
    }
    let cleaned: String = if text.contains('_') {
        if !underscore_ok(text) {
            return None;
        }
        text.chars().filter(|c| *c != '_').collect()
    } else {
        text.to_owned()
    };
    let unsigned = cleaned.trim_start_matches(['+', '-']);
    let lowered = unsigned.to_ascii_lowercase();
    let signed = unsigned.len() != cleaned.len();
    if lowered == "nan" && signed {
        return None;
    }
    if lowered.starts_with("0x") {
        return None;
    }
    if !lowered
        .chars()
        .all(|c| c.is_ascii_digit() || matches!(c, '.' | 'e' | '+' | '-') || c.is_ascii_alphabetic())
    {
        return None;
    }
    // Rust accepts the same decimal grammar plus `inf`/`infinity`/`nan`.
    let value: f64 = cleaned.parse().ok()?;
    if value.is_infinite() && !matches!(lowered.as_str(), "inf" | "infinity") {
        return None;
    }
    Some(value)
}

/// Go `strconv.underscoreOK` for the decimal case.
fn underscore_ok(text: &str) -> bool {
    let bytes = text.as_bytes();
    let mut index = 0;
    if matches!(bytes.first(), Some(b'+' | b'-')) {
        index = 1;
    }
    let mut saw = b'^';
    while index < bytes.len() {
        let c = bytes[index];
        if c.is_ascii_digit() {
            saw = b'0';
        } else if c == b'_' {
            if saw != b'0' {
                return false;
            }
            saw = b'_';
        } else {
            if saw == b'_' {
                return false;
            }
            saw = b'!';
        }
        index += 1;
    }
    saw != b'_'
}

fn days_in_month(year: i64, month: i64) -> i64 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        _ => {
            if (year % 4 == 0 && year % 100 != 0) || year % 400 == 0 {
                29
            } else {
                28
            }
        }
    }
}

fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let yoe = year - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

fn digits(bytes: &[u8], at: usize, count: usize) -> Option<i64> {
    let slice = bytes.get(at..at + count)?;
    if !slice.iter().all(u8::is_ascii_digit) {
        return None;
    }
    std::str::from_utf8(slice).ok()?.parse().ok()
}

/// The date portion `2006-01-02`; returns the day count and the next offset.
fn parse_ymd(bytes: &[u8]) -> Option<(i64, usize)> {
    let year = digits(bytes, 0, 4)?;
    if bytes.get(4) != Some(&b'-') {
        return None;
    }
    let month = digits(bytes, 5, 2)?;
    if bytes.get(7) != Some(&b'-') {
        return None;
    }
    let day = digits(bytes, 8, 2)?;
    if !(1..=12).contains(&month) || day < 1 || day > days_in_month(year, month) {
        return None;
    }
    Some((days_from_civil(year, month, day), 10))
}

/// `15:04` or `15:04:05` (+ fractional seconds when seconds are present).
fn parse_clock(bytes: &[u8], with_seconds: bool) -> Option<(i128, usize)> {
    let hour = digits(bytes, 0, 2)?;
    if bytes.get(2) != Some(&b':') {
        return None;
    }
    let minute = digits(bytes, 3, 2)?;
    if hour > 23 || minute > 59 {
        return None;
    }
    let mut at = 5;
    let mut second = 0;
    let mut nanos: i128 = 0;
    if with_seconds {
        if bytes.get(5) != Some(&b':') {
            return None;
        }
        second = digits(bytes, 6, 2)?;
        if second > 59 {
            return None;
        }
        at = 8;
        if matches!(bytes.get(at), Some(b'.' | b',')) {
            let mut end = at + 1;
            while bytes.get(end).is_some_and(u8::is_ascii_digit) {
                end += 1;
            }
            if end > at + 1 {
                let fraction = std::str::from_utf8(&bytes[at + 1..end]).ok()?;
                let mut padded = fraction.to_owned();
                padded.truncate(9);
                while padded.len() < 9 {
                    padded.push('0');
                }
                nanos = padded.parse().ok()?;
                at = end;
            }
        }
    }
    let seconds = i128::from(hour * 3600 + minute * 60 + second);
    Some((seconds * 1_000_000_000 + nanos, at))
}

/// Go `time.Parse` over the six layouts of `parseDateFlexible`; the result
/// is the instant in nanoseconds since the Unix epoch.
pub fn parse_date_flexible(text: &str) -> Option<i128> {
    let text = text.trim();
    if text.is_empty() {
        return None;
    }
    let bytes = text.as_bytes();
    let (days, mut at) = parse_ymd(bytes)?;
    let day_nanos = i128::from(days) * 86_400 * 1_000_000_000;
    if at == bytes.len() {
        return Some(day_nanos);
    }
    let separator = bytes[at];
    if separator != b'T' && separator != b' ' {
        return None;
    }
    at += 1;
    let rest = &bytes[at..];
    if separator == b' ' {
        // "2006-01-02 15:04:05" or "2006-01-02 15:04".
        if let Some((clock, used)) = parse_clock(rest, true)
            && used == rest.len()
        {
            return Some(day_nanos + clock);
        }
        let (clock, used) = parse_clock(rest, false)?;
        return (used == rest.len()).then_some(day_nanos + clock);
    }
    let (clock, used) = parse_clock(rest, true)?;
    let zone = &rest[used..];
    let offset_nanos: i128 = match zone {
        [] | [b'Z'] => 0,
        [sign @ (b'+' | b'-'), tail @ ..] => {
            let hours = digits(tail, 0, 2)?;
            if tail.get(2) != Some(&b':') || tail.len() != 5 {
                return None;
            }
            let minutes = digits(tail, 3, 2)?;
            if hours > 23 || minutes > 59 {
                return None;
            }
            let total = i128::from(hours * 3600 + minutes * 60) * 1_000_000_000;
            if *sign == b'+' { total } else { -total }
        }
        _ => return None,
    };
    Some(day_nanos + clock - offset_nanos)
}

/// Go `extractSetElements` for a decoded property value.
fn extract_set_elements(value: Option<&Value>) -> Vec<String> {
    let trim_keep = |items: Vec<String>| -> Vec<String> {
        items
            .into_iter()
            .map(|item| item.trim().to_owned())
            .filter(|item| !item.is_empty())
            .collect()
    };
    match value {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(items)) => items
            .iter()
            .map(|item| percent_v(item).trim().to_owned())
            .filter(|item| !item.is_empty() && item != "<nil>")
            .collect(),
        Some(Value::String(text)) => {
            let mut text = text.trim();
            if text.is_empty() || text == "<nil>" {
                return Vec::new();
            }
            if text.starts_with('[') && text.ends_with(']') {
                text = text[1..text.len() - 1].trim();
            }
            if text.is_empty() {
                return Vec::new();
            }
            let items: Vec<String> = if text.contains(',') {
                text.split(',').map(str::to_owned).collect()
            } else {
                text.split_whitespace().map(str::to_owned).collect()
            };
            trim_keep(items)
        }
        Some(other) => {
            let text = percent_v(other).trim().to_owned();
            if text.is_empty() || text == "<nil>" {
                Vec::new()
            } else {
                vec![text]
            }
        }
    }
}

fn values_equal(left: &str, right: &str) -> bool {
    if left == right {
        return true;
    }
    if let (Some(a), Some(b)) = (parse_number_flexible(left), parse_number_flexible(right)) {
        #[allow(clippy::float_cmp)]
        return a == b;
    }
    if let (Some(a), Some(b)) = (parse_date_flexible(left), parse_date_flexible(right)) {
        return a == b;
    }
    go_equal_fold(left.trim(), right.trim())
}

fn compare_relational(left: &str, right: &str) -> i32 {
    if let (Some(a), Some(b)) = (parse_number_flexible(left), parse_number_flexible(right)) {
        return if a < b {
            -1
        } else {
            i32::from(a > b)
        };
    }
    if let (Some(a), Some(b)) = (parse_date_flexible(left), parse_date_flexible(right)) {
        return match a.cmp(&b) {
            std::cmp::Ordering::Less => -1,
            std::cmp::Ordering::Equal => 0,
            std::cmp::Ordering::Greater => 1,
        };
    }
    match left.cmp(right) {
        std::cmp::Ordering::Less => -1,
        std::cmp::Ordering::Equal => 0,
        std::cmp::Ordering::Greater => 1,
    }
}

fn lowered(text: &str) -> String {
    symdesk_vault::go_lowercase(text)
}

fn date_compare(
    actual: &str,
    filter: &str,
    date: impl Fn(i128, i128) -> bool,
    text: impl Fn(&str, &str) -> bool,
) -> bool {
    if let (Some(a), Some(f)) = (parse_date_flexible(actual), parse_date_flexible(filter)) {
        return date(a, f);
    }
    text(actual, filter)
}

/// Go `matchesFilter`.
#[allow(clippy::too_many_lines)]
pub fn matches_filter(props: &Row, filter: &Filter) -> bool {
    let value = props.get(&filter.key);
    let exists = value.is_some();
    let actual = match value {
        Some(Value::Null) | None => String::new(),
        Some(other) => percent_v(other),
    };
    let operator = lowered(filter.operator.trim());
    let filter_value = filter.value.trim();
    let present = exists && !actual.is_empty() && actual != "<nil>";
    match operator.as_str() {
        "is_empty" | "empty" => !exists || actual.is_empty() || actual == "<nil>",
        "is_not_empty" | "not_empty" => present,
        "not_equals" | "is_not" | "!=" => {
            if !exists || actual == "<nil>" {
                return true;
            }
            !values_equal(&actual, filter_value)
        }
        "contains" => exists && lowered(&actual).contains(&lowered(filter_value)),
        "not_contains" => !exists || !lowered(&actual).contains(&lowered(filter_value)),
        "starts_with" | "prefix" => exists && lowered(&actual).starts_with(&lowered(filter_value)),
        "ends_with" | "suffix" => exists && lowered(&actual).ends_with(&lowered(filter_value)),
        "greater_than" | "gt" | ">" => {
            exists && !actual.is_empty() && compare_relational(&actual, filter_value) > 0
        }
        "greater_than_or_equal" | "gte" | ">=" => {
            exists && !actual.is_empty() && compare_relational(&actual, filter_value) >= 0
        }
        "less_than" | "lt" | "<" => {
            exists && !actual.is_empty() && compare_relational(&actual, filter_value) < 0
        }
        "less_than_or_equal" | "lte" | "<=" => {
            exists && !actual.is_empty() && compare_relational(&actual, filter_value) <= 0
        }
        "after" => {
            exists
                && !actual.is_empty()
                && date_compare(&actual, filter_value, |a, f| a > f, |a, f| a > f)
        }
        "before" => {
            exists
                && !actual.is_empty()
                && date_compare(&actual, filter_value, |a, f| a < f, |a, f| a < f)
        }
        "on_or_after" => {
            exists
                && !actual.is_empty()
                && date_compare(&actual, filter_value, |a, f| a >= f, |a, f| a >= f)
        }
        "on_or_before" => {
            exists
                && !actual.is_empty()
                && date_compare(&actual, filter_value, |a, f| a <= f, |a, f| a <= f)
        }
        "in" | "not_in" | "contains_all" | "contains_any" | "contains_none" => {
            set_operator(&operator, exists, &actual, value, filter_value)
        }
        _ => {
            if !exists {
                return filter_value.is_empty();
            }
            values_equal(&actual, filter_value)
        }
    }
}

fn set_operator(
    operator: &str,
    exists: bool,
    actual: &str,
    value: Option<&Value>,
    filter_value: &str,
) -> bool {
    let missing = !exists || actual.is_empty();
    let filter_set = extract_set_elements(Some(&Value::String(filter_value.to_owned())));
    let actual_items = extract_set_elements(value);
    let any_match = || {
        actual_items
            .iter()
            .any(|a| filter_set.iter().any(|f| go_equal_fold(a, f)))
    };
    match operator {
        "in" | "contains_any" => !missing && any_match(),
        "not_in" | "contains_none" => missing || !any_match(),
        _ => {
            // contains_all
            if missing {
                return false;
            }
            for f in &filter_set {
                if !actual_items.iter().any(|a| go_equal_fold(a, f)) {
                    return false;
                }
            }
            !filter_set.is_empty()
        }
    }
}

/// Go `matchesGroup`.
pub fn matches_group(props: &Row, group: &FilterGroup) -> bool {
    let all = lowered(&group.operator) != "any";
    let mut matched = 0usize;
    let total = group.filters.len() + group.groups.len();
    for filter in &group.filters {
        if matches_filter(props, filter) {
            matched += 1;
        } else if all {
            return false;
        }
    }
    for child in &group.groups {
        if matches_group(props, child) {
            matched += 1;
        } else if all {
            return false;
        }
    }
    if total == 0 {
        return true;
    }
    matched > 0
}

/// Go `evaluateFormula`: `{key}` placeholders replaced by `%v` values.
pub fn evaluate_formula(formula: &str, props: &Row) -> String {
    let mut result = formula.to_owned();
    for (key, value) in props {
        result = result.replace(&format!("{{{key}}}"), &percent_v(value));
    }
    result
}

/// Go `evaluateRollup` over the link graph and the rows built so far.
pub fn evaluate_rollup(
    rollup: &str,
    path: &str,
    links: &BTreeMap<String, Vec<String>>,
    lookup: &dyn Fn(&str, &str) -> Option<String>,
) -> String {
    let Some(targets) = links.get(path).filter(|targets| !targets.is_empty()) else {
        return String::new();
    };
    if rollup.starts_with("count") {
        return targets.len().to_string();
    }
    if let Some(rest) = rollup.strip_prefix("sum(links.") {
        let property = rest.strip_suffix(')').unwrap_or(rest);
        let mut sum = 0.0;
        for target in targets {
            if let Some(text) = lookup(target, property)
                && let Ok(number) = text.parse::<f64>()
            {
                sum += number;
            }
        }
        return format!("{sum:.2}");
    }
    if let Some(rest) = rollup.strip_prefix("list(links.") {
        let property = rest.strip_suffix(')').unwrap_or(rest);
        let values: Vec<String> = targets
            .iter()
            .filter_map(|target| lookup(target, property))
            .collect();
        return values.join(", ");
    }
    String::new()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dates_follow_the_go_layouts() {
        assert!(parse_date_flexible("2026-01-02").is_some());
        assert!(parse_date_flexible("2026-02-30").is_none());
        assert_eq!(
            parse_date_flexible("2026-01-02T00:00:00Z"),
            parse_date_flexible("2026-01-02")
        );
        assert_eq!(
            parse_date_flexible("2026-01-02T01:00:00+01:00"),
            parse_date_flexible("2026-01-02")
        );
        assert!(parse_date_flexible("2026-01-02 10:30").is_some());
        assert!(parse_date_flexible("2026-01-02T10:30").is_none());
        assert!(parse_date_flexible("2026-1-2").is_none());
    }

    #[test]
    fn numbers_follow_parsefloat() {
        assert_eq!(parse_number_flexible(" 1_000 "), Some(1000.0));
        assert_eq!(parse_number_flexible("1e3"), Some(1000.0));
        assert_eq!(parse_number_flexible("1e999"), None);
        assert_eq!(parse_number_flexible("-inf"), Some(f64::NEG_INFINITY));
        assert_eq!(parse_number_flexible("+nan"), None);
        assert_eq!(parse_number_flexible("_1"), None);
        assert_eq!(parse_number_flexible("abc"), None);
        assert_eq!(parse_number_flexible(""), None);
    }

    #[test]
    fn set_operators_split_like_go() {
        assert_eq!(
            extract_set_elements(Some(&Value::String("[a b, c]".to_owned()))),
            vec!["a b".to_owned(), "c".to_owned()]
        );
        assert_eq!(
            extract_set_elements(Some(&Value::String("[a b c]".to_owned()))),
            vec!["a".to_owned(), "b".to_owned(), "c".to_owned()]
        );
    }
}
