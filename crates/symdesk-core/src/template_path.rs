//! Storage-path templates, ported from Go `internal/templatepath`.
//!
//! A template such as `documents/{correspondent}/{document_date:%Y}/{title}`
//! is compiled once and evaluated against frontmatter field values. `{field}`
//! substitutes the sanitized value; `{field:%fmt}` formats the date fields
//! (`document_date`, `created`) with a small strftime subset.
//!
//! Known, deliberate differences from the Go source:
//!
//! * Path cleaning only treats `/` as a separator. Go's `filepath.Clean` and
//!   `filepath.Ext` additionally treat `\` (and drive letters) specially on
//!   Windows; the frozen fixture contains no such input.
//! * Go applies its strftime directive table by ranging over a map, so a
//!   format whose substituted output completes another directive (`%%a`
//!   becomes `%Sun`, then `%S`) depends on the random iteration order. Rust
//!   scans left to right and substitutes each directive once.

use std::{
    collections::{BTreeMap, HashSet},
    fmt,
};

use crate::config::quote_artifact;

const VALID_FIELDS: [&str; 8] = [
    "correspondent",
    "document_type",
    "document_date",
    "person",
    "category",
    "asn",
    "title",
    "created",
];

const MONTH_NAMES: [&str; 12] = [
    "January",
    "February",
    "March",
    "April",
    "May",
    "June",
    "July",
    "August",
    "September",
    "October",
    "November",
    "December",
];

const WEEKDAY_NAMES: [&str; 7] = [
    "Sunday",
    "Monday",
    "Tuesday",
    "Wednesday",
    "Thursday",
    "Friday",
    "Saturday",
];

/// Why [`Template::compile`] rejected a template. `Display` is the exact Go
/// error text.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum TemplateError {
    /// The template (after trimming) climbs out of the vault root.
    EscapesVaultRoot(String),
    /// A `{name}` reference names no supported field.
    UnknownField(String),
}

impl fmt::Display for TemplateError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::EscapesVaultRoot(raw) => write!(
                formatter,
                "template escapes vault root: {}",
                quote_artifact(raw)
            ),
            Self::UnknownField(name) => {
                write!(formatter, "unknown template field {}", quote_artifact(name))
            }
        }
    }
}

impl std::error::Error for TemplateError {}

#[derive(Clone, Debug, Eq, PartialEq)]
enum Segment {
    Literal(String),
    Field { name: String, format: String },
}

/// A compiled storage-path template.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Template {
    raw: String,
    segments: Vec<Segment>,
}

/// One `{name}` / `{name:format}` match: byte range plus captures.
struct FieldMatch<'a> {
    start: usize,
    end: usize,
    name: &'a str,
    format: &'a str,
}

/// Leftmost match of Go's `\{(\w+)(?::([^}]*))?\}` where `\w` is ASCII only.
fn find_field(text: &str) -> Option<FieldMatch<'_>> {
    let bytes = text.as_bytes();
    let mut from = 0;
    while let Some(offset) = text[from..].find('{') {
        let start = from + offset;
        let name_start = start + 1;
        let mut name_end = name_start;
        while name_end < bytes.len()
            && (bytes[name_end].is_ascii_alphanumeric() || bytes[name_end] == b'_')
        {
            name_end += 1;
        }
        if name_end > name_start {
            match bytes.get(name_end) {
                Some(b'}') => {
                    return Some(FieldMatch {
                        start,
                        end: name_end + 1,
                        name: &text[name_start..name_end],
                        format: "",
                    });
                }
                Some(b':') => {
                    if let Some(close) = text[name_end + 1..].find('}') {
                        let close = name_end + 1 + close;
                        return Some(FieldMatch {
                            start,
                            end: close + 1,
                            name: &text[name_start..name_end],
                            format: &text[name_end + 1..close],
                        });
                    }
                }
                _ => {}
            }
        }
        from = start + 1;
    }
    None
}

/// Go `strings.TrimSpace`: trims Unicode White_Space.
fn trim_space(value: &str) -> &str {
    value.trim_matches(char::is_whitespace)
}

/// Go `filepath.Clean` for `/`-separated paths.
fn clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let rooted = path.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for element in path.split('/') {
        match element {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            other => parts.push(other),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, true) => "/".to_owned(),
        (true, false) => format!("/{joined}"),
        (false, true) => ".".to_owned(),
        (false, false) => joined,
    }
}

