//! Go `encoding/json` rendering for migration reports: `MarshalIndent` with
//! two-space indentation, HTML-safe string escapes, RFC 3339 nano timestamps
//! and Go's shortest float formatting.

use serde::{Serialize, Serializer};
use time::OffsetDateTime;

/// A float serialized with Go's `strconv`-style text (through a placeholder
/// string that [`go_indent`] replaces by the bare number).
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct GoFloat(pub f64);

impl Serialize for GoFloat {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&format!("\u{1}F:{}", go_float(self.0)))
    }
}

/// `encoding/json` float64 text: shortest round-trip digits, exponent form
/// only below 1e-6 or from 1e21.
#[must_use]
pub fn go_float(f: f64) -> String {
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        let text = format!("{f:e}");
        return match text.split_once('e') {
            Some((mantissa, exp)) if !exp.starts_with('-') => format!("{mantissa}e+{exp}"),
            _ => text,
        };
    }
    format!("{f}")
}

/// Go `time.Time` JSON (RFC 3339 with nanoseconds, zero time when absent).
#[must_use]
pub fn go_time(t: Option<OffsetDateTime>) -> String {
    let Some(t) = t else {
        return "0001-01-01T00:00:00Z".to_owned();
    };
    let base = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
        t.year(),
        u8::from(t.month()),
        t.day(),
        t.hour(),
        t.minute(),
        t.second()
    );
    let nanos = format!("{:09}", t.nanosecond());
    let nanos = nanos.trim_end_matches('0');
    let fraction = if nanos.is_empty() {
        String::new()
    } else {
        format!(".{nanos}")
    };
    let offset = t.offset();
    let zone = if offset.is_utc() {
        "Z".to_owned()
    } else {
        let seconds = offset.whole_seconds();
        let sign = if seconds < 0 { '-' } else { '+' };
        let abs = seconds.abs();
        format!("{sign}{:02}:{:02}", abs / 3600, abs % 3600 / 60)
    };
    format!("{base}{fraction}{zone}")
}

/// Go `Sub(...).Seconds()`: whole seconds plus the nanosecond remainder.
#[must_use]
pub fn go_seconds(start: Option<OffsetDateTime>, end: Option<OffsetDateTime>) -> f64 {
    let at = |t: Option<OffsetDateTime>| {
        t.map_or(-62_135_596_800_i128 * 1_000_000_000, |t| {
            t.unix_timestamp_nanos()
        })
    };
    let nanos = at(end) - at(start);
    let seconds = nanos / 1_000_000_000;
    let remainder = nanos % 1_000_000_000;
    seconds as f64 + remainder as f64 / 1e9
}

/// `json.MarshalIndent(value, "", "  ")` for report structures.
pub fn go_indent<T: Serialize>(value: &T) -> Result<String, String> {
    let text = serde_json::to_string_pretty(value).map_err(|e| e.to_string())?;
    let mut out = String::with_capacity(text.len());
    let mut chars = text.chars().peekable();
    let mut in_string = false;
    while let Some(c) = chars.next() {
        if in_string {
            match c {
                '\\' => {
                    out.push(c);
                    if let Some(n) = chars.next() {
                        out.push(n);
                    }
                }
                '"' => {
                    in_string = false;
                    out.push(c);
                }
                '<' => out.push_str("\\u003c"),
                '>' => out.push_str("\\u003e"),
                '&' => out.push_str("\\u0026"),
                '\u{2028}' => out.push_str("\\u2028"),
                '\u{2029}' => out.push_str("\\u2029"),
                _ => out.push(c),
            }
        } else {
            if c == '"' {
                in_string = true;
            }
            out.push(c);
        }
    }
    // Float placeholders: `"\u0001F:<number>"` -> `<number>`.
    let marker = "\"\\u0001F:";
    while let Some(start) = out.find(marker) {
        let value_start = start + marker.len();
        let end = out[value_start..]
            .find('"')
            .map(|i| value_start + i)
            .ok_or("unterminated float placeholder")?;
        let number = out[value_start..end].to_owned();
        out.replace_range(start..=end, &number);
    }
    Ok(out)
}
