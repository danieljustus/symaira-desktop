//! Dataset CSV parser and sidecar projection helper parity.
//!
//! Go sources: `internal/dataset/dataset.go` (`ParseCSV`, `CanonicalRowHash`,
//! `inferType`, `convertValue`, `parseDate`) and `internal/service/dataset.go`
//! (`replaceDatasetRows`, which deduplicates by row key, orders by that key and
//! serialises each value map with `encoding/json`).
//!
//! The service orchestration lives in `symdesk-index`, which already depends on
//! this crate and owns the real SQLite sidecar. This module also renders the
//! authoritative Markdown handle and exposes the row projection used by that
//! service.
//!
//! The CSV reader mirrors Go's default `encoding/csv.Reader`: quoted fields and
//! doubled quotes, CRLF-to-LF normalization inside quoted fields, blank-line
//! skipping, UTF-8 preservation, physical-line parse diagnostics and first-row
//! field-count enforcement.

use std::collections::{BTreeMap, HashMap};

use time::{
    Date, PrimitiveDateTime, format_description::well_known::Iso8601, macros::format_description,
};

use crate::go_string::{lowercase as go_lowercase, quote as go_quote, rejects_case_edge};

const DATE_TIME_SECONDS: &[time::format_description::FormatItem<'static>] =
    format_description!("[year]-[month]-[day] [hour]:[minute]:[second]");
const DATE_TIME_MINUTES: &[time::format_description::FormatItem<'static>] =
    format_description!("[year]-[month]-[day] [hour]:[minute]");

/// Errors mirror Go's wrapped messages so a failing case reports the same text.
#[derive(Debug, PartialEq, Eq)]
pub enum DatasetError {
    ReaderRequired,
    ReadCsv(String),
    EmptyCsv,
    EmptyColumnName(usize),
    DuplicateColumn(String),
    MissingIdentity(String),
    /// Go's `encoding/csv` reports this from `ReadAll` before `ParseCSV`
    /// gets to check the widths itself, so the message is the record line.
    WrongFieldCount {
        record: usize,
    },
    /// Go wraps `convertValue` failures with the row and column that failed.
    CellValue {
        row: usize,
        column: String,
        detail: String,
    },
    InvalidNumber(String),
    InvalidBoolean(String),
    InvalidDate(String),
    UnsupportedJsonValue(String),
    HandleRender(String),
}

impl std::error::Error for DatasetError {}

impl std::fmt::Display for DatasetError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::ReaderRequired => write!(f, "csv reader is required"),
            Self::ReadCsv(detail) => write!(f, "read csv: {detail}"),
            Self::EmptyCsv => write!(f, "csv is empty"),
            Self::EmptyColumnName(index) => write!(f, "csv column {index} has an empty name"),
            Self::DuplicateColumn(name) => {
                write!(f, "csv has duplicate column {}", go_quote(name))
            }
            Self::MissingIdentity(name) => {
                write!(f, "identity field {} is not a CSV column", go_quote(name))
            }
            Self::WrongFieldCount { record } => {
                write!(
                    f,
                    "read csv: record on line {record}: wrong number of fields"
                )
            }
            Self::CellValue {
                row,
                column,
                detail,
            } => {
                write!(f, "row {row} column {}: {detail}", go_quote(column))
            }
            Self::InvalidNumber(value) => write!(f, "invalid number {}", go_quote(value)),
            Self::InvalidBoolean(value) => write!(f, "invalid boolean {}", go_quote(value)),
            Self::InvalidDate(value) => write!(f, "invalid date {}", go_quote(value)),
            Self::UnsupportedJsonValue(value) => {
                write!(f, "json: unsupported value: {value}")
            }
            Self::HandleRender(detail) => write!(f, "{detail}"),
        }
    }
}

/// One schema entry, matching `dbviews.PropertyConfig` as the oracle records it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PropertyConfig {
    pub label: String,
    pub kind: String,
}

/// A parsed CSV row before it is projected into the sidecar.
#[derive(Debug, Clone, PartialEq)]
pub struct Row {
    pub key: String,
    pub identity: String,
    pub values: BTreeMap<String, GoValue>,
    pub source_path: String,
    pub row_number: usize,
}

/// The projected row, matching `sidecar.DatasetRow` as Go reads it back.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SidecarRow {
    pub dataset_slug: String,
    pub row_key: String,
    pub identity: String,
    pub values_json: String,
    pub source_path: String,
    pub row_number: usize,
}

/// A converted cell value. Floats stay `f64` because Go's `convertValue`
/// returns `float64` for inferred `number` columns.
#[derive(Debug, Clone, PartialEq)]
pub enum GoValue {
    Text(String),
    Number(f64),
    Boolean(bool),
}

/// Reads CSV with Go's default `encoding/csv.Reader` behavior.
pub fn read_all_csv(input: &str) -> Result<Vec<Vec<String>>, DatasetError> {
    let mut reader = CsvLineReader::new(input.as_bytes());
    let mut records = Vec::new();
    let mut fields_per_record = None;
    while let Some((record_line, record)) = read_csv_record(&mut reader)? {
        if let Some(want) = fields_per_record {
            if record.len() != want {
                return Err(DatasetError::WrongFieldCount {
                    record: record_line,
                });
            }
        } else {
            fields_per_record = Some(record.len());
        }
        records.push(record);
    }
    Ok(records)
}

