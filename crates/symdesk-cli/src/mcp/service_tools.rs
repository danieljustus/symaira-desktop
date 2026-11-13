//! Service tools: `vault_timeline`, `desk_transform`, `vault_health`,
//! `desk_related`, `desk_dataset_{list,describe,query,sync}`, `desk_clip`,
//! `desk_export` and `desk_autofill` (Go `internal/tools`). The write tools are
//! only registered with `--allow-write`.
use std::path::Path;

use serde::Serialize;
use serde_json::{Value, json};
use symdesk_index::{
    DatasetAggregate, DatasetQueryFilter, DatasetQueryFilterGroup, DatasetServiceOptions,
    DatasetSyncOptions, DatasetSyncService, Sidecar,
};
use symdesk_vault::{HistoryStore, go_quote};

use super::{
    ServerConfig,
    args::{Kind, decode, field},
    open_sidecar,
    read::{backlink_relative_path, encoded, meetings},
    strict,
};

pub(super) fn supports(name: &str, allow_write: bool) -> bool {
    matches!(
        name,
        "vault_timeline"
            | "desk_transform"
            | "vault_health"
            | "desk_related"
            | "desk_dataset_list"
            | "desk_dataset_describe"
            | "desk_dataset_query"
    ) || (allow_write
        && matches!(
            name,
            "desk_dataset_sync" | "desk_clip" | "desk_export" | "desk_autofill"
        ))
}

