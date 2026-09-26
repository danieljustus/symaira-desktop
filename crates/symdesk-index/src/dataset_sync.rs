use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    io::{self, Read as _, Write as _},
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use cap_std::{
    ambient_authority,
    fs::{Dir, OpenOptions},
};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use symdesk_vault::{
    Coverage, DatasetHandle, PropertyConfig, Provenance, dataset, go_lowercase, go_quote,
    parse_bytes, parse_dataset_handle,
};
use thiserror::Error;
use time::{OffsetDateTime, UtcOffset, format_description::well_known::Rfc3339};

use crate::{IndexedDocument, Sidecar, SidecarError};

const RAW_DIR: &str = "datasets";

static TEMP_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct DatasetSyncRow {
    pub identity: String,
    #[serde(default)]
    pub values: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct DatasetSyncOptions {
    pub title: String,
    pub slug: String,
    pub identity_field: String,
    #[serde(default)]
    pub schema: BTreeMap<String, PropertyConfig>,
    pub provenance: Provenance,
    pub sensitivity: String,
    pub retention_rule: String,
    #[serde(default)]
    pub rows: Vec<DatasetSyncRow>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct DatasetSyncResult {
    pub slug: String,
    pub rows: usize,
    pub imported_rows: usize,
    pub raw_path: String,
    pub handle_path: String,
    pub idempotent: bool,
}

#[derive(Clone, Debug, Default)]
pub struct DatasetImportOptions {
    pub title: String,
    pub slug: String,
    pub identity_field: String,
    pub schema: BTreeMap<String, PropertyConfig>,
    pub refresh_command: String,
    pub sensitivity: String,
    pub retention_rule: String,
    pub now: Option<OffsetDateTime>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct DatasetImportResult {
    pub handle_path: String,
    pub raw_path: String,
    pub slug: String,
    pub rows: usize,
    pub columns: BTreeMap<String, PropertyConfig>,
    pub source_sha256: String,
    pub sensitivity: String,
    pub retention_rule: String,
}

#[derive(Debug, Error)]
pub enum DatasetSyncError {
    #[error("{0}")]
    Contract(String),
    #[error(transparent)]
    Dataset(#[from] dataset::DatasetError),
    #[error(transparent)]
    Sidecar(#[from] SidecarError),
    #[error(transparent)]
    Io(#[from] io::Error),
}

/// A narrow production service boundary for the Markdown/CSV-authoritative
/// dataset sync operation. Optional parts preserve the Go service's dependency
/// validation boundary without introducing an in-memory substitute.
pub struct DatasetSyncService<'a> {
    vault_root: Option<&'a Path>,
    sidecar: Option<&'a mut Sidecar>,
}

impl<'a> DatasetSyncService<'a> {
    #[must_use]
    pub fn new(vault_root: &'a Path, sidecar: &'a mut Sidecar) -> Self {
        Self {
            vault_root: Some(vault_root),
            sidecar: Some(sidecar),
        }
    }

    #[must_use]
    pub fn from_parts(vault_root: Option<&'a Path>, sidecar: Option<&'a mut Sidecar>) -> Self {
        Self {
            vault_root,
            sidecar,
        }
    }

    /// Imports one explicitly selected CSV as an exact-byte raw asset, writes
    /// its authoritative Markdown handle, then rebuilds the sidecar from every
    /// raw CSV for the dataset. Repeated imports on one UTC date use the same
    /// collision-safe suffix sequence as Go's `StoreRaw`.
    ///
    /// # Errors
    /// Returns Go-compatible validation, source I/O, parser, vault, projection,
    /// or sidecar errors. Raw and Markdown writes remain if later projection
    /// fails, matching the Go service ordering.
    pub fn import_csv(
        &mut self,
        source: &Path,
        options: DatasetImportOptions,
    ) -> Result<DatasetImportResult, DatasetSyncError> {
        let Some(vault_root) = self.vault_root else {
            return Err(DatasetSyncError::Contract(
                "dataset import requires a vault".to_owned(),
            ));
        };
        let Some(sidecar) = self.sidecar.as_deref_mut() else {
            return Err(DatasetSyncError::Contract(
                "dataset import requires a sidecar".to_owned(),
            ));
        };
        let (sensitivity, retention_rule) = dataset::normalize_policy(
            options.sensitivity.as_str(),
            options.retention_rule.as_str(),
        )?;
        let metadata = fs::metadata(source)
            .map_err(|error| DatasetSyncError::Contract(format!("stat dataset source: {error}")))?;
        let extension = source
            .extension()
            .and_then(|value| value.to_str())
            .unwrap_or("");
        if metadata.is_dir() || !extension.eq_ignore_ascii_case("csv") {
            return Err(DatasetSyncError::Contract(
                "dataset import supports CSV files only".to_owned(),
            ));
        }
        let bytes = fs::read(source)
            .map_err(|error| DatasetSyncError::Contract(format!("read dataset source: {error}")))?;
        let text = String::from_utf8(bytes.clone())
            .map_err(|error| DatasetSyncError::Contract(format!("read csv: {error}")))?;
        let declared = options
            .schema
            .iter()
            .map(|(column, property)| {
                (
                    column.clone(),
                    dataset::PropertyConfig {
                        label: property.label.clone(),
                        kind: property.r#type.clone(),
                    },
                )
            })
            .collect();
        let (rows, inferred) =
            dataset::parse_csv(&text, &declared, options.identity_field.as_str())?;
        let schema = inferred
            .iter()
            .map(|(column, parsed)| {
                let mut property = options.schema.get(column).cloned().unwrap_or_default();
                property.r#type.clone_from(&parsed.kind);
                property.label.clone_from(&parsed.label);
                (column.clone(), property)
            })
            .collect::<BTreeMap<_, _>>();
        let now = options.now.unwrap_or_else(OffsetDateTime::now_utc);
        let source_name = source
            .file_name()
            .and_then(|name| name.to_str())
            .ok_or_else(|| {
                DatasetSyncError::Contract("dataset source file name is not UTF-8".to_owned())
            })?;
        let title = if options.title.trim().is_empty() {
            source_name
                .rsplit_once('.')
                .map_or(source_name, |(stem, _)| stem)
                .to_owned()
        } else {
            options.title.trim().to_owned()
        };
        let slug = if options.slug.trim().is_empty() {
            slugify(&title)
        } else {
            options.slug.trim().to_owned()
        };
        if slug != slugify(&slug) {
            return Err(DatasetSyncError::Contract(format!(
                "dataset slug {} is not filesystem-safe",
                go_quote(&slug)
            )));
        }

        let root = Dir::open_ambient_dir(vault_root, ambient_authority())?;
        let date = now.to_offset(UtcOffset::UTC).date();
        let raw_name = format!("{}.csv", date);
        let raw_path = store_raw(&root, &slug, &raw_name, &bytes).map_err(|error| {
            DatasetSyncError::Contract(format!("store dataset source: {error}"))
        })?;
        let handle_path = format!("{RAW_DIR}/{slug}.md");
        let old_handle = read_handle(&root, &handle_path).ok();
        let created = old_handle
            .as_ref()
            .map_or_else(|| format_rfc3339(now), |handle| handle.created.clone());
        let handle = DatasetHandle {
            path: handle_path.clone(),
            slug: slug.clone(),
            title: title.clone(),
            created,
            source: raw_path.clone(),
            schema,
            coverage: coverage_for_rows(&rows, &inferred),
            provenance: Provenance {
                imported_at: format_rfc3339(now),
                source_name: source_name.to_owned(),
                source_sha256: symdesk_vault::sha256_hex(&bytes),
            },
            identity_field: options.identity_field,
            refresh_command: options.refresh_command,
            sensitivity: sensitivity.clone(),
            retention_rule: retention_rule.clone(),
        };
        let handle_bytes = dataset::render_handle(&handle)?;
        create_dir_all_0755(&root, Path::new(RAW_DIR)).map_err(|error| {
            DatasetSyncError::Contract(format!("create dataset handle directory: {error}"))
        })?;
        let handle_parent = root.open_dir(RAW_DIR)?;
        write_atomic(&handle_parent, &format!("{slug}.md"), &handle_bytes).map_err(|error| {
            DatasetSyncError::Contract(format!("write dataset handle: {error}"))
        })?;

        let materialized = read_raw_files(&root, &slug, &handle.schema, &handle.identity_field)
            .map_err(|error| {
                DatasetSyncError::Contract(format!("rebuild dataset rows: {error}"))
            })?;
        let projected = dataset::project_rows(&slug, &materialized, "")?;
        sidecar
            .replace_dataset_rows(&slug, &projected)
            .map_err(|error| DatasetSyncError::Contract(format!("store dataset rows: {error}")))?;
        let document = parse_bytes(&handle_path, &handle_bytes).map_err(|error| {
            DatasetSyncError::Contract(format!("parse dataset handle: {error}"))
        })?;
        let indexed = IndexedDocument::from_vault(&document, None)?;
        sidecar.index_document(&indexed)?;

        Ok(DatasetImportResult {
            handle_path,
            raw_path,
            slug,
            rows: projected.len(),
            columns: handle.schema,
            source_sha256: handle.provenance.source_sha256,
            sensitivity,
            retention_rule,
        })
    }

    /// Persists producer rows as a native CSV snapshot, writes the authoritative
    /// Markdown handle, rebuilds the real SQLite sidecar, and indexes the handle.
    ///
    /// # Errors
    /// Returns the first Go-compatible validation, filesystem, projection, or
    /// SQLite error. Raw and handle writes intentionally remain when projection
    /// or sidecar replacement fails, matching the production oracle.
    pub fn sync(
        &mut self,
        options: DatasetSyncOptions,
    ) -> Result<DatasetSyncResult, DatasetSyncError> {
        let Some(vault_root) = self.vault_root else {
            return Err(DatasetSyncError::Contract(
                "dataset sync requires a vault and sidecar".to_owned(),
            ));
        };
        let Some(sidecar) = self.sidecar.as_deref_mut() else {
            return Err(DatasetSyncError::Contract(
                "dataset sync requires a vault and sidecar".to_owned(),
            ));
        };

        let (sensitivity, retention_rule) = dataset::normalize_policy(
            options.sensitivity.as_str(),
            options.retention_rule.as_str(),
        )?;
        if options.identity_field.trim().is_empty() {
            return Err(DatasetSyncError::Contract(
                "dataset sync requires an identity field".to_owned(),
            ));
        }
        if options.provenance.source_name.trim().is_empty()
            || options.provenance.source_sha256.trim().is_empty()
            || options.provenance.imported_at.trim().is_empty()
        {
            return Err(DatasetSyncError::Contract(
                "dataset sync requires explicit provenance: source_name, source_sha256, imported_at"
                    .to_owned(),
            ));
        }
        let imported_at = parse_imported_at(&options.provenance.imported_at)?;
        let slug = if options.slug.trim().is_empty() {
            slugify(&options.title)
        } else {
            options.slug.trim().to_owned()
        };
        if slug.is_empty() || slug != slugify(&slug) {
            return Err(DatasetSyncError::Contract(format!(
                "dataset slug {} is not filesystem-safe",
                go_quote(&slug)
            )));
        }
        if options.rows.is_empty() {
            return Err(DatasetSyncError::Contract(
                "dataset sync requires at least one row".to_owned(),
            ));
        }
        let mut identities = BTreeSet::new();
        for row in &options.rows {
            if row.identity.trim().is_empty() {
                return Err(DatasetSyncError::Contract(
                    "dataset sync requires an identity for every row".to_owned(),
                ));
            }
            if !identities.insert(row.identity.as_str()) {
                return Err(DatasetSyncError::Contract(format!(
                    "duplicate dataset row identity {}",
                    go_quote(&row.identity)
                )));
            }
        }

        let root = Dir::open_ambient_dir(vault_root, ambient_authority())?;
        let handle_path = format!("{RAW_DIR}/{slug}.md");
        let existing = read_handle(&root, &handle_path).ok();
        if let Some(handle) = existing.as_ref()
            && handle.provenance.source_sha256 == options.provenance.source_sha256
            && handle.provenance.source_name == options.provenance.source_name
        {
            let mut rows = sidecar.dataset_rows(&slug)?;
            if rows.is_empty() {
                let materialized =
                    read_raw_files(&root, &slug, &handle.schema, &handle.identity_field)?;
                let projected = dataset::project_rows(&slug, &materialized, "")?;
                sidecar.replace_dataset_rows(&slug, &projected)?;
                rows = sidecar.dataset_rows(&slug)?;
            }
            return Ok(DatasetSyncResult {
                slug,
                rows: rows.len(),
                imported_rows: options.rows.len(),
                raw_path: handle.source.clone(),
                handle_path,
                idempotent: true,
            });
        }

        let (csv, schema) = sync_csv(&options.rows, &options.identity_field, &options.schema)?;
        let raw_name = format!("{imported_at}.csv");
        let raw_path = store_raw(&root, &slug, &raw_name, &csv)?;

        let mut title = options.title.trim().to_owned();
        if title.is_empty()
            && let Some(handle) = existing.as_ref()
        {
            title.clone_from(&handle.title);
        }
        if title.is_empty() {
            title.clone_from(&slug);
        }
        let created = existing.as_ref().map_or_else(
            || options.provenance.imported_at.clone(),
            |handle| handle.created.clone(),
        );
        let handle = DatasetHandle {
            path: handle_path.clone(),
            slug: slug.clone(),
            title,
            created,
            source: raw_path.clone(),
            schema: schema.clone(),
            coverage: existing
                .as_ref()
                .map_or_else(Coverage::default, |handle| handle.coverage.clone()),
            provenance: options.provenance,
            identity_field: options.identity_field.clone(),
            refresh_command: existing
                .as_ref()
                .map_or_else(String::new, |handle| handle.refresh_command.clone()),
            sensitivity,
            retention_rule,
        };
        let handle_bytes = dataset::render_handle(&handle)?;
        create_dir_all_0755(&root, Path::new(RAW_DIR))?;
        let handle_parent = root.open_dir(RAW_DIR)?;
        write_atomic(&handle_parent, &format!("{slug}.md"), &handle_bytes)?;

        let materialized = read_raw_files(&root, &slug, &schema, &options.identity_field)?;
        let projected = dataset::project_rows(&slug, &materialized, "")?;
        sidecar.replace_dataset_rows(&slug, &projected)?;

        if let Ok(document) = parse_bytes(&handle_path, &handle_bytes) {
            let indexed = IndexedDocument::from_vault(&document, None)?;
            sidecar.index_document(&indexed)?;
        }

        Ok(DatasetSyncResult {
            slug,
            rows: projected.len(),
            imported_rows: options.rows.len(),
            raw_path,
            handle_path,
            idempotent: false,
        })
    }
}

fn format_rfc3339(value: OffsetDateTime) -> String {
    value
        .format(&Rfc3339)
        .expect("valid timestamp formats as RFC3339")
}

fn coverage_for_rows(
    rows: &[dataset::Row],
    schema: &BTreeMap<String, dataset::PropertyConfig>,
) -> Coverage {
    let mut date_columns = schema
        .iter()
        .filter(|(_, property)| property.kind == "date")
        .map(|(column, _)| column.as_str())
        .collect::<Vec<_>>();
    date_columns.sort_unstable();
    let mut dates = rows
        .iter()
        .flat_map(|row| {
            date_columns
                .iter()
                .filter_map(move |column| match row.values.get(*column) {
                    Some(dataset::GoValue::Text(value)) if !value.is_empty() => Some(value.clone()),
                    _ => None,
                })
        })
        .collect::<Vec<_>>();
    dates.sort();
    match (dates.first(), dates.last()) {
        (Some(from), Some(to)) => Coverage {
            from: from.clone(),
            to: to.clone(),
        },
        _ => Coverage::default(),
    }
}

fn parse_imported_at(value: &str) -> Result<String, DatasetSyncError> {
    parse_go_rfc3339(value)
        .map_err(|detail| DatasetSyncError::Contract(format!("invalid imported_at: {detail}")))
}

fn parse_go_rfc3339(value: &str) -> Result<String, String> {
    let bytes = value.as_bytes();
    let cannot_parse = |value_element: &str, layout_element: &str| {
        format!(
            "parsing time {} as \"2006-01-02T15:04:05Z07:00\": cannot parse {} as {}",
            time_quote(value),
            time_quote(value_element),
            time_quote(layout_element)
        )
    };
    let range_error =
        |field: &str| format!("parsing time {}: {field} out of range", time_quote(value));
    let extra_text = |text: &str| {
        format!(
            "parsing time {}: extra text: {}",
            time_quote(value),
            time_quote(text)
        )
    };

    if bytes.len() < 10 {
        return Err(cannot_parse(value, "2006"));
    }
    let year = parse_fixed_decimal(bytes, 0, 4).ok_or_else(|| cannot_parse(value, "2006"))?;
    if bytes.get(4) != Some(&b'-') {
        return Err(cannot_parse(&value[4..], "-"));
    }
    let month = parse_fixed_decimal(bytes, 5, 2).ok_or_else(|| cannot_parse(&value[5..], "01"))?;
    if bytes.get(7) != Some(&b'-') {
        return Err(cannot_parse(&value[7..], "-"));
    }
    let day = parse_fixed_decimal(bytes, 8, 2).ok_or_else(|| cannot_parse(&value[8..], "02"))?;
    if !(1..=12).contains(&month) {
        return Err(range_error("month"));
    }
    let month = u8::try_from(month).expect("month range checked");
    if day < 1 || day > i32::from(days_in_month(year, month)) {
        return Err(range_error("day"));
    }
    let day = u8::try_from(day).expect("day range checked");

    if bytes.len() == 10 {
        return Err(cannot_parse("", "T"));
    }
    if bytes[10] != b'T' {
        return Err(cannot_parse(&value[10..], "T"));
    }

    let mut index = 11;
    let hour_start = index;
    while index < bytes.len() && bytes[index].is_ascii_digit() && index - hour_start < 2 {
        index += 1;
    }
    if index == hour_start || bytes.get(index) != Some(&b':') {
        return Err(cannot_parse(&value[hour_start..], "15"));
    }
    let hour = parse_decimal(&bytes[hour_start..index]).expect("hour digits were checked");
    if hour > 23 {
        return Err(range_error("hour"));
    }
    index += 1;

    let minute =
        parse_fixed_decimal(bytes, index, 2).ok_or_else(|| cannot_parse(&value[index..], "04"))?;
    if minute > 59 {
        return Err(range_error("minute"));
    }
    index += 2;
    if bytes.get(index) != Some(&b':') {
        return Err(cannot_parse(&value[index..], ":"));
    }
    index += 1;
    let second =
        parse_fixed_decimal(bytes, index, 2).ok_or_else(|| cannot_parse(&value[index..], "05"))?;
    if second > 59 {
        return Err(range_error("second"));
    }
    index += 2;

    if matches!(bytes.get(index), Some(b'.' | b','))
        && bytes.get(index + 1).is_some_and(u8::is_ascii_digit)
    {
        index += 1;
        while bytes.get(index).is_some_and(u8::is_ascii_digit) {
            index += 1;
        }
    }

    let zone = &value[index..];
    let (offset_minutes, zone_bytes) = if zone.starts_with('Z') {
        (0i64, 1usize)
    } else if matches!(zone.as_bytes().first(), Some(b'+' | b'-')) {
        let Some(zone_hour) = parse_fixed_decimal(zone.as_bytes(), 1, 2) else {
            return Err(cannot_parse(zone, "Z07:00"));
        };
        if zone.as_bytes().get(3) != Some(&b':') {
            return Err(cannot_parse(zone, "Z07:00"));
        }
        let Some(zone_minute) = parse_fixed_decimal(zone.as_bytes(), 4, 2) else {
            return Err(cannot_parse(zone, "Z07:00"));
        };
        if zone_hour > 24 {
            return Err(range_error("time zone offset hour"));
        }
        if zone_minute > 60 {
            return Err(range_error("time zone offset minute"));
        }
        let total = i64::from(zone_hour * 60 + zone_minute);
        let offset = if zone.as_bytes()[0] == b'-' {
            -total
        } else {
            total
        };
        (offset, 6usize)
    } else {
        return Err(cannot_parse(zone, "Z07:00"));
    };

    if zone_bytes < zone.len() {
        return Err(extra_text(&zone[zone_bytes..]));
    }

    // Preserve Go's valid year carry beyond time::Date's representable range.
    let local_minute = i64::from(hour * 60 + minute) - offset_minutes;
    let day_delta = local_minute.div_euclid(24 * 60);
    let (utc_year, utc_month, utc_day) = add_calendar_days(year, month, day, day_delta);
    Ok(format_go_date(utc_year, utc_month, utc_day))
}

fn days_in_month(year: i32, month: u8) -> u8 {
    match month {
        2 if year.rem_euclid(4) == 0
            && (year.rem_euclid(100) != 0 || year.rem_euclid(400) == 0) =>
        {
            29
        }
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

fn add_calendar_days(mut year: i32, mut month: u8, mut day: u8, delta: i64) -> (i32, u8, u8) {
    if delta < 0 {
        for _ in 0..delta.unsigned_abs() {
            if day > 1 {
                day -= 1;
            } else if month > 1 {
                month -= 1;
                day = days_in_month(year, month);
            } else {
                year -= 1;
                month = 12;
                day = 31;
            }
        }
    } else {
        for _ in 0..delta as u64 {
            if day < days_in_month(year, month) {
                day += 1;
            } else if month < 12 {
                month += 1;
                day = 1;
            } else {
                year += 1;
                month = 1;
                day = 1;
            }
        }
    }
    (year, month, day)
}

fn format_go_date(year: i32, month: u8, day: u8) -> String {
    let year = if year < 0 {
        format!("-{:04}", year.unsigned_abs())
    } else {
        format!("{year:04}")
    };
    format!("{year}-{month:02}-{day:02}")
}

fn time_quote(value: &str) -> String {
    let mut quoted = String::with_capacity(value.len() + 2);
    quoted.push('"');
    for byte in value.bytes() {
        match byte {
            0x00..=0x1f | 0x80..=0xff => {
                use std::fmt::Write as _;
                write!(quoted, "\\x{byte:02x}").expect("String writes cannot fail");
            }
            b'"' | b'\\' => {
                quoted.push('\\');
                quoted.push(char::from(byte));
            }
            _ => quoted.push(char::from(byte)),
        }
    }
    quoted.push('"');
    quoted
}

fn parse_fixed_decimal(bytes: &[u8], start: usize, length: usize) -> Option<i32> {
    let end = start.checked_add(length)?;
    parse_decimal(bytes.get(start..end)?)
}

fn parse_decimal(bytes: &[u8]) -> Option<i32> {
    if bytes.is_empty() || !bytes.iter().all(u8::is_ascii_digit) {
        return None;
    }
    Some(
        bytes
            .iter()
            .fold(0i32, |value, digit| value * 10 + i32::from(*digit - b'0')),
    )
}

fn slugify(value: &str) -> String {
    let mut result = String::new();
    let mut separator = false;
    for character in go_lowercase(value.trim()).chars() {
        if character.is_ascii_lowercase() || character.is_ascii_digit() {
            if separator && !result.is_empty() {
                result.push('-');
            }
            result.push(character);
            separator = false;
        } else if !result.is_empty() {
            separator = true;
        }
    }
    while result.ends_with('-') {
        result.pop();
    }
    if result.is_empty() {
        "base".to_owned()
    } else {
        result
    }
}

fn sync_csv(
    rows: &[DatasetSyncRow],
    identity_field: &str,
    declared: &BTreeMap<String, PropertyConfig>,
) -> Result<(Vec<u8>, BTreeMap<String, PropertyConfig>), DatasetSyncError> {
    let mut columns: BTreeSet<String> = declared.keys().cloned().collect();
    columns.insert(identity_field.to_owned());
    for row in rows {
        columns.extend(row.values.keys().cloned());
    }
    let headers: Vec<String> = columns
        .into_iter()
        .filter(|column| !column.trim().is_empty())
        .collect();
    let mut csv = Vec::new();
    write_csv_record(&mut csv, headers.iter().map(String::as_str));
    for row in rows {
        let record = headers.iter().map(|column| {
            if column == identity_field {
                row.identity.clone()
            } else {
                sync_value_string(row.values.get(column).unwrap_or(&Value::Null))
            }
        });
        write_csv_record(&mut csv, record);
    }
    let csv_text = std::str::from_utf8(&csv).expect("producer JSON and identity strings are UTF-8");
    let parser_schema = declared
        .iter()
        .map(|(column, property)| {
            (
                column.clone(),
                dataset::PropertyConfig {
                    label: property.label.clone(),
                    kind: property.r#type.clone(),
                },
            )
        })
        .collect();
    let (parsed, inferred) = dataset::parse_csv(csv_text, &parser_schema, identity_field)?;
    if parsed.len() != rows.len() {
        return Err(DatasetSyncError::Contract(
            "dataset sync failed to materialize producer rows".to_owned(),
        ));
    }
    let schema = inferred
        .into_iter()
        .map(|(column, inferred)| {
            let mut property = declared.get(&column).cloned().unwrap_or_default();
            property.r#type = inferred.kind;
            property.label = inferred.label;
            (column, property)
        })
        .collect();
    Ok((csv, schema))
}

fn sync_value_string(value: &Value) -> String {
    match value {
        Value::Null => String::new(),
        Value::String(value) => value.clone(),
        Value::Bool(value) => value.to_string(),
        Value::Number(value) => go_fixed_number(value),
        Value::Array(_) | Value::Object(_) => go_json_string(value),
    }
}

fn go_fixed_number(value: &serde_json::Number) -> String {
    // DatasetSync's Go CLI decodes JSON objects into `interface{}`, so every
    // JSON number becomes float64 before `syncValueString` formats it. Preserve
    // that observable rounding for integers beyond float64's exact range.
    value
        .as_f64()
        .expect("serde_json numbers are finite")
        .to_string()
}

fn go_json_string(value: &Value) -> String {
    let mut output = String::new();
    write_go_json(&mut output, value);
    output
}

fn write_go_json(output: &mut String, value: &Value) {
    match value {
        Value::Null => output.push_str("null"),
        Value::Bool(value) => output.push_str(if *value { "true" } else { "false" }),
        Value::Number(value) => output.push_str(&go_json_number(value)),
        Value::String(value) => write_go_json_string(output, value),
        Value::Array(values) => {
            output.push('[');
            for (index, value) in values.iter().enumerate() {
                if index != 0 {
                    output.push(',');
                }
                write_go_json(output, value);
            }
            output.push(']');
        }
        Value::Object(values) => {
            output.push('{');
            let mut entries = values.iter().collect::<Vec<_>>();
            entries.sort_by_key(|(key, _)| *key);
            for (index, (key, value)) in entries.into_iter().enumerate() {
                if index != 0 {
                    output.push(',');
                }
                write_go_json_string(output, key);
                output.push(':');
                write_go_json(output, value);
            }
            output.push('}');
        }
    }
}

fn go_json_number(value: &serde_json::Number) -> String {
    // Values nested in producer objects are marshalled by Go's encoding/json
    // after interface{} decoding, so their integer tokens also become float64.
    let value = value.as_f64().expect("serde_json numbers are finite");
    if value == 0.0 {
        return if value.is_sign_negative() { "-0" } else { "0" }.to_owned();
    }
    let magnitude = value.abs();
    if !(1e-6..1e21).contains(&magnitude) {
        let scientific = format!("{value:e}");
        if let Some((mantissa, exponent)) = scientific.split_once('e') {
            let exponent = exponent.parse::<i32>().expect("Rust exponent is decimal");
            return format!("{mantissa}e{exponent:+}");
        }
        return scientific;
    }
    value.to_string()
}

fn write_go_json_string(output: &mut String, value: &str) {
    output.push('"');
    for character in value.chars() {
        match character {
            '"' => output.push_str("\\\""),
            '\\' => output.push_str("\\\\"),
            '\u{08}' => output.push_str("\\b"),
            '\u{0c}' => output.push_str("\\f"),
            '\n' => output.push_str("\\n"),
            '\r' => output.push_str("\\r"),
            '\t' => output.push_str("\\t"),
            '<' => output.push_str("\\u003c"),
            '>' => output.push_str("\\u003e"),
            '&' => output.push_str("\\u0026"),
            '\u{2028}' => output.push_str("\\u2028"),
            '\u{2029}' => output.push_str("\\u2029"),
            value if value <= '\u{1f}' => {
                use std::fmt::Write as _;
                write!(output, "\\u{:04x}", value as u32).expect("writing to String cannot fail");
            }
            value => output.push(value),
        }
    }
    output.push('"');
}

fn write_csv_record(output: &mut Vec<u8>, fields: impl IntoIterator<Item = impl AsRef<str>>) {
    for (index, field) in fields.into_iter().enumerate() {
        if index != 0 {
            output.push(b',');
        }
        let field = field.as_ref();
        if field == "\\."
            || field.chars().next().is_some_and(char::is_whitespace)
            || field
                .bytes()
                .any(|byte| matches!(byte, b',' | b'"' | b'\r' | b'\n'))
        {
            output.push(b'"');
            for byte in field.bytes() {
                if byte == b'"' {
                    output.push(b'"');
                }
                output.push(byte);
            }
            output.push(b'"');
        } else {
            output.extend_from_slice(field.as_bytes());
        }
    }
    output.push(b'\n');
}

fn store_raw(
    root: &Dir,
    slug: &str,
    preferred_name: &str,
    bytes: &[u8],
) -> Result<String, DatasetSyncError> {
    if slug.trim().is_empty() {
        return Err(DatasetSyncError::Contract(
            "dataset slug is required".to_owned(),
        ));
    }
    let directory_path = PathBuf::from(RAW_DIR).join(slug);
    create_dir_all_0755(root, &directory_path)?;
    let directory = root.open_dir(&directory_path)?;
    let preferred = Path::new(preferred_name);
    let stem = preferred
        .file_stem()
        .and_then(|value| value.to_str())
        .unwrap_or("source");
    let extension = preferred
        .extension()
        .and_then(|value| value.to_str())
        .unwrap_or("csv");
    let mut counter = 1usize;
    let name = loop {
        let candidate = if counter == 1 {
            format!("{stem}.{extension}")
        } else {
            format!("{stem}-{counter}.{extension}")
        };
        match directory.metadata(&candidate) {
            Ok(_) => counter += 1,
            Err(error) if error.kind() == io::ErrorKind::NotFound => break candidate,
            Err(error) => return Err(error.into()),
        }
    };
    write_atomic(&directory, &name, bytes)?;
    Ok(format!("{RAW_DIR}/{slug}/{name}"))
}

fn read_handle(root: &Dir, relative: &str) -> Result<DatasetHandle, DatasetSyncError> {
    let bytes = read_regular_file(root, relative)?;
    parse_dataset_handle(relative, &bytes)
        .map_err(|error| DatasetSyncError::Contract(error.to_string()))
}

fn read_raw_files(
    root: &Dir,
    slug: &str,
    schema: &BTreeMap<String, PropertyConfig>,
    identity_field: &str,
) -> Result<Vec<dataset::Row>, DatasetSyncError> {
    let directory_path = PathBuf::from(RAW_DIR).join(slug);
    let directory = match root.open_dir(&directory_path) {
        Ok(directory) => directory,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(error.into()),
    };
    let mut names = Vec::new();
    for entry in directory.entries()? {
        let entry = entry?;
        if entry.file_type()?.is_dir() {
            continue;
        }
        let name = entry.file_name().into_string().map_err(|_| {
            DatasetSyncError::Contract("dataset raw file name is not UTF-8".to_owned())
        })?;
        if Path::new(&name)
            .extension()
            .and_then(|value| value.to_str())
            .is_some_and(|extension| extension.eq_ignore_ascii_case("csv"))
        {
            names.push(name);
        }
    }
    names.sort();
    let declared = schema
        .iter()
        .map(|(column, property)| {
            (
                column.clone(),
                dataset::PropertyConfig {
                    label: property.label.clone(),
                    kind: property.r#type.clone(),
                },
            )
        })
        .collect();
    let mut all = Vec::new();
    for name in names {
        let bytes = read_regular_file(&directory, &name)?;
        let text = String::from_utf8(bytes).map_err(|error| {
            DatasetSyncError::Contract(format!("parse {name}: read csv: {error}"))
        })?;
        let (mut rows, _) = dataset::parse_csv(&text, &declared, identity_field)
            .map_err(|error| DatasetSyncError::Contract(format!("parse {name}: {error}")))?;
        let source_path = format!("{RAW_DIR}/{slug}/{name}");
        for row in &mut rows {
            row.source_path.clone_from(&source_path);
        }
        all.extend(rows);
    }
    Ok(all)
}

fn create_dir_all_0755(root: &Dir, path: &Path) -> io::Result<()> {
    let mut builder = cap_std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use cap_std::fs::DirBuilderExt as _;
        builder.mode(0o755);
    }
    root.create_dir_with(path, &builder)
}

fn read_regular_file(directory: &Dir, path: &str) -> io::Result<Vec<u8>> {
    let mut options = OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt as _;
        options.custom_flags(unix_nonblock_flag());
    }
    let mut file = directory.open_with(path, &options)?;
    if !file.metadata()?.is_file() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("vault path is not a regular file: {path}"),
        ));
    }
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)?;
    Ok(bytes)
}

#[cfg(any(target_os = "linux", target_os = "android"))]
const fn unix_nonblock_flag() -> i32 {
    0x800
}

#[cfg(any(
    target_os = "macos",
    target_os = "ios",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd",
    target_os = "dragonfly"
))]
const fn unix_nonblock_flag() -> i32 {
    0x4
}