struct CsvLineReader<'a> {
    input: &'a [u8],
    offset: usize,
    line: usize,
}

impl<'a> CsvLineReader<'a> {
    fn new(input: &'a [u8]) -> Self {
        Self {
            input,
            offset: 0,
            line: 0,
        }
    }

    fn read_line(&mut self) -> Option<Vec<u8>> {
        if self.offset >= self.input.len() {
            return None;
        }
        let remaining = &self.input[self.offset..];
        let consumed = remaining
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or(remaining.len(), |index| index + 1);
        let has_newline = remaining.get(consumed.wrapping_sub(1)) == Some(&b'\n');
        let mut line = remaining[..consumed].to_vec();
        self.offset += consumed;
        self.line += 1;

        if !has_newline && line.last() == Some(&b'\r') {
            line.pop();
        } else if line.ends_with(b"\r\n") {
            let newline = line.len() - 2;
            line[newline] = b'\n';
            line.pop();
        }
        Some(line)
    }
}

fn read_csv_record(
    reader: &mut CsvLineReader<'_>,
) -> Result<Option<(usize, Vec<String>)>, DatasetError> {
    let mut line = loop {
        let Some(line) = reader.read_line() else {
            return Ok(None);
        };
        if line.len() == csv_newline_len(&line) {
            continue;
        }
        break line;
    };

    let record_line = reader.line;
    let mut fields = Vec::new();
    let mut offset = 0usize;
    let mut position_line = reader.line;
    let mut position_column = 1usize;

    loop {
        let remaining = &line[offset..];
        if remaining.first() != Some(&b'"') {
            let comma = remaining.iter().position(|byte| *byte == b',');
            let field_end = comma.unwrap_or_else(|| remaining.len() - csv_newline_len(remaining));
            let field = &remaining[..field_end];
            if let Some(quote) = field.iter().position(|byte| *byte == b'"') {
                return Err(csv_parse_error(
                    record_line,
                    reader.line,
                    position_column + quote,
                    "bare \" in non-quoted-field",
                ));
            }
            fields.push(
                std::str::from_utf8(field)
                    .expect("CSV input is valid UTF-8")
                    .to_owned(),
            );
            if let Some(comma) = comma {
                offset += comma + 1;
                position_column += comma + 1;
                continue;
            }
            break;
        }

        let mut field = Vec::new();
        offset += 1;
        position_column += 1;
        loop {
            let remaining = &line[offset..];
            if let Some(quote) = remaining.iter().position(|byte| *byte == b'"') {
                field.extend_from_slice(&remaining[..quote]);
                offset += quote + 1;
                position_column += quote + 1;
                let after_quote = &line[offset..];
                match after_quote.first() {
                    Some(b'"') => {
                        field.push(b'"');
                        offset += 1;
                        position_column += 1;
                    }
                    Some(b',') => {
                        offset += 1;
                        position_column += 1;
                        fields.push(String::from_utf8(field).expect("CSV input is valid UTF-8"));
                        break;
                    }
                    _ if after_quote.len() == csv_newline_len(after_quote) => {
                        fields.push(String::from_utf8(field).expect("CSV input is valid UTF-8"));
                        return Ok(Some((record_line, fields)));
                    }
                    _ => {
                        return Err(csv_parse_error(
                            record_line,
                            reader.line,
                            position_column - 1,
                            "extraneous or missing \" in quoted-field",
                        ));
                    }
                }
                continue;
            }

            if !remaining.is_empty() {
                field.extend_from_slice(remaining);
                position_column += remaining.len();
                let Some(next_line) = reader.read_line() else {
                    return Err(csv_parse_error(
                        record_line,
                        position_line,
                        position_column,
                        "extraneous or missing \" in quoted-field",
                    ));
                };
                line = next_line;
                offset = 0;
                if !line.is_empty() {
                    position_line += 1;
                    position_column = 1;
                }
                continue;
            }

            return Err(csv_parse_error(
                record_line,
                position_line,
                position_column,
                "extraneous or missing \" in quoted-field",
            ));
        }
    }

    Ok(Some((record_line, fields)))
}

fn csv_newline_len(input: &[u8]) -> usize {
    usize::from(input.last() == Some(&b'\n'))
}

fn csv_parse_error(
    record_line: usize,
    error_line: usize,
    column: usize,
    detail: &str,
) -> DatasetError {
    if record_line == error_line {
        DatasetError::ReadCsv(format!(
            "parse error on line {error_line}, column {column}: {detail}"
        ))
    } else {
        DatasetError::ReadCsv(format!(
            "record on line {record_line}; parse error on line {error_line}, column {column}: {detail}"
        ))
    }
}

