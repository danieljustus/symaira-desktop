//! Dataset sync projection — Go parity for contract row DATA-001.
//!
//! Go sources: `internal/dataset/dataset.go` (`ParseCSV`, `CanonicalRowHash`,
//! `inferType`, `convertValue`, `parseDate`) and `internal/service/dataset.go`
//! (`replaceDatasetRows`, which deduplicates by row key, orders by that key and
//! serialises each value map with `encoding/json`).
//!
//! The replay reproduces the sidecar projection byte-for-byte from the CSV the
//! Go oracle recorded: row keys, identities, the `values_json` blob and row
//! numbers.
//!
//! ponytail: only the read/projection half of DATA-001 is ported here. Go's
//! `Handle.Render()` YAML writer (the "manifest first" half) and `StoreRaw`'s
//! same-day collision suffix are deliberately absent — `symdesk-vault` has no
//! YAML writer and adding one is a larger decision than this slice warrants.
//! Upgrade path: port the writer, then let this fixture assert the produced
//! handle bytes instead of only the Go-recorded ones.
//!
//! ponytail: the CSV reader below implements the RFC4180 subset the oracle
//! exercises — quoted fields with `""` escapes, `\n` and `\r\n` records,
//! unquoted leading spaces preserved, and the record-length check Go's
//! `FieldsPerRecord` performs. Upgrade path: swap `read_all_csv` for a real
//! CSV crate if a fixture case needs comments, custom separators, BOM
//! stripping or Go's `LazyQuotes` leniency.

use std::collections::{BTreeMap, HashMap};

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
}

impl std::fmt::Display for DatasetError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::ReaderRequired => write!(f, "csv reader is required"),
            Self::ReadCsv(detail) => write!(f, "read csv: {detail}"),
            Self::EmptyCsv => write!(f, "csv is empty"),
            Self::EmptyColumnName(index) => write!(f, "csv column {index} has an empty name"),
            Self::DuplicateColumn(name) => write!(f, "csv has duplicate column {name:?}"),
            Self::MissingIdentity(name) => {
                write!(f, "identity field {name:?} is not a CSV column")
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
                write!(f, "row {row} column {column:?}: {detail}")
            }
            Self::InvalidNumber(value) => write!(f, "invalid number {value:?}"),
            Self::InvalidBoolean(value) => write!(f, "invalid boolean {value:?}"),
            Self::InvalidDate(value) => write!(f, "invalid date {value:?}"),
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

/// Splits a CSV document into records the way `encoding/csv.Reader.ReadAll`
/// does for the subset the oracle exercises.
///
/// ponytail: quoted fields, `""` escapes and both record terminators are
/// handled; bare quotes inside unquoted fields are rejected rather than
/// tolerated, because Go's default reader rejects them too.
pub fn read_all_csv(input: &str) -> Result<Vec<Vec<String>>, DatasetError> {
    if input.is_empty() {
        return Err(DatasetError::EmptyCsv);
    }
    let bytes = input.as_bytes();
    let mut records: Vec<Vec<String>> = Vec::new();
    let mut record: Vec<String> = Vec::new();
    let mut field = String::new();
    let mut in_quotes = false;
    let mut field_started = false;
    let mut index = 0usize;

    while index < bytes.len() {
        let byte = bytes[index];
        if in_quotes {
            if byte == b'"' {
                if bytes.get(index + 1) == Some(&b'"') {
                    field.push('"');
                    index += 2;
                    continue;
                }
                in_quotes = false;
                index += 1;
                continue;
            }
            field.push(byte as char);
            index += 1;
            continue;
        }
        match byte {
            b'"' if !field_started => {
                in_quotes = true;
                field_started = true;
                index += 1;
            }
            b'"' => return Err(DatasetError::ReadCsv(format!("bare \" in field {}", index))),
            b',' => {
                record.push(std::mem::take(&mut field));
                field_started = false;
                index += 1;
            }
            b'\r' if bytes.get(index + 1) == Some(&b'\n') => {
                record.push(std::mem::take(&mut field));
                records.push(std::mem::take(&mut record));
                field_started = false;
                index += 2;
            }
            b'\n' => {
                record.push(std::mem::take(&mut field));
                records.push(std::mem::take(&mut record));
                field_started = false;
                index += 1;
            }
            _ => {
                field.push(byte as char);
                field_started = true;
                index += 1;
            }
        }
    }

    // A trailing newline already closed the record; otherwise the last field
    // and record still have to be flushed, exactly as Go does at EOF.
    if !field.is_empty() || !record.is_empty() || field_started {
        record.push(std::mem::take(&mut field));
        records.push(std::mem::take(&mut record));
    }

    // Go's reader reports a field-count mismatch through `ReadAll`, which this
    // loop cannot see yet: enforce it against the first record's width.
    if let Some(want) = records.first().map(Vec::len) {
        for (offset, rec) in records.iter().enumerate() {
            if rec.len() != want {
                return Err(DatasetError::WrongFieldCount { record: offset + 1 });
            }
        }
    }
    Ok(records)
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
        let folded = header.to_lowercase();
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
                if header.eq_ignore_ascii_case(identity_field) {
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
            row_number,
        });
    }
    Ok((rows, schema))
}