impl Template {
    /// Parses `raw` (after trimming surrounding whitespace). An empty
    /// template is valid and evaluates to the empty string.
    ///
    /// # Errors
    ///
    /// [`TemplateError::EscapesVaultRoot`] when the cleaned template starts
    /// with or contains a `..` element, [`TemplateError::UnknownField`] for
    /// the first unsupported `{name}`.
    pub fn compile(raw: &str) -> Result<Self, TemplateError> {
        let raw = trim_space(raw);
        if raw.is_empty() {
            return Ok(Self {
                raw: String::new(),
                segments: Vec::new(),
            });
        }

        let cleaned = clean(raw);
        if cleaned == ".." || cleaned.starts_with("../") || cleaned.split('/').any(|p| p == "..") {
            return Err(TemplateError::EscapesVaultRoot(raw.to_owned()));
        }

        let mut segments = Vec::new();
        let mut remaining = raw;
        while !remaining.is_empty() {
            let Some(found) = find_field(remaining) else {
                segments.push(Segment::Literal(remaining.to_owned()));
                break;
            };
            if found.start > 0 {
                segments.push(Segment::Literal(remaining[..found.start].to_owned()));
            }
            if !VALID_FIELDS.contains(&found.name) {
                return Err(TemplateError::UnknownField(found.name.to_owned()));
            }
            segments.push(Segment::Field {
                name: found.name.to_owned(),
                format: found.format.to_owned(),
            });
            remaining = &remaining[found.end..];
        }

        Ok(Self {
            raw: raw.to_owned(),
            segments,
        })
    }

    /// The trimmed, uncompiled template text.
    #[must_use]
    pub fn raw(&self) -> &str {
        &self.raw
    }

    /// Whether no template is configured.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.raw.is_empty()
    }

    /// Resolves the template against `fields` and returns the cleaned,
    /// slash-separated relative path. Missing or empty fields become
    /// `unknown`; unparsable dates fall back to the sanitized raw value.
    #[must_use]
    pub fn eval(&self, fields: &BTreeMap<String, String>) -> String {
        if self.segments.is_empty() {
            return String::new();
        }
        let mut built = String::new();
        for segment in &self.segments {
            match segment {
                Segment::Literal(text) => built.push_str(text),
                Segment::Field { name, format } => {
                    let value = fields.get(name).map_or("", String::as_str);
                    if (name == "document_date" || name == "created")
                        && let Some(formatted) = format_date_field(value, format)
                        && !formatted.is_empty()
                    {
                        built.push_str(&formatted);
                        continue;
                    }
                    if value.is_empty() {
                        built.push_str("unknown");
                    } else {
                        built.push_str(&sanitize_path_segment(value));
                    }
                }
            }
        }
        if built.is_empty() {
            return String::new();
        }
        clean(&built)
    }
}

/// Returns `target`, or `target` with `_N` inserted before its extension for
/// the smallest `N >= 1` that is not in `known`. The caller tracks the paths
/// allocated so far.
#[must_use]
pub fn disambiguate(target: &str, known: &HashSet<String>) -> String {
    let mut candidate = target.to_owned();
    let extension = path_ext(target);
    let base = &target[..target.len() - extension.len()];
    let mut counter = 1_u64;
    while known.contains(&candidate) {
        candidate = format!("{base}_{counter}{extension}");
        counter += 1;
    }
    candidate
}

/// Go `filepath.Ext` for `/`-separated paths: the suffix starting at the
/// final dot of the last element, or empty.
fn path_ext(path: &str) -> &str {
    for (index, byte) in path.bytes().enumerate().rev() {
        if byte == b'/' {
            break;
        }
        if byte == b'.' {
            return &path[index..];
        }
    }
    ""
}

/// Replaces characters that are unsafe in a path segment.
fn sanitize_path_segment(value: &str) -> String {
    let mut built = String::new();
    for character in value.chars() {
        if character == '/' || character == '\\' {
            built.push('-');
        } else if character.is_control() || (character == ':' && built.is_empty()) {
            built.push('_');
        } else {
            built.push(character);
        }
    }
    let trimmed = built.trim_matches(|c| c == '.' || c == ' ');
    if trimmed.is_empty() {
        "unknown".to_owned()
    } else {
        trimmed.to_owned()
    }
}

fn format_date_field(value: &str, format: &str) -> Option<String> {
    if value.is_empty() {
        return None;
    }
    let date = parse_date(value)?;
    let format = if format.is_empty() {
        "%Y-%m-%d"
    } else {
        format
    };
    Some(strftime(&date, format))
}