/// Port of `dataset.ParseCSV`.
pub fn parse_csv(
    input: &str,
    declared: &BTreeMap<String, PropertyConfig>,
    identity_field: &str,
) -> Result<(Vec<Row>, BTreeMap<String, PropertyConfig>), DatasetError> {
    let records = read_all_csv(input)?;
    if records.is_empty() {
        return Err(DatasetError::EmptyCsv);
    }

    let mut headers: Vec<String> = Vec::with_capacity(records[0].len());
    let mut seen: HashMap<String, bool> = HashMap::new();
    for (index, raw) in records[0].iter().enumerate() {
        let header = raw.trim().to_string();
        if header.is_empty() {
            return Err(DatasetError::EmptyColumnName(index + 1));
        }
        let folded = go_lowercase(&header);
        if seen.contains_key(&folded) {
            return Err(DatasetError::DuplicateColumn(header.clone()));
        }
        seen.insert(folded, true);
        headers.push(header);
    }
    if !identity_field.is_empty() && !has_header(&headers, identity_field) {
        return Err(DatasetError::MissingIdentity(identity_field.to_string()));
    }

    let mut values_by_column: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for header in &headers {
        values_by_column.insert(header.clone(), Vec::new());
    }
    for record in records.iter().skip(1) {
        for (index, value) in record.iter().enumerate() {
            let header = &headers[index];
            if let Some(slot) = values_by_column.get_mut(header) {
                slot.push(value.trim().to_string());
            }
        }
    }

    let mut schema: BTreeMap<String, PropertyConfig> = BTreeMap::new();
    for header in &headers {
        let mut property = declared.get(header).cloned().unwrap_or_default();
        if property.kind.is_empty() {
            property.kind = values_by_column
                .get(header)
                .map(|values| infer_type(values))
                .unwrap_or_else(|| "text".to_string());
        }
        if property.label.is_empty() {
            property.label = header.clone();
        }
        schema.insert(header.clone(), property);
    }

    let mut rows: Vec<Row> = Vec::new();
    for (offset, record) in records.iter().enumerate().skip(1) {
        let row_number = offset + 1;
        let mut values: BTreeMap<String, GoValue> = BTreeMap::new();
        let mut raw_values: BTreeMap<String, String> = BTreeMap::new();
        for (index, header) in headers.iter().enumerate() {
            let raw = record[index].trim().to_string();
            let kind = schema.get(header).map(|p| p.kind.as_str()).unwrap_or("");
            let value = convert_value(&raw, kind).map_err(|detail| DatasetError::CellValue {
                row: row_number,
                column: header.clone(),
                detail: detail.to_string(),
            })?;
            raw_values.insert(header.clone(), raw);
            values.insert(header.clone(), value);
        }
        let mut identity = String::new();
        if !identity_field.is_empty() {
            for header in &headers {
                if go_equal_fold(header, identity_field) {
                    identity = raw_values.get(header).cloned().unwrap_or_default();
                    break;
                }
            }
        }
        let mut key = format!("hash:{}", canonical_row_hash(&headers, &raw_values));
        if !identity.is_empty() {
            key = format!("identity:{identity}");
        }
        rows.push(Row {
            key,
            identity,
            values,
            source_path: String::new(),
            row_number,
        });
    }
    Ok((rows, schema))
}

fn has_header(headers: &[String], wanted: &str) -> bool {
    headers.iter().any(|header| go_equal_fold(header, wanted))
}

fn go_equal_fold(left: &str, right: &str) -> bool {
    let mut left = left.chars();
    let mut right = right.chars();
    loop {
        match (left.next(), right.next()) {
            (None, None) => return true,
            (Some(left), Some(right)) if left == right => {}
            (Some(left), Some(right)) if simple_fold_key(left) == simple_fold_key(right) => {}
            _ => return false,
        }
    }
}

fn simple_fold_key(value: char) -> char {
    if let Some(canonical) = simple_fold_exception(value) {
        return canonical;
    }
    let mut pending = vec![value];
    let mut seen = Vec::new();
    while let Some(candidate) = pending.pop() {
        if seen.contains(&candidate) {
            continue;
        }
        seen.push(candidate);
        for mapped in [
            single_case_mapping(candidate.to_lowercase()),
            single_case_mapping(candidate.to_uppercase()),
        ]
        .into_iter()
        .flatten()
        .filter(|mapped| !rejects_case_edge(candidate, *mapped))
        {
            if !seen.contains(&mapped) {
                pending.push(mapped);
            }
        }
    }
    seen.into_iter().min().unwrap_or(value)
}

fn simple_fold_exception(value: char) -> Option<char> {
    // Rust's one-way Unicode case mappings omit the reverse edge for three
    // Go SimpleFold cycles. These are the only exceptions found by exhaustively
    // checking every Go 1.26.6 SimpleFold edge against Rust 1.98.0.
    match value {
        '\u{00b5}' | '\u{039c}' | '\u{03bc}' => Some('\u{00b5}'),
        '\u{0345}' | '\u{0399}' | '\u{03b9}' | '\u{1fbe}' => Some('\u{0345}'),
        '\u{1c88}' | '\u{a64a}' | '\u{a64b}' => Some('\u{1c88}'),
        _ => None,
    }
}

