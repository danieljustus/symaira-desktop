#![deny(unsafe_code)]

//! Portable search-query grammar and inclusive UTC date filters.

use std::{error::Error, fmt};

use regex::Regex;
use time::{
    Date, Duration, OffsetDateTime, Time, format_description::well_known::Rfc3339,
    macros::format_description,
};

const DATE_FORMAT: &[time::format_description::FormatItem<'static>] =
    format_description!("[year]-[month]-[day]");

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Field {
    Path,
    Tag,
    Type,
    Status,
    Filename,
    FileType,
    Created,
    Modified,
    Index,
}

impl Field {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Path => "path",
            Self::Tag => "tag",
            Self::Type => "type",
            Self::Status => "status",
            Self::Filename => "filename",
            Self::FileType => "filetype",
            Self::Created => "created",
            Self::Modified => "modified",
            Self::Index => "index",
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Filter {
    pub field: Field,
    pub value: String,
    pub negated: bool,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Term {
    pub value: String,
    pub phrase: bool,
    pub negated: bool,
}

#[derive(Clone, Debug)]
pub struct RegexTerm {
    pub pattern: String,
    pub negated: bool,
    compiled: Regex,
}

impl RegexTerm {
    #[must_use]
    pub fn matches(&self, text: &str) -> bool {
        self.compiled.is_match(text)
    }
}

#[derive(Clone, Debug, Default)]
pub struct Plan {
    pub filters: Vec<Filter>,
    pub terms: Vec<Term>,
    pub regexes: Vec<RegexTerm>,
}

impl Plan {
    #[must_use]
    pub fn requires_sidecar(&self) -> bool {
        !self.filters.is_empty()
            || !self.regexes.is_empty()
            || self.terms.iter().any(|term| term.phrase || term.negated)
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct QueryError {
    pub class: &'static str,
    pub message: String,
}

impl QueryError {
    fn new(class: &'static str, message: impl Into<String>) -> Self {
        Self {
            class,
            message: message.into(),
        }
    }
}

impl fmt::Display for QueryError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl Error for QueryError {}

/// Parses the Go-compatible query grammar.
///
/// # Errors
///
/// Returns a classified syntax/date/regex error without partially returning a plan.
pub fn parse(input: &str) -> Result<Plan, QueryError> {
    let bytes = input.as_bytes();
    let mut plan = Plan::default();
    let mut index = 0;
    loop {
        index = skip_space(bytes, index);
        if index >= bytes.len() {
            return Ok(plan);
        }
        let mut negated = false;
        if bytes[index] == b'-' {
            negated = true;
            index += 1;
            if index >= bytes.len() || is_go_space(bytes[index]) {
                return Err(QueryError::new(
                    "negation",
                    "negation must be followed by a term",
                ));
            }
        }

        match bytes[index] {
            b'"' => {
                let (value, next) = quoted(bytes, index)?;
                plan.terms.push(Term {
                    value,
                    phrase: true,
                    negated,
                });
                index = next;
            }
            b'/' => {
                let (pattern, next) = regex_literal(bytes, index)?;
                let rust_pattern = pattern.replace("\\/", "/");
                let compiled = Regex::new(&rust_pattern).map_err(|error| {
                    QueryError::new(
                        "invalid regular expression",
                        format!("invalid regular expression: {error}"),
                    )
                })?;
                plan.regexes.push(RegexTerm {
                    pattern,
                    negated,
                    compiled,
                });
                index = next;
            }
            _ => {
                let (value, next) = bare(bytes, index);
                if value.is_empty() {
                    return Err(QueryError::new(
                        "expected search term",
                        "expected search term",
                    ));
                }
                match parse_filter(&value)? {
                    Some((field, mut filter_value)) => {
                        let mut consumed_to = next;
                        if is_date_field(field) && filter_value.eq_ignore_ascii_case("last") {
                            let unit_start = skip_space(bytes, next);
                            let (unit, unit_end) = bare(bytes, unit_start);
                            if is_date_unit(&unit) {
                                filter_value.push(' ');
                                filter_value.push_str(&unit.to_lowercase());
                                consumed_to = unit_end;
                            }
                        }
                        if is_date_field(field) {
                            validate_date_value(&filter_value)?;
                        }
                        plan.filters.push(Filter {
                            field,
                            value: filter_value,
                            negated,
                        });
                        index = consumed_to;
                    }
                    None => {
                        plan.terms.push(Term {
                            value,
                            phrase: false,
                            negated,
                        });
                        index = next;
                    }
                }
            }
        }
        if index < bytes.len() && !is_go_space(bytes[index]) {
            return Err(QueryError::new(
                "unexpected character",
                format!(
                    "unexpected character {:?} after search term",
                    char::from(bytes[index])
                ),
            ));
        }
    }
}

fn quoted(input: &[u8], start: usize) -> Result<(String, usize), QueryError> {
    let mut index = start + 1;
    let mut value = Vec::new();
    let mut escaped = false;
    while index < input.len() {
        let byte = input[index];
        index += 1;
        if escaped {
            value.push(byte);
            escaped = false;
        } else if byte == b'\\' {
            escaped = true;
        } else if byte == b'"' {
            if value.is_empty() {
                return Err(QueryError::new(
                    "empty quoted phrase",
                    format!("empty quoted phrase at offset {start}"),
                ));
            }
            return Ok((String::from_utf8_lossy(&value).into_owned(), index));
        } else {
            value.push(byte);
        }
    }
    Err(QueryError::new(
        "unterminated quoted phrase",
        format!("unterminated quoted phrase at offset {start}"),
    ))
}

fn regex_literal(input: &[u8], start: usize) -> Result<(String, usize), QueryError> {
    let mut index = start + 1;
    let mut value = Vec::new();
    let mut escaped = false;
    while index < input.len() {
        let byte = input[index];
        index += 1;
        if escaped {
            value.push(b'\\');
            value.push(byte);
            escaped = false;
        } else if byte == b'\\' {
            escaped = true;
        } else if byte == b'/' {
            if value.is_empty() {
                return Err(QueryError::new(
                    "empty regular expression",
                    format!("empty regular expression at offset {start}"),
                ));
            }
            return Ok((String::from_utf8_lossy(&value).into_owned(), index));
        } else {
            value.push(byte);
        }
    }
    Err(QueryError::new(
        "unterminated regular expression",
        format!("unterminated regular expression at offset {start}"),
    ))
}

fn parse_filter(token: &str) -> Result<Option<(Field, String)>, QueryError> {
    let Some((field_name, value)) = token.split_once(':') else {
        return Ok(None);
    };
    if value.is_empty() {
        return Err(QueryError::new(
            "requires a value",
            format!("operator {:?} requires a value", format!("{field_name}:")),
        ));
    }
    let field = match field_name.to_ascii_lowercase().as_str() {
        "path" => Field::Path,
        "tag" => Field::Tag,
        "type" => Field::Type,
        "status" => Field::Status,
        "filename" => Field::Filename,
        "filetype" => Field::FileType,
        "created" => Field::Created,
        "modified" => Field::Modified,
        "index" | "index_status" => Field::Index,
        _ if operator_name(field_name) => {
            return Err(QueryError::new(
                "unknown search operator",
                format!("unknown search operator {:?}", format!("{field_name}:")),
            ));
        }
        _ => return Ok(None),
    };
    Ok(Some((field, value.to_owned())))
}

fn operator_name(value: &str) -> bool {
    let mut bytes = value.bytes();
    bytes.next().is_some_and(|byte| byte.is_ascii_alphabetic())
        && bytes.all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
}

fn is_date_field(field: Field) -> bool {
    matches!(field, Field::Created | Field::Modified)
}

fn is_date_unit(value: &str) -> bool {
    matches!(
        value.to_ascii_lowercase().as_str(),
        "day" | "week" | "month" | "year"
    )
}

fn skip_space(input: &[u8], mut index: usize) -> usize {
    while index < input.len() && is_go_space(input[index]) {
        index += 1;
    }
    index
}

fn bare(input: &[u8], start: usize) -> (String, usize) {
    let mut index = start;
    while index < input.len() && !is_go_space(input[index]) {
        index += 1;
    }
    (
        String::from_utf8_lossy(&input[start..index]).into_owned(),
        index,
    )
}

fn is_go_space(byte: u8) -> bool {
    char::from(byte).is_whitespace()
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct DateRange {
    pub from: OffsetDateTime,
    pub to: OffsetDateTime,
}

/// Resolves one inclusive date expression against a fixed reference instant.
///
/// # Errors
///
/// Returns the Go-compatible validation error for invalid expressions.
pub fn parse_date_value(value: &str, reference: OffsetDateTime) -> Result<DateRange, QueryError> {
    let value = value.trim();
    if value.is_empty() {
        return Err(QueryError::new(
            "date filter requires a value",
            "date filter requires a value",
        ));
    }
    let reference = reference.to_offset(time::UtcOffset::UTC);
    let lower = value.to_ascii_lowercase();
    if lower.starts_with("last ") {
        let duration = match lower.as_str() {
            "last day" => Duration::days(1),
            "last week" => Duration::days(7),
            "last month" => Duration::days(30),
            "last year" => Duration::days(365),
            _ => {
                return Err(QueryError::new(
                    "invalid relative date",
                    format!("invalid relative date {value:?}"),
                ));
            }
        };
        return Ok(DateRange {
            from: reference - duration,
            to: reference,
        });
    }

    let parts: Vec<_> = value.split("..").collect();
    if parts.len() > 2
        || (parts.len() == 2 && (parts[0].trim().is_empty() || parts[1].trim().is_empty()))
    {
        return Err(QueryError::new(
            "invalid date range",
            format!("invalid date range {value:?}"),
        ));
    }
    let (mut from, from_date) = parse_date_endpoint(parts[0].trim())?;
    let (mut to, to_date) = if parts.len() == 2 {
        parse_date_endpoint(parts[1].trim())?
    } else {
        (from, from_date)
    };
    if from_date {
        from = from.date().with_time(Time::MIDNIGHT).assume_utc();
    }
    if to_date {
        to = to.date().with_time(Time::MIDNIGHT).assume_utc() + Duration::days(1)
            - Duration::nanoseconds(1);
    }
    if from > to {
        return Err(QueryError::new(
            "date range starts after it ends",
            format!("date range starts after it ends: {value:?}"),
        ));
    }
    Ok(DateRange { from, to })
}

/// Validates a comma-separated date value against the Unix epoch.
///
/// # Errors
///
/// Returns the first invalid-expression error.
pub fn validate_date_value(value: &str) -> Result<(), QueryError> {
    for expression in value.split(',') {
        if expression.trim().is_empty() {
            return Err(QueryError::new(
                "invalid date",
                format!("invalid date {value:?}"),
            ));
        }
        parse_date_value(expression, OffsetDateTime::UNIX_EPOCH)?;
    }
    Ok(())
}

fn parse_date_endpoint(value: &str) -> Result<(OffsetDateTime, bool), QueryError> {
    if let Ok(date) = Date::parse(value, DATE_FORMAT) {
        return Ok((date.with_time(Time::MIDNIGHT).assume_utc(), true));
    }
    if let Ok(timestamp) = OffsetDateTime::parse(value, &Rfc3339) {
        return Ok((timestamp.to_offset(time::UtcOffset::UTC), false));
    }
    Err(QueryError::new(
        "invalid date",
        format!("invalid date {value:?} (use YYYY-MM-DD or YYYY-MM-DD..YYYY-MM-DD)"),
    ))
}