/// Wall-clock fields of a parsed date; the zone offset only had to be valid.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
struct Date {
    year: u32,
    month: u32,
    day: u32,
    hour: u32,
    minute: u32,
    second: u32,
}

#[derive(Clone, Copy, Eq, PartialEq)]
enum Zone {
    None,
    LiteralZ,
    Rfc3339,
}

/// Go layouts tried by `parseDate`, in order: the first match wins.
const LAYOUTS: [(bool, bool, Zone); 5] = [
    // (has_time, has_day, zone)
    (true, true, Zone::Rfc3339),  // 2006-01-02T15:04:05Z07:00
    (true, true, Zone::LiteralZ), // 2006-01-02T15:04:05Z
    (true, true, Zone::None),     // 2006-01-02T15:04:05
    (false, true, Zone::None),    // 2006-01-02
    (false, false, Zone::None),   // 2006-01
];

fn parse_date(value: &str) -> Option<Date> {
    LAYOUTS
        .iter()
        .find_map(|&(has_time, has_day, zone)| parse_layout(value, has_time, has_day, zone))
}

fn digit(bytes: &[u8], index: usize) -> bool {
    bytes.get(index).is_some_and(u8::is_ascii_digit)
}

fn two(bytes: &[u8], index: usize) -> Option<u32> {
    if digit(bytes, index) && digit(bytes, index + 1) {
        Some(u32::from(bytes[index] - b'0') * 10 + u32::from(bytes[index + 1] - b'0'))
    } else {
        None
    }
}