fn single_case_mapping(mut mapping: impl Iterator<Item = char>) -> Option<char> {
    let first = mapping.next()?;
    mapping.next().is_none().then_some(first)
}

/// Port of `dataset.CanonicalRowHash`: sorted columns, `len:name=len:value`
/// records separated by newlines, values trimmed by the caller.
pub fn canonical_row_hash(columns: &[String], values: &BTreeMap<String, String>) -> String {
    let mut sorted: Vec<&String> = columns.iter().collect();
    sorted.sort();
    let mut builder = String::new();
    for column in sorted {
        let value = values.get(column).map(String::as_str).unwrap_or("").trim();
        builder.push_str(&column.len().to_string());
        builder.push(':');
        builder.push_str(column);
        builder.push('=');
        builder.push_str(&value.len().to_string());
        builder.push(':');
        builder.push_str(value);
        builder.push('\n');
    }
    let digest = crate::sha256::digest(builder.as_bytes());
    let mut out = String::with_capacity(digest.len() * 2);
    for byte in digest {
        out.push_str(&format!("{byte:02x}"));
    }
    out
}

/// Port of `dataset.inferType`. Note Go tests booleans first, so an all-`1`/`0`
/// column becomes `checkbox` rather than `number`.
fn infer_type(values: &[String]) -> String {
    let mut has_value = false;
    let mut all_number = true;
    let mut all_date = true;
    let mut all_bool = true;
    for value in values {
        if value.is_empty() {
            continue;
        }
        has_value = true;
        if parse_go_float(value).is_none() {
            all_number = false;
        }
        if !parse_date(value) {
            all_date = false;
        }
        if parse_go_bool(value).is_none() {
            all_bool = false;
        }
    }
    if !has_value {
        return "text".to_string();
    }
    if all_bool {
        return "checkbox".to_string();
    }
    if all_number {
        return "number".to_string();
    }
    if all_date {
        return "date".to_string();
    }
    "text".to_string()
}

/// Port of `dataset.convertValue`.
fn convert_value(value: &str, kind: &str) -> Result<GoValue, DatasetError> {
    if value.is_empty() {
        return Ok(GoValue::Text(String::new()));
    }
    match kind.trim().to_lowercase().as_str() {
        "number" => parse_go_float(value)
            .map(GoValue::Number)
            .ok_or_else(|| DatasetError::InvalidNumber(value.to_string())),
        "checkbox" | "boolean" | "bool" => parse_go_bool(value)
            .map(GoValue::Boolean)
            .ok_or_else(|| DatasetError::InvalidBoolean(value.to_string())),
        "date" => {
            if parse_date(value) {
                Ok(GoValue::Text(value.to_string()))
            } else {
                Err(DatasetError::InvalidDate(value.to_string()))
            }
        }
        _ => Ok(GoValue::Text(value.to_string())),
    }
}

/// `strconv.ParseFloat` syntax: decimal and hexadecimal floating literals,
/// valid digit separators, and Go's exact `Inf`/`NaN` spellings.
fn parse_go_float(value: &str) -> Option<f64> {
    if value.eq_ignore_ascii_case("nan") {
        return Some(f64::NAN);
    }
    let (negative, unsigned) = if let Some(rest) = value.strip_prefix('-') {
        (true, rest)
    } else {
        (false, value.strip_prefix('+').unwrap_or(value))
    };
    if unsigned.eq_ignore_ascii_case("inf") || unsigned.eq_ignore_ascii_case("infinity") {
        return Some(if negative {
            f64::NEG_INFINITY
        } else {
            f64::INFINITY
        });
    }

    let normalized = remove_float_underscores(value)?;
    let unsigned = normalized
        .strip_prefix(['+', '-'])
        .unwrap_or(normalized.as_str());
    let parsed = if unsigned.starts_with("0x") || unsigned.starts_with("0X") {
        parse_go_hex_float(&normalized)?
    } else {
        if !is_go_decimal_float(&normalized) {
            return None;
        }
        normalized.parse::<f64>().ok()?
    };
    parsed.is_finite().then_some(parsed)
}

fn remove_float_underscores(value: &str) -> Option<String> {
    let chars: Vec<char> = value.chars().collect();
    let sign_offset = usize::from(matches!(chars.first(), Some('+' | '-')));
    let is_hex = chars.get(sign_offset) == Some(&'0')
        && matches!(chars.get(sign_offset + 1), Some('x' | 'X'));
    let exponent = chars.iter().position(|ch| {
        if is_hex {
            matches!(ch, 'p' | 'P')
        } else {
            matches!(ch, 'e' | 'E')
        }
    });

    let mut normalized = String::with_capacity(value.len());
    for (index, ch) in chars.iter().copied().enumerate() {
        if ch != '_' {
            normalized.push(ch);
            continue;
        }
        let next = chars.get(index + 1).copied()?;
        if is_hex && index == sign_offset + 2 {
            if !next.is_ascii_hexdigit() {
                return None;
            }
            continue;
        }
        let previous = index.checked_sub(1).and_then(|i| chars.get(i)).copied()?;
        let in_exponent = exponent.is_some_and(|position| index > position);
        let valid = if in_exponent || !is_hex {
            previous.is_ascii_digit() && next.is_ascii_digit()
        } else {
            previous.is_ascii_hexdigit() && next.is_ascii_hexdigit()
        };
        if !valid {
            return None;
        }
    }
    Some(normalized)
}