#[cfg(any(target_os = "solaris", target_os = "illumos"))]
const fn unix_nonblock_flag() -> i32 {
    0x80
}

#[cfg(all(
    unix,
    not(any(
        target_os = "linux",
        target_os = "android",
        target_os = "macos",
        target_os = "ios",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
        target_os = "dragonfly",
        target_os = "solaris",
        target_os = "illumos"
    ))
))]
const fn unix_nonblock_flag() -> i32 {
    0
}

fn write_atomic(directory: &Dir, target: &str, bytes: &[u8]) -> io::Result<()> {
    let mut owned = None;
    for _ in 0..100 {
        let counter = TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
        let temporary = format!(".symdesk-dataset-{}-{counter}.tmp", std::process::id());
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use cap_std::fs::OpenOptionsExt as _;
            options.mode(0o600);
        }
        match directory.open_with(&temporary, &options) {
            Ok(file) => {
                owned = Some((file, temporary));
                break;
            }
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(error),
        }
    }
    let Some((mut file, temporary)) = owned else {
        return Err(io::Error::new(
            io::ErrorKind::AlreadyExists,
            "temporary name space exhausted",
        ));
    };
    let result = (|| {
        file.write_all(bytes)?;
        file.sync_all()?;
        drop(file);
        directory.rename(&temporary, directory, target)
    })();
    if result.is_err() {
        let _ = directory.remove_file(&temporary);
    }
    result
}