fn has_header(headers: &[String], wanted: &str) -> bool {
    headers
        .iter()
        .any(|header| header.eq_ignore_ascii_case(wanted))
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

/// `strconv.ParseFloat` for the forms the oracle uses: optional sign, decimal
/// point, exponent, and Go's `Inf`/`NaN` spellings.
///
/// ponytail: underscore-grouped literals and hex floats are not accepted here;
/// upgrade path: add them if a fixture case records one.
fn parse_go_float(value: &str) -> Option<f64> {
    match value {
        "Inf" | "+Inf" => return Some(f64::INFINITY),
        "-Inf" => return Some(f64::NEG_INFINITY),
        "NaN" => return Some(f64::NAN),
        _ => {}
    }
    value.parse::<f64>().ok()
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

/// Port of `dataset.parseDate`: the four layouts Go tries, in order.
fn parse_date(value: &str) -> bool {
    if value.as_bytes().get(10) == Some(&b'T') {
        return parse_rfc3339(value);
    }
    if value.len() == 10 {
        if let (Some(y), Some(m), Some(d)) = (value.get(0..4), value.get(5..7), value.get(8..10)) {
            let numeric = [y, m, d]
                .iter()
                .all(|part| part.chars().all(|c| c.is_ascii_digit()));
            if numeric && value.as_bytes()[4] == b'-' && value.as_bytes()[7] == b'-' {
                return is_valid_calendar_date(y, m, d);
            }
        }
    }
    if value.len() >= 19 && value.as_bytes()[10] == b' ' {
        let (date, time_part) = value.split_at(10);
        let time_part = &time_part[1..];
        return is_valid_time(
            time_part.get(0..2),
            time_part.get(3..5),
            time_part.get(6..8),
        ) && is_valid_calendar_date(&date[0..4], &date[5..7], &date[8..10]);
    }
    if value.len() == 16 && value.as_bytes()[10] == b' ' {
        let (date, time_part) = value.split_at(10);
        let time_part = &time_part[1..];
        return is_valid_time(time_part.get(0..2), time_part.get(3..5), None)
            && is_valid_calendar_date(&date[0..4], &date[5..7], &date[8..10]);
    }
    false
}

/// `time.Parse(time.RFC3339, ...)`: `YYYY-MM-DDTHH:MM:SS` with an optional
/// fractional part and either `Z` or a numeric offset.
fn parse_rfc3339(value: &str) -> bool {
    let bytes = value.as_bytes();
    if value.len() < 20
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || bytes[10] != b'T'
        || bytes[13] != b':'
        || bytes[16] != b':'
        || !is_valid_calendar_date(&value[0..4], &value[5..7], &value[8..10])
        || !is_valid_time(
            Some(&value[11..13]),
            Some(&value[14..16]),
            Some(&value[17..19]),
        )
    {
        return false;
    }
    let rest = match value[19..].strip_prefix('.') {
        Some(fraction) => {
            let digits = fraction.chars().take_while(|c| c.is_ascii_digit()).count();
            if digits == 0 {
                return false;
            }
            &fraction[digits..]
        }
        None => &value[19..],
    };
    if rest == "Z" {
        return true;
    }
    let offset = rest.as_bytes();
    offset.len() == 6
        && matches!(offset[0], b'+' | b'-')
        && offset[3] == b':'
        && offset[1..3].iter().all(u8::is_ascii_digit)
        && offset[4..6].iter().all(u8::is_ascii_digit)
}

fn is_valid_calendar_date(year: &str, month: &str, day: &str) -> bool {
    let (Ok(y), Ok(m), Ok(d)) = (
        year.parse::<i32>(),
        month.parse::<u32>(),
        day.parse::<u32>(),
    ) else {
        return false;
    };
    if !(1..=12).contains(&m) || d < 1 {
        return false;
    }
    let leap = (y % 4 == 0 && y % 100 != 0) || y % 400 == 0;
    let days = match m {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if leap => 29,
        2 => 28,
        _ => return false,
    };
    d <= days
}

fn is_valid_time(hour: Option<&str>, minute: Option<&str>, second: Option<&str>) -> bool {
    let parse = |part: Option<&str>, max: u32| -> Option<u32> {
        let text = part?;
        if text.len() != 2 || !text.chars().all(|c| c.is_ascii_digit()) {
            return None;
        }
        let value: u32 = text.parse().ok()?;
        (value <= max).then_some(value)
    };
    if parse(hour, 23).is_none() || parse(minute, 59).is_none() {
        return false;
    }
    match second {
        Some(_) => parse(second, 60).is_some(),
        None => true,
    }
}

/// Port of `service.replaceDatasetRows`: deduplicate by key (last row wins),
/// order by key, and serialise each value map with Go's `encoding/json`.
pub fn project_rows(slug: &str, rows: &[Row], source_path: &str) -> Vec<SidecarRow> {
    let mut by_key: BTreeMap<&str, &Row> = BTreeMap::new();
    for row in rows {
        by_key.insert(row.key.as_str(), row);
    }
    by_key
        .values()
        .map(|row| SidecarRow {
            dataset_slug: slug.to_string(),
            row_key: row.key.clone(),
            identity: row.identity.clone(),
            values_json: go_json_object(&row.values),
            source_path: source_path.to_string(),
            row_number: row.row_number,
        })
        .collect()
}

/// Serialises a value map the way `encoding/json` marshals a
/// `map[string]interface{}`: keys sorted, HTML escapes applied, and integral
/// floats printed without a `.0` suffix.
pub fn go_json_object(values: &BTreeMap<String, GoValue>) -> String {
    let mut out = String::from("{");
    let mut first = true;
    for (key, value) in values {
        if !first {
            out.push(',');
        }
        first = false;
        out.push_str(&go_json_string(key));
        out.push(':');
        out.push_str(&go_json_value(value));
    }
    out.push('}');
    out
}

fn go_json_value(value: &GoValue) -> String {
    match value {
        GoValue::Text(text) => go_json_string(text),
        GoValue::Boolean(flag) => flag.to_string(),
        GoValue::Number(number) => go_json_float(*number),
    }
}

/// Go prints the shortest representation that round-trips, and drops the
/// fractional part for integral values (`json.Marshal(8.0)` yields `8`).
fn go_json_float(number: f64) -> String {
    if number.is_nan() || number.is_infinite() {
        return "null".to_string();
    }
    if number == number.trunc() && number.abs() < 9.007_199_254_740_992e15 {
        return format!("{}", number as i64);
    }
    let shortest = format!("{number}");
    if shortest.contains(['e', 'E']) {
        return shortest
            .replace('e', "e+")
            .replace("e++", "e+")
            .replace("e+-", "e-");
    }
    shortest
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