fn is_go_decimal_float(value: &str) -> bool {
    let bytes = value.as_bytes();
    let mut index = usize::from(matches!(bytes.first(), Some(b'+' | b'-')));
    let mut digits = 0usize;
    while bytes.get(index).is_some_and(u8::is_ascii_digit) {
        digits += 1;
        index += 1;
    }
    if bytes.get(index) == Some(&b'.') {
        index += 1;
        while bytes.get(index).is_some_and(u8::is_ascii_digit) {
            digits += 1;
            index += 1;
        }
    }
    if digits == 0 {
        return false;
    }
    if matches!(bytes.get(index), Some(b'e' | b'E')) {
        index += 1;
        if matches!(bytes.get(index), Some(b'+' | b'-')) {
            index += 1;
        }
        let exponent_start = index;
        while bytes.get(index).is_some_and(u8::is_ascii_digit) {
            index += 1;
        }
        if exponent_start == index {
            return false;
        }
    }
    index == bytes.len()
}

fn parse_go_hex_float(value: &str) -> Option<f64> {
    let (negative, unsigned) = if let Some(rest) = value.strip_prefix('-') {
        (true, rest)
    } else {
        (false, value.strip_prefix('+').unwrap_or(value))
    };
    let literal = unsigned
        .strip_prefix("0x")
        .or_else(|| unsigned.strip_prefix("0X"))?;
    let (mantissa, exponent) = literal
        .split_once(['p', 'P'])
        .filter(|(_, exponent)| !exponent.contains(['p', 'P']))?;
    let exponent = parse_bounded_decimal_exponent(exponent)?;

    let mut digits = Vec::with_capacity(mantissa.len());
    let mut fractional_digits = 0i64;
    let mut saw_dot = false;
    for byte in mantissa.bytes() {
        if byte == b'.' {
            if saw_dot {
                return None;
            }
            saw_dot = true;
            continue;
        }
        let digit = match byte {
            b'0'..=b'9' => byte - b'0',
            b'a'..=b'f' => byte - b'a' + 10,
            b'A'..=b'F' => byte - b'A' + 10,
            _ => return None,
        };
        digits.push(digit);
        if saw_dot {
            fractional_digits = fractional_digits.checked_add(1)?;
        }
    }
    if digits.is_empty() {
        return None;
    }

    let Some(first_nonzero) = digits.iter().position(|digit| *digit != 0) else {
        return Some(f64::from_bits(u64::from(negative) << 63));
    };
    let digits = &digits[first_nonzero..];
    let first_bits = 8 - digits[0].leading_zeros() as i64;
    let trailing_nibbles = i64::try_from(digits.len().checked_sub(1)?).ok()?;
    let bit_len = trailing_nibbles.checked_mul(4)?.checked_add(first_bits)?;
    let base_exponent = exponent.checked_sub(fractional_digits.checked_mul(4)?)?;
    let mut top_exponent = bit_len.checked_sub(1)?.checked_add(base_exponent)?;

    let sign = u64::from(negative) << 63;
    let magnitude = if top_exponent >= -1022 {
        let shift = bit_len.checked_sub(53)?;
        let mut significand = round_hex_integer(digits, bit_len, shift)?;
        if significand == 1u64 << 53 {
            significand >>= 1;
            top_exponent = top_exponent.checked_add(1)?;
        }
        if top_exponent > 1023 {
            return None;
        }
        let biased = u64::try_from(top_exponent + 1023).ok()?;
        (biased << 52) | (significand & ((1u64 << 52) - 1))
    } else {
        let shift = base_exponent.checked_add(1074)?.checked_neg()?;
        let significand = round_hex_integer(digits, bit_len, shift)?;
        if significand >= 1u64 << 52 {
            1u64 << 52
        } else {
            significand
        }
    };
    Some(f64::from_bits(sign | magnitude))
}

fn parse_bounded_decimal_exponent(value: &str) -> Option<i64> {
    let (negative, digits) = if let Some(rest) = value.strip_prefix('-') {
        (true, rest)
    } else {
        (false, value.strip_prefix('+').unwrap_or(value))
    };
    if digits.is_empty() || !digits.bytes().all(|byte| byte.is_ascii_digit()) {
        return None;
    }
    let mut exponent = 0i64;
    for byte in digits.bytes() {
        if exponent < 100_000 {
            exponent = (exponent * 10 + i64::from(byte - b'0')).min(100_000);
        }
    }
    Some(if negative { -exponent } else { exponent })
}