pub(super) fn call(name: &str, raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    match name {
        "vault_timeline" => {
            let args = decode(
                raw,
                &[
                    field("from", "From", Kind::Str),
                    field("to", "To", Kind::Str),
                    field("limit", "Limit", Kind::Int),
                ],
            )?;
            let (root, sidecar) = open_sidecar(config)?;
            timeline(
                &root,
                &sidecar,
                &args.str("from"),
                &args.str("to"),
                args.i64("limit"),
            )
        }
        "desk_transform" => {
            let args = decode(
                raw,
                &[
                    field("text", "Text", Kind::Str),
                    field("intent", "Intent", Kind::Str),
                ],
            )?;
            let text = args.str("text");
            if text.is_empty() {
                return Err("text is required".into());
            }
            Ok(json!({"result": crate::ai_cli::transform_to_string(&text, &args.str("intent"))}))
        }
        "vault_health" => {
            let args = decode(
                raw,
                &[field(
                    "duplicate_threshold",
                    "DuplicateThreshold",
                    Kind::Int,
                )],
            )?;
            let (root, sidecar) = open_sidecar(config)?;
            // Go `service.ResolveDuplicateThreshold`: non-positive means unset.
            let threshold = match args.i64("duplicate_threshold") {
                value if value <= 0 => 85,
                value => value,
            };
            encoded(&crate::vault_health_cli::scan(&root, &sidecar, threshold)?)
        }
        "desk_related" => {
            let args = decode(raw, &[field("file", "File", Kind::Str)])?;
            let file = args.str("file");
            if file.is_empty() {
                return Err("file is required".into());
            }
            let (root, sidecar) = open_sidecar(config)?;
            encoded(&crate::related_cli::related(&root, &sidecar, &file)?)
        }
        "desk_dataset_list" => {
            dataset_object(raw)?;
            let (root, sidecar) = open_sidecar(config)?;
            let handles = crate::dataset::list_datasets(&root, &sidecar)?;
            json_text(crate::dataset::summaries_json(&handles)?)
        }
        "desk_dataset_describe" => {
            let args = strict::decode(raw, &strict::describe_shape())?;
            let slug = text(&args, "dataset");
            if slug.is_empty() {
                return Err("dataset is required".into());
            }
            let (root, sidecar) = open_sidecar(config)?;
            let (handle, rows) = describe(&root, &sidecar, &slug)?;
            json_text(crate::dataset::description_json(&handle, rows)?)
        }
        "desk_dataset_query" => dataset_query(raw, config),
        "desk_dataset_sync" => dataset_sync(raw, config),
        "desk_clip" => {
            let args = decode(raw, &[field("url", "URL", Kind::Str)])?;
            let url = args.str("url");
            if url.is_empty() {
                return Err("url is required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            let path = crate::clip_cli::note_clip(&root, &mut sidecar, &url)?;
            Ok(json!({"path": path}))
        }
        "desk_export" => {
            let args = decode(
                raw,
                &[
                    field("note", "Note", Kind::Str),
                    field("view", "View", Kind::Str),
                    field("output", "Output", Kind::Str),
                    field("format", "Format", Kind::Str),
                    field("profile", "Profile", Kind::Str),
                ],
            )?;
            let mut request = crate::export_cli::Request {
                note: args.str("note"),
                view: args.str("view"),
                output: args.str("output"),
                format: args.str("format"),
                profile: args.str("profile"),
            };
            if request.note.is_empty() && request.view.is_empty() {
                return Err("note or view is required".into());
            }
            if request.format.is_empty() {
                request.format = "pdf".into();
            }
            let (root, sidecar) = open_sidecar(config)?;
            encoded(&crate::export_cli::export(&root, &sidecar, &request)?)
        }
        "desk_autofill" => {
            let args = decode(
                raw,
                &[
                    field("view", "View", Kind::Str),
                    field("property", "Property", Kind::Str),
                    field("prompt", "Prompt", Kind::Str),
                    field("dry_run", "DryRun", Kind::Bool),
                ],
            )?;
            let (view, property) = (args.str("view"), args.str("property"));
            if view.is_empty() || property.is_empty() {
                return Err("view and property are required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            encoded(&crate::admin_ai_cli::autofill(
                &root,
                &mut sidecar,
                &view,
                &property,
                &args.str("prompt"),
                args.bool("dry_run"),
            )?)
        }
        other => Err(format!("Unknown tool: {other}")),
    }
}

fn json_text(text: String) -> Result<Value, String> {
    Ok(Value::String(crate::go_escape_json(text)))
}

fn text(value: &Value, key: &str) -> String {
    value
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

/// Go `decodeObject`: the arguments must be a JSON object (or `null`).
fn dataset_object(raw: Option<&str>) -> Result<(), String> {
    let raw = raw.ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    if raw == "null" || raw.starts_with('{') {
        return Ok(());
    }
    Err(format!(
        "json: cannot unmarshal {} into Go value of type map[string]json.RawMessage",
        super::go_json_type(raw)
    ))
}

// ---- datasets ---------------------------------------------------------------

/// Go `Service.DatasetDescribe`: the handle (migrated when legacy) and its row count.
fn describe(
    root: &Path,
    sidecar: &Sidecar,
    slug: &str,
) -> Result<(symdesk_vault::DatasetHandle, usize), String> {
    let slug = slug.trim();
    if slug.is_empty() {
        return Err("dataset slug is required".into());
    }
    let rel = crate::ingest_common::go_clean(&format!("datasets/{slug}.md"));
    let handle = crate::dataset::read_handle_with_migration(root, &rel)
        .map_err(|error| format!("dataset {} not found: {error}", go_quote(slug)))?;
    let (rows, _) = sidecar
        .dataset_query_page(&handle.slug, 0)
        .map_err(|error| error.to_string())?;
    Ok((handle, rows))
}

fn dataset_query(raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    let args = strict::decode(raw, &strict::query_shape())?;
    let slug = text(&args, "dataset");
    if slug.is_empty() {
        return Err("dataset is required".into());
    }
    let (root, sidecar) = open_sidecar(config)?;
    let (handle, _) = describe(&root, &sidecar, &slug)?;
    let part = |key: &str| args.get(key).cloned().unwrap_or(Value::Null);
    let options = DatasetServiceOptions {
        columns: serde_json::from_value(part("columns")).map_err(|error| error.to_string())?,
        filters: serde_json::from_value::<Vec<DatasetQueryFilter>>(part("filters"))
            .map_err(|error| error.to_string())?,
        filter_group: match part("filter_group") {
            Value::Null => None,
            group => Some(
                serde_json::from_value::<DatasetQueryFilterGroup>(group)
                    .map_err(|error| error.to_string())?,
            ),
        },
        sorts: Vec::new(),
        group_by: text(&args, "group_by"),
        aggregates: serde_json::from_value::<Vec<DatasetAggregate>>(part("aggregates"))
            .map_err(|error| error.to_string())?,
        limit: args
            .get("limit")
            .and_then(Value::as_i64)
            .unwrap_or_default(),
    };
    let schema = handle
        .schema
        .iter()
        .map(|(key, property)| (key.clone(), property.r#type.clone()))
        .collect();
    let result = sidecar
        .dataset_service_query(&handle.slug, &schema, &options)
        .map_err(|error| error.to_string())?;
    encoded(&result)
}

fn dataset_sync(raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    let args = strict::decode(raw, &strict::sync_shape())?;
    let slug = text(&args, "dataset");
    if slug.is_empty() {
        return Err("dataset is required".into());
    }
    let (root, mut sidecar) = open_sidecar(config)?;
    let part = |key: &str| args.get(key).cloned().unwrap_or(Value::Null);
    let options = DatasetSyncOptions {
        title: text(&args, "title"),
        slug,
        identity_field: text(&args, "identity_field"),
        schema: serde_json::from_value(part("schema")).map_err(|error| error.to_string())?,
        provenance: serde_json::from_value(part("provenance"))
            .map_err(|error| error.to_string())?,
        sensitivity: text(&args, "sensitivity"),
        retention_rule: text(&args, "retention_rule"),
        rows: serde_json::from_value(part("rows")).map_err(|error| error.to_string())?,
    };
    let result = DatasetSyncService::new(&root, &mut sidecar)
        .sync(options)
        .map_err(|error| error.to_string())?;
    encoded(&crate::dataset::SyncJson {
        slug: &result.slug,
        rows: result.rows,
        imported_rows: result.imported_rows,
        raw_path: &result.raw_path,
        handle_path: &result.handle_path,
        idempotent: result.idempotent,
    })
}

// ---- vault_timeline ---------------------------------------------------------
//
// Go `Service.Timeline`. Instants are nanoseconds since the Unix epoch in
// UTC (an `i128`), which covers Go's year 1..9999 range without calendar types.

const NANOS_PER_SECOND: i128 = 1_000_000_000;
const NANOS_PER_DAY: i128 = 86_400 * NANOS_PER_SECOND;

#[derive(Serialize)]
struct Item {
    timestamp: String,
    #[serde(rename = "type")]
    kind: &'static str,
    action: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    description: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    details: Option<DocumentDetails>,
}

#[derive(Serialize)]
struct DocumentDetails {
    document_type: String,
    person: String,
    status: String,
}

#[derive(Serialize)]
struct TimelineResult {
    from: String,
    to: String,
    total: usize,
    items: Vec<Item>,
}

/// Days since 1970-01-01 of a proleptic Gregorian date.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let year_of_era = year - era * 400;
    let day_of_year = (153 * (month + if month > 2 { -3 } else { 9 }) + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let day_of_era = z - era * 146_097;
    let year_of_era =
        (day_of_era - day_of_era / 1_460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let year = year_of_era + era * 400;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let mp = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    (if month <= 2 { year + 1 } else { year }, month, day)
}

fn days_in_month(year: i64, month: i64) -> i64 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        _ if year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) => 29,
        _ => 28,
    }
}

fn instant(year: i64, month: i64, day: i64, seconds_of_day: i64, nanos: i64) -> i128 {
    i128::from(days_from_civil(year, month, day)) * NANOS_PER_DAY
        + i128::from(seconds_of_day) * NANOS_PER_SECOND
        + i128::from(nanos)
}

/// Go `Time.Format("2006-01-02")` in UTC.
fn format_day(at: i128) -> String {
    let (year, month, day) =
        civil_from_days(i64::try_from(at.div_euclid(NANOS_PER_DAY)).unwrap_or(0));
    format!("{year:04}-{month:02}-{day:02}")
}

/// Go `Time.Format(time.RFC3339)` in UTC (fractional seconds are dropped).
fn format_rfc3339(at: i128) -> String {
    let seconds = i64::try_from(at.div_euclid(NANOS_PER_SECOND).rem_euclid(86_400)).unwrap_or(0);
    format!(
        "{}T{:02}:{:02}:{:02}Z",
        format_day(at),
        seconds / 3_600,
        seconds % 3_600 / 60,
        seconds % 60
    )
}

fn digits(bytes: &[u8]) -> Option<i64> {
    if bytes.is_empty() || !bytes.iter().all(u8::is_ascii_digit) {
        return None;
    }
    std::str::from_utf8(bytes).ok()?.parse().ok()
}

/// Go `time.Parse("2006-01-02", value)`.
fn parse_day(value: &str) -> Option<(i64, i64, i64)> {
    let bytes = value.as_bytes();
    if bytes.len() != 10 || bytes[4] != b'-' || bytes[7] != b'-' {
        return None;
    }
    let (year, month, day) = (
        digits(&bytes[0..4])?,
        digits(&bytes[5..7])?,
        digits(&bytes[8..10])?,
    );
    ((1..=12).contains(&month) && (1..=days_in_month(year, month)).contains(&day))
        .then_some((year, month, day))
}

/// Go `time.Parse("2006-01-02 15:04:05", value)`: the hour takes one or two
/// digits, and a fraction after the seconds is accepted.
fn parse_day_time(value: &str) -> Option<i128> {
    let bytes = value.as_bytes();
    if bytes.len() < 17 || bytes[4] != b'-' || bytes[7] != b'-' || bytes[10] != b' ' {
        return None;
    }
    let (year, month, day) = (
        digits(&bytes[0..4])?,
        digits(&bytes[5..7])?,
        digits(&bytes[8..10])?,
    );
    if !((1..=12).contains(&month) && (1..=days_in_month(year, month)).contains(&day)) {
        return None;
    }
    let mut at = 11;
    let hour_width = if bytes.get(at + 1).is_some_and(u8::is_ascii_digit) {
        2
    } else {
        1
    };
    let hour = digits(bytes.get(at..at + hour_width)?)?;
    at += hour_width;
    if bytes.get(at) != Some(&b':') {
        return None;
    }
    let minute = digits(bytes.get(at + 1..at + 3)?)?;
    if bytes.get(at + 3) != Some(&b':') {
        return None;
    }
    let second = digits(bytes.get(at + 4..at + 6)?)?;
    at += 6;
    if hour > 23 || minute > 59 || second > 59 {
        return None;
    }
    let mut nanos = 0_i64;
    if matches!(bytes.get(at), Some(b'.' | b','))
        && bytes.get(at + 1).is_some_and(u8::is_ascii_digit)
    {
        at += 1;
        let start = at;
        while bytes.get(at).is_some_and(u8::is_ascii_digit) {
            at += 1;
        }
        for (index, digit) in bytes[start..at].iter().take(9).enumerate() {
            nanos += i64::from(digit - b'0') * 10_i64.pow(8 - u32::try_from(index).unwrap_or(0));
        }
    }
    (at == bytes.len())
        .then(|| instant(year, month, day, hour * 3_600 + minute * 60 + second, nanos))
}

fn parse_rfc3339(value: &str) -> Option<i128> {
    symdesk_vault::retention::parse_go_rfc3339(value)
        .ok()
        .map(|at| at.unix_timestamp_nanos())
}

/// Go `parseFlexibleDate`.
fn parse_flexible_date(value: &str, is_end: bool) -> Result<i128, String> {
    let value = value.trim();
    if value.is_empty() {
        return Err("empty date string".into());
    }
    if let Some((year, month, day)) = parse_day(value) {
        return Ok(if is_end {
            instant(year, month, day, 86_399, 999_999_999)
        } else {
            instant(year, month, day, 0, 0)
        });
    }
    if let Some(at) = parse_rfc3339(value).or_else(|| parse_day_time(value)) {
        return Ok(at);
    }
    Err(format!(
        "invalid date format {}: expected YYYY-MM-DD or RFC3339",
        go_quote(value)
    ))
}

/// One decodable line of a daily journal (Go `journal.Entry`).
struct JournalEntry {
    at: i128,
    event: String,
    path: String,
    title: String,
    details: String,
}

/// Go `Entry.UnmarshalJSON`; `None` is a line the journal skips.
fn parse_journal_line(line: &str) -> Option<JournalEntry> {
    let mut entry = JournalEntry {
        // Go's zero `time.Time`: 0001-01-01T00:00:00Z.
        at: instant(1, 1, 1, 0, 0),
        event: String::new(),
        path: String::new(),
        title: String::new(),
        details: String::new(),
    };
    if line == "null" {
        return Some(entry);
    }
    if !line.starts_with('{') {
        return None;
    }
    let mut timestamp: Option<Box<serde_json::value::RawValue>> = None;
    for (key, token) in super::go_raw_fields(line).ok()?.0 {
        let matches = |name: &str| symdesk_vault::dataset::go_equal_fold(&key, name);
        if matches("ts") {
            if super::go_json_type(token.get()) == "number"
                && !token.get().parse::<f64>().is_ok_and(f64::is_finite)
            {
                return None;
            }
            timestamp = Some(token);
            continue;
        }
        let target = if matches("event") {
            &mut entry.event
        } else if matches("path") {
            &mut entry.path
        } else if matches("title") {
            &mut entry.title
        } else if matches("details") {
            &mut entry.details
        } else {
            continue;
        };
        match token.get() {
            "null" => {}
            text if text.starts_with('"') => *target = serde_json::from_str(text).ok()?,
            _ => return None,
        }
    }
    if let Some(token) = timestamp {
        let token = token.get();
        match super::go_json_type(token) {
            "string" => {
                let text: String = serde_json::from_str(token).ok()?;
                if let Some(at) = parse_rfc3339(&text) {
                    entry.at = at;
                } else if let Some((year, month, day)) = parse_day(&text) {
                    entry.at = instant(year, month, day, 0, 0);
                }
            }
            "number" => {
                #[allow(clippy::cast_possible_truncation)]
                let seconds = token.parse::<f64>().ok()? as i64;
                entry.at = i128::from(seconds) * NANOS_PER_SECOND;
            }
            _ => {}
        }
    }
    Some(entry)
}

/// Go `journal.ReadRange` for `[from, to]`; `None` stands for a read error,
/// which makes the timeline skip the journal section altogether.
fn read_journal(root: &Path, from: i128, to: i128) -> Option<Vec<JournalEntry>> {
    let directory = root.join(".symdesk").join("journal");
    let (from_day, to_day) = (from.div_euclid(NANOS_PER_DAY), to.div_euclid(NANOS_PER_DAY));
    let mut days: Vec<(i128, std::path::PathBuf)> = Vec::new();
    match std::fs::read_dir(&directory) {
        Ok(listing) => {
            for listed in listing {
                let name = listed.ok()?.file_name().to_string_lossy().into_owned();
                let Some(day) = name.strip_suffix(".ndjson").and_then(parse_day) else {
                    continue;
                };
                let canonical = format!("{:04}-{:02}-{:02}.ndjson", day.0, day.1, day.2);
                let index = i128::from(days_from_civil(day.0, day.1, day.2));
                if canonical == name && (from_day..=to_day).contains(&index) {
                    days.push((index, directory.join(&name)));
                }
            }
        }
        // A missing journal is an empty one; any other failure is Go's error.
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Some(Vec::new()),
        Err(error) if error.raw_os_error() == Some(20) => return Some(Vec::new()),
        Err(_) => return None,
    }
    days.sort();
    let mut entries = Vec::new();
    for (_, path) in days {
        let bytes = std::fs::read(&path).ok()?;
        for line in bytes.split(|byte| *byte == b'\n') {
            // `bufio.Scanner` fails on a token that fills its 64 KiB buffer.
            if line.len() > 65_535 {
                return None;
            }
            let line = String::from_utf8_lossy(line);
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            let Some(entry) = parse_journal_line(line) else {
                continue;
            };
            if entry.at >= from && entry.at <= to {
                entries.push(entry);
            }
        }
    }
    Some(entries)
}

fn timeline(
    root: &Path,
    sidecar: &Sidecar,
    from_text: &str,
    to_text: &str,
    limit: i64,
) -> Result<Value, String> {
    if from_text.trim().is_empty() {
        return Err("from date is required".into());
    }
    let mut from = parse_flexible_date(from_text, false)?;
    let mut to = from + NANOS_PER_DAY - 1;
    if !to_text.trim().is_empty() {
        to = parse_flexible_date(to_text, true)?;
    }
    if to < from {
        std::mem::swap(&mut from, &mut to);
    }
    let in_range = |at: i128| at >= from && at <= to;
    let mut items: Vec<Item> = Vec::new();

    if let Some(entries) = read_journal(root, from, to) {
        for entry in entries {
            let title = if entry.title.is_empty() {
                sidecar.get_title(&entry.path).unwrap_or_default()
            } else {
                entry.title.clone()
            };
            let description = if entry.details.is_empty() {
                format!("{}: {}", entry.event, entry.path)
            } else {
                entry.details.clone()
            };
            items.push(Item {
                timestamp: format_rfc3339(entry.at),
                kind: "journal",
                action: entry.event,
                path: entry.path,
                title,
                description,
                details: None,
            });
        }
    }

    let (from_day, to_day) = (format_day(from), format_day(to));
    if let Ok(documents) = sidecar.timeline_docs(&from_day, &to_day) {
        for document in documents {
            let relative = backlink_relative_path(root, Path::new(&document.path));
            if !document.document_date.is_empty()
                && document.document_date >= from_day
                && document.document_date <= to_day
            {
                items.push(Item {
                    timestamp: format!("{}T00:00:00Z", document.document_date),
                    kind: "document",
                    action: "document_date".into(),
                    path: relative,
                    title: document.title.clone(),
                    description: format!("Document dated {}", document.document_date),
                    details: Some(DocumentDetails {
                        document_type: document.file_type.clone(),
                        person: document.person.clone(),
                        status: document.status.clone(),
                    }),
                });
            }
        }
    }

    if let Ok((meetings, _)) = meetings::summaries(root) {
        for meeting in meetings {
            if meeting.started_at.is_empty() {
                continue;
            }
            let Some(at) = parse_rfc3339(&meeting.started_at).or_else(|| {
                parse_day(&meeting.started_at)
                    .map(|(year, month, day)| instant(year, month, day, 0, 0))
            }) else {
                continue;
            };
            if !in_range(at) {
                continue;
            }
            let description = if meeting.duration_ms > 0 {
                format!(
                    "Meeting ({}, {} mins)",
                    meeting.review_state,
                    meeting.duration_ms / 60_000
                )
            } else {
                format!("Meeting ({})", meeting.review_state)
            };
            items.push(Item {
                timestamp: meeting.started_at.clone(),
                kind: "meeting",
                action: "meeting".into(),
                path: meeting.path.clone(),
                title: meeting.title.clone(),
                description,
                details: None,
            });
        }
    }

    if let Ok(history) = symdesk_vault::retention::load_history(root) {
        for entry in history {
            let at = entry.timestamp.unix_timestamp_nanos();
            if in_range(at) {
                items.push(Item {
                    timestamp: format_rfc3339(at),
                    kind: "retention",
                    description: format!("Retention action {} ({})", entry.action, entry.rule_name),
                    action: entry.action,
                    path: entry.path,
                    title: entry.title,
                    details: None,
                });
            }
        }
    }

    if let Ok(trash) = HistoryStore::new(root).trash_list() {
        for entry in trash {
            let at = entry.deleted_at.unix_timestamp_nanos();
            if in_range(at) {
                items.push(Item {
                    timestamp: format_rfc3339(at),
                    kind: "trash",
                    action: "file_removed".into(),
                    path: entry.original_path,
                    title: entry.name,
                    description: "Soft-deleted to trash".into(),
                    details: None,
                });
            }
        }
    }

    items.sort_by(|a, b| a.timestamp.cmp(&b.timestamp));
    let mut seen = std::collections::HashSet::new();
    items.retain(|item| {
        seen.insert(format!(
            "{}|{}|{}|{}",
            item.timestamp, item.kind, item.action, item.path
        ))
    });
    if let Ok(limit) = usize::try_from(limit)
        && limit > 0
        && items.len() > limit
    {
        items.truncate(limit);
    }
    encoded(&TimelineResult {
        from: from_day,
        to: to_day,
        total: items.len(),
        items,
    })
}