fn days_in_month(year: u32, month: u32) -> u32 {
    match month {
        2 if year.is_multiple_of(4) && (!year.is_multiple_of(100) || year.is_multiple_of(400)) => {
            29
        }
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// Mirrors `time.Parse` for one fixed layout: `2006-01[-02[T15:04:05<zone>]]`.
/// The hour accepts one or two digits, every other field exactly two; a
/// fraction (`.` or `,` plus digits) is accepted after the seconds.
fn parse_layout(value: &str, has_time: bool, has_day: bool, zone: Zone) -> Option<Date> {
    let bytes = value.as_bytes();
    if bytes.len() < 4 || !bytes[..4].iter().all(u8::is_ascii_digit) {
        return None;
    }
    let year = value[..4].parse::<u32>().ok()?;
    let mut at = 4;

    if bytes.get(at) != Some(&b'-') {
        return None;
    }
    at += 1;
    let month = two(bytes, at)?;
    if !(1..=12).contains(&month) {
        return None;
    }
    at += 2;

    let mut day = 1;
    let (mut hour, mut minute, mut second) = (0, 0, 0);
    if has_day {
        if bytes.get(at) != Some(&b'-') {
            return None;
        }
        at += 1;
        day = two(bytes, at)?;
        at += 2;
    }
    if has_time {
        if bytes.get(at) != Some(&b'T') {
            return None;
        }
        at += 1;
        if !digit(bytes, at) {
            return None;
        }
        if digit(bytes, at + 1) {
            hour = two(bytes, at)?;
            at += 2;
        } else {
            hour = u32::from(bytes[at] - b'0');
            at += 1;
        }
        if hour >= 24 {
            return None;
        }
        if bytes.get(at) != Some(&b':') {
            return None;
        }
        at += 1;
        minute = two(bytes, at)?;
        if minute >= 60 {
            return None;
        }
        at += 2;
        if bytes.get(at) != Some(&b':') {
            return None;
        }
        at += 1;
        second = two(bytes, at)?;
        if second >= 60 {
            return None;
        }
        at += 2;

        // Fractional seconds are consumed whenever the next layout element is
        // not itself a fraction, which holds for all three time layouts.
        if matches!(bytes.get(at), Some(b'.' | b',')) && digit(bytes, at + 1) {
            at += 2;
            while digit(bytes, at) {
                at += 1;
            }
        }

        match zone {
            Zone::None => {}
            Zone::LiteralZ => {
                if bytes.get(at) != Some(&b'Z') {
                    return None;
                }
                at += 1;
            }
            Zone::Rfc3339 => {
                if bytes.get(at) == Some(&b'Z') {
                    at += 1;
                } else {
                    if bytes.len() < at + 6 || bytes[at + 3] != b':' {
                        return None;
                    }
                    if !matches!(bytes[at], b'+' | b'-') {
                        return None;
                    }
                    let zone_hours = two(bytes, at + 1)?;
                    let zone_minutes = two(bytes, at + 4)?;
                    if zone_hours > 24 || zone_minutes > 60 {
                        return None;
                    }
                    at += 6;
                }
            }
        }
    }
    if at != bytes.len() {
        return None;
    }
    if day < 1 || day > days_in_month(year, month) {
        return None;
    }
    Some(Date {
        year,
        month,
        day,
        hour,
        minute,
        second,
    })
}

/// Day of week with Sunday = 0, proleptic Gregorian.
fn weekday(date: &Date) -> usize {
    let year = i64::from(date.year) - i64::from(date.month <= 2);
    let era = year.div_euclid(400);
    let year_of_era = year.rem_euclid(400);
    let month = i64::from(date.month);
    let day_of_year =
        (153 * (if month > 2 { month - 3 } else { month + 9 }) + 2) / 5 + i64::from(date.day) - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    let days_since_epoch = era * 146_097 + day_of_era - 719_468;
    // 1970-01-01 was a Thursday.
    usize::try_from((days_since_epoch + 4).rem_euclid(7)).expect("weekday index is 0..7")
}

/// The limited strftime subset: `%Y %y %m %d %H %M %S %b %B %a %A`. Unknown
/// directives and stray percent signs are kept verbatim.
fn strftime(date: &Date, format: &str) -> String {
    let mut output = String::new();
    let mut rest = format;
    while let Some(percent) = rest.find('%') {
        output.push_str(&rest[..percent]);
        rest = &rest[percent..];
        let replacement = rest
            .as_bytes()
            .get(1)
            .and_then(|directive| match directive {
                b'Y' => Some(format!("{:04}", date.year)),
                b'y' => Some(format!("{:02}", date.year % 100)),
                b'm' => Some(format!("{:02}", date.month)),
                b'd' => Some(format!("{:02}", date.day)),
                b'H' => Some(format!("{:02}", date.hour)),
                b'M' => Some(format!("{:02}", date.minute)),
                b'S' => Some(format!("{:02}", date.second)),
                b'b' => Some(MONTH_NAMES[date.month as usize - 1][..3].to_owned()),
                b'B' => Some(MONTH_NAMES[date.month as usize - 1].to_owned()),
                b'a' => Some(WEEKDAY_NAMES[weekday(date)][..3].to_owned()),
                b'A' => Some(WEEKDAY_NAMES[weekday(date)].to_owned()),
                _ => None,
            });
        if let Some(replacement) = replacement {
            output.push_str(&replacement);
            rest = &rest[2..];
        } else {
            output.push('%');
            rest = &rest[1..];
        }
    }
    output.push_str(rest);
    output
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fields(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
        pairs
            .iter()
            .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
            .collect()
    }

    #[test]
    fn evaluates_the_documented_example() {
        let template =
            Template::compile("documents/{correspondent}/{document_date:%Y}/{title}").unwrap();
        let path = template.eval(&fields(&[
            ("correspondent", "ACME GmbH"),
            ("document_date", "2026-03-05"),
            ("title", "Report"),
        ]));
        assert_eq!(path, "documents/ACME GmbH/2026/Report");
    }

    #[test]
    fn weekday_matches_known_dates() {
        let date = |year, month, day| Date {
            year,
            month,
            day,
            hour: 0,
            minute: 0,
            second: 0,
        };
        assert_eq!(weekday(&date(1970, 1, 1)), 4);
        assert_eq!(weekday(&date(2026, 3, 5)), 4);
        assert_eq!(weekday(&date(2000, 2, 29)), 2);
        assert_eq!(weekday(&date(1, 1, 1)), 1);
    }

    #[test]
    fn clean_matches_go_semantics() {
        for (input, want) in [
            ("", "."),
            ("a//b/./c/", "a/b/c"),
            ("a/../..", ".."),
            ("/../x", "/x"),
            ("/..", "/"),
            ("../a", "../a"),
            ("a/b/../../..", ".."),
        ] {
            assert_eq!(clean(input), want, "{input}");
        }
    }

    #[test]
    fn disambiguate_inserts_suffix_before_extension() {
        let known: HashSet<String> = ["a/b.pdf", "a/b_1.pdf"].map(String::from).into();
        assert_eq!(disambiguate("a/b.pdf", &known), "a/b_2.pdf");
        assert_eq!(
            disambiguate("a.d/b", &["a.d/b".to_owned()].into()),
            "a.d/b_1"
        );
    }
}