fn round_hex_integer(digits: &[u8], bit_len: i64, right_shift: i64) -> Option<u64> {
    if right_shift <= 0 {
        let left_shift = u32::try_from(right_shift.checked_neg()?).ok()?;
        let mut value = 0u64;
        for position in (0..bit_len).rev() {
            value = value.checked_mul(2)?;
            if hex_bit(digits, position) {
                value = value.checked_add(1)?;
            }
        }
        return value.checked_shl(left_shift);
    }

    let kept_bits = bit_len.saturating_sub(right_shift).max(0);
    if kept_bits > 64 {
        return None;
    }
    let mut value = 0u64;
    for position in (right_shift..bit_len).rev() {
        value <<= 1;
        if hex_bit(digits, position) {
            value |= 1;
        }
    }

    let round_position = right_shift - 1;
    let round_bit = round_position < bit_len && hex_bit(digits, round_position);
    let sticky = (0..round_position.min(bit_len)).any(|position| hex_bit(digits, position));
    if round_bit && (sticky || value & 1 == 1) {
        value = value.checked_add(1)?;
    }
    Some(value)
}

fn hex_bit(digits: &[u8], position: i64) -> bool {
    let Ok(position) = usize::try_from(position) else {
        return false;
    };
    let nibble_from_end = position / 4;
    let Some(index) = digits.len().checked_sub(nibble_from_end + 1) else {
        return false;
    };
    digits[index] & (1 << (position % 4)) != 0
}

/// `strconv.ParseBool` accepts these exact spellings case-insensitively after
/// Go lowercases the value for the `checkbox` path.
fn parse_go_bool(value: &str) -> Option<bool> {
    match value.to_lowercase().as_str() {
        "1" | "t" | "true" | "TRUE" | "True" => Some(true),
        "0" | "f" | "false" | "FALSE" | "False" => Some(false),
        _ => None,
    }
}

/// Port of `dataset.parseDate`: the four exact layouts Go tries, in order.
fn parse_date(value: &str) -> bool {
    (value.len() == 10
        && value.as_bytes().get(4) == Some(&b'-')
        && value.as_bytes().get(7) == Some(&b'-')
        && Date::parse(value, &Iso8601::DATE).is_ok())
        || parse_go_rfc3339(value)
        || parse_go_custom_seconds(value)
        || (value.len() == 16 && PrimitiveDateTime::parse(value, DATE_TIME_MINUTES).is_ok())
}

fn parse_go_custom_seconds(value: &str) -> bool {
    if !value.is_ascii()
        || value.len() < 19
        || PrimitiveDateTime::parse(&value[..19], DATE_TIME_SECONDS).is_err()
    {
        return false;
    }
    let remainder = &value.as_bytes()[19..];
    remainder.is_empty()
        || (matches!(remainder.first(), Some(b'.' | b','))
            && remainder.len() > 1
            && remainder[1..].iter().all(u8::is_ascii_digit))
}

fn parse_go_rfc3339(value: &str) -> bool {
    if !value.is_ascii() {
        return false;
    }
    let bytes = value.as_bytes();
    if bytes.len() < 20
        || bytes.get(4) != Some(&b'-')
        || bytes.get(7) != Some(&b'-')
        || bytes.get(10) != Some(&b'T')
        || bytes.get(13) != Some(&b':')
        || bytes.get(16) != Some(&b':')
        || Date::parse(&value[..10], &Iso8601::DATE).is_err()
        || parse_two_digits(bytes, 11).is_none_or(|hour| hour > 23)
        || parse_two_digits(bytes, 14).is_none_or(|minute| minute > 59)
        || parse_two_digits(bytes, 17).is_none_or(|second| second > 59)
    {
        return false;
    }

    let mut index = 19usize;
    if matches!(bytes.get(index), Some(b'.' | b',')) {
        index += 1;
        let fraction_start = index;
        while bytes.get(index).is_some_and(u8::is_ascii_digit) {
            index += 1;
        }
        if index == fraction_start {
            return false;
        }
    }
    if bytes.get(index) == Some(&b'Z') {
        return index + 1 == bytes.len();
    }
    bytes.get(index).is_some_and(|sign| matches!(sign, b'+' | b'-'))
        && bytes.get(index + 3) == Some(&b':')
        && index + 6 == bytes.len()
        // Go's time.Parse currently accepts +24:00 and +00:60.
        && parse_two_digits(bytes, index + 1).is_some_and(|hour| hour <= 24)
        && parse_two_digits(bytes, index + 4).is_some_and(|minute| minute <= 60)
}

fn parse_two_digits(bytes: &[u8], index: usize) -> Option<u8> {
    let tens = bytes.get(index)?.checked_sub(b'0')?;
    let ones = bytes.get(index + 1)?.checked_sub(b'0')?;
    (tens <= 9 && ones <= 9).then_some(tens * 10 + ones)
}

pub fn normalize_policy(
    sensitivity: &str,
    retention_rule: &str,
) -> Result<(String, String), DatasetError> {
    let sensitivity = sensitivity.trim().to_lowercase();
    let sensitivity = if sensitivity.is_empty() {
        "restricted".to_owned()
    } else if matches!(
        sensitivity.as_str(),
        "public" | "internal" | "confidential" | "restricted"
    ) {
        sensitivity
    } else {
        return Err(DatasetError::HandleRender(format!(
            "invalid dataset sensitivity {} (valid: public, internal, confidential, restricted)",
            go_quote(&sensitivity)
        )));
    };
    let retention_rule = if retention_rule.trim().is_empty() {
        "default".to_owned()
    } else {
        retention_rule.trim().to_owned()
    };
    if retention_rule.chars().any(|character| {
        character.is_control()
            || unicode_general_category::get_general_category(character)
                == unicode_general_category::GeneralCategory::Format
            || matches!(character, '/' | '\\')
    }) {
        return Err(DatasetError::HandleRender(format!(
            "invalid dataset retention_rule {}: control characters and path separators are not allowed",
            go_quote(&retention_rule)
        )));
    }
    Ok((sensitivity, retention_rule))
}

/// Renders the authoritative dataset Markdown handle with the same key ordering,
/// struct-field ordering and quoting used by Go's `yaml.v3` encoder.
///
/// # Errors
/// Returns a validation or YAML-scalar rendering error.
pub fn render_handle(handle: &crate::DatasetHandle) -> Result<Vec<u8>, DatasetError> {
    if handle.title.is_empty() || handle.slug.is_empty() || handle.source.is_empty() {
        return Err(DatasetError::HandleRender(
            "dataset handle requires title, slug, and source".to_owned(),
        ));
    }
    if !matches!(
        handle.sensitivity.as_str(),
        "public" | "internal" | "confidential" | "restricted"
    ) {
        return Err(DatasetError::HandleRender(format!(
            "invalid dataset sensitivity {} (valid: public, internal, confidential, restricted)",
            go_quote(&handle.sensitivity)
        )));
    }
    if handle.retention_rule.trim().is_empty() {
        return Err(DatasetError::HandleRender(
            "dataset handle requires retention_rule".to_owned(),
        ));
    }
    if handle.retention_rule.chars().any(|character| {
        character.is_control()
            || unicode_general_category::get_general_category(character)
                == unicode_general_category::GeneralCategory::Format
            || matches!(character, '/' | '\\')
    }) {
        return Err(DatasetError::HandleRender(format!(
            "invalid dataset retention_rule {}: control characters and path separators are not allowed",
            go_quote(&handle.retention_rule)
        )));
    }

    fn scalar(value: &str, indent: usize) -> Result<String, DatasetError> {
        crate::mutations::render_go_yaml_string(value, indent, false)
            .map_err(|error| DatasetError::HandleRender(error.to_string()))
    }
    fn key(value: &str, indent: usize) -> Result<String, DatasetError> {
        crate::mutations::render_go_yaml_string(value, indent, true)
            .map_err(|error| DatasetError::HandleRender(error.to_string()))
    }
    fn push_scalar(
        lines: &mut Vec<String>,
        indent: usize,
        name: &str,
        value: &str,
    ) -> Result<(), DatasetError> {
        lines.push(format!(
            "{}{}: {}",
            " ".repeat(indent),
            key(name, indent)?,
            scalar(value, indent + 4)?
        ));
        Ok(())
    }

    let mut lines = Vec::new();
    if handle.coverage.from.is_empty() && handle.coverage.to.is_empty() {
        lines.push("coverage: {}".to_owned());
    } else {
        lines.push("coverage:".to_owned());
        if !handle.coverage.from.is_empty() {
            push_scalar(&mut lines, 4, "from", &handle.coverage.from)?;
        }
        if !handle.coverage.to.is_empty() {
            push_scalar(&mut lines, 4, "to", &handle.coverage.to)?;
        }
    }
    push_scalar(&mut lines, 0, "created", &handle.created)?;
    push_scalar(&mut lines, 0, "dataset_id", &handle.slug)?;
    if !handle.identity_field.is_empty() {
        push_scalar(&mut lines, 0, "identity_field", &handle.identity_field)?;
    }
    lines.push("provenance:".to_owned());
    push_scalar(&mut lines, 4, "imported_at", &handle.provenance.imported_at)?;
    if !handle.provenance.source_name.is_empty() {
        push_scalar(&mut lines, 4, "source_name", &handle.provenance.source_name)?;
    }
    push_scalar(
        &mut lines,
        4,
        "source_sha256",
        &handle.provenance.source_sha256,
    )?;
    if !handle.refresh_command.is_empty() {
        push_scalar(&mut lines, 0, "refresh_command", &handle.refresh_command)?;
    }
    push_scalar(&mut lines, 0, "retention_rule", &handle.retention_rule)?;
    if handle.schema.is_empty() {
        lines.push("schema: {}".to_owned());
    } else {
        lines.push("schema:".to_owned());
        for (column, property) in &handle.schema {
            lines.push(format!("    {}:", key(column, 4)?));
            if !property.r#type.is_empty() {
                push_scalar(&mut lines, 8, "type", &property.r#type)?;
            }
            if !property.label.is_empty() {
                push_scalar(&mut lines, 8, "label", &property.label)?;
            }
            if !property.options.is_empty() {
                lines.push("        options:".to_owned());
                for option in &property.options {
                    lines.push(format!("            - {}", scalar(option, 14)?));
                }
            }
            if !property.description.is_empty() {
                push_scalar(&mut lines, 8, "description", &property.description)?;
            }
            if !property.default.is_empty() {
                push_scalar(&mut lines, 8, "default", &property.default)?;
            }
        }
    }
    push_scalar(&mut lines, 0, "sensitivity", &handle.sensitivity)?;
    push_scalar(&mut lines, 0, "source", &handle.source)?;
    lines.push("tags:".to_owned());
    lines.push("    - dataset".to_owned());
    push_scalar(&mut lines, 0, "title", &handle.title)?;
    lines.push("type: dataset".to_owned());

    Ok(format!(
        "---\n{}\n---\n\n# {}\n\nSource: `{}`\n",
        lines.join("\n"),
        handle.title,
        handle.source
    )
    .into_bytes())
}

/// Port of `service.replaceDatasetRows`: deduplicate by key (last row wins),
/// order by key, and serialise each value map with Go's `encoding/json`.
pub fn project_rows(
    slug: &str,
    rows: &[Row],
    source_path: &str,
) -> Result<Vec<SidecarRow>, DatasetError> {
    let mut by_key: BTreeMap<&str, &Row> = BTreeMap::new();
    for row in rows {
        by_key.insert(row.key.as_str(), row);
    }
    let mut projected = Vec::with_capacity(by_key.len());
    for row in by_key.values() {
        projected.push(SidecarRow {
            dataset_slug: slug.to_string(),
            row_key: row.key.clone(),
            identity: row.identity.clone(),
            values_json: go_json_object(&row.values)?,
            source_path: if row.source_path.is_empty() {
                source_path.to_string()
            } else {
                row.source_path.clone()
            },
            row_number: row.row_number,
        });
    }
    Ok(projected)
}

/// Serialises a value map the way `encoding/json` marshals a
/// `map[string]interface{}`: keys sorted, HTML escapes applied, finite floats
/// use Go's exponent thresholds, and nonfinite floats return Marshal's error.
pub fn go_json_object(values: &BTreeMap<String, GoValue>) -> Result<String, DatasetError> {
    let mut out = String::from("{");
    let mut first = true;
    for (key, value) in values {
        if !first {
            out.push(',');
        }
        first = false;
        out.push_str(&go_json_string(key));
        out.push(':');
        out.push_str(&go_json_value(value)?);
    }
    out.push('}');
    Ok(out)
}

fn go_json_value(value: &GoValue) -> Result<String, DatasetError> {
    match value {
        GoValue::Text(text) => Ok(go_json_string(text)),
        GoValue::Boolean(flag) => Ok(flag.to_string()),
        GoValue::Number(number) => go_json_float(*number),
    }
}

/// Go uses fixed notation for `1e-6 <= |x| < 1e21` and scientific notation
/// outside that range. Rust's float formatter provides the same shortest
/// round-tripping digits; this selects Go's notation and exponent sign.
fn go_json_float(number: f64) -> Result<String, DatasetError> {
    if !number.is_finite() {
        let rendered = if number.is_nan() {
            "NaN"
        } else if number.is_sign_negative() {
            "-Inf"
        } else {
            "+Inf"
        };
        return Err(DatasetError::UnsupportedJsonValue(rendered.to_owned()));
    }
    if number == 0.0 && number.is_sign_negative() {
        return Ok("-0".to_owned());
    }
    let absolute = number.abs();
    if absolute != 0.0 && !(1e-6..1e21).contains(&absolute) {
        let scientific = format!("{number:e}");
        let (mantissa, exponent) = scientific
            .split_once('e')
            .expect("scientific formatting contains an exponent");
        let sign = if exponent.starts_with('-') { "" } else { "+" };
        return Ok(format!("{mantissa}e{sign}{exponent}"));
    }
    Ok(number.to_string())
}

/// Go's `encoding/json` string quoting: control characters escaped, `<`, `>`
/// and `&` escaped as `\u00XX`, and the two Unicode line separators escaped so
/// the output stays valid in all embedding contexts.
pub fn go_json_string(input: &str) -> String {
    let mut out = String::with_capacity(input.len() + 2);
    out.push('"');
    for ch in input.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{08}' => out.push_str("\\b"),
            '\u{0c}' => out.push_str("\\f"),
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::go_equal_fold;

    #[test]
    fn go_equal_fold_keeps_go_only_cycles_and_rejects_newer_rust_edges() {
        for (left, right) in [("µ", "Μ"), ("ͅ", "Ι"), ("ᲈ", "Ꙋ")] {
            assert!(
                go_equal_fold(left, right),
                "{left:?} should fold to {right:?}"
            );
        }
        for (left, right) in [
            ("ı", "I"),
            ("\u{1c89}", "\u{1c8a}"),
            ("\u{10d50}", "\u{10d70}"),
            ("\u{16ea0}", "\u{16ebb}"),
        ] {
            assert!(
                !go_equal_fold(left, right),
                "{left:?} must not fold to {right:?} under Go 1.26.6"
            );
        }
    }
}
