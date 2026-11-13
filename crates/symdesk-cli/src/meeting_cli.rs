//! `symdesk meeting list|show|review|publish` and `meeting participant
//! candidates|confirm|create|unlink-contact` (Go `cmd/symdesk/meetings.go`,
//! `internal/service/meeting*.go`). The note frontmatter is decoded into the
//! typed shape Go uses (`meetingFrontmatter`) and re-encoded in declaration
//! order, so unknown fields survive an edit exactly as they do in Go.
use std::{collections::BTreeMap, path::Path, process::ExitCode};

use clap::{Arg, ArgAction, ArgMatches, Command};
use noyalib::{Mapping, Number, Value};
use serde::Serialize;
use symdesk_core::{compose::Resolver, compose_probe::ToolCache};
use symdesk_service::memory;
use symdesk_vault::{
    Document, go_quote, render_go_yaml_map, render_go_yaml_meeting_map, secure_path,
    set_frontmatter_value,
};

use crate::{
    emit_error,
    ingest_cli::init_service_deps,
    mcp::read::meetings,
    mutate_cli::{reindex, snapshot},
    service_cli::go_string_map,
    write_go_json, write_stdout,
};

const SYMMEMORY_UNAVAILABLE: &str = "symmemory not found on PATH";
const PUBLISHED_FACTS_KEY: &str = "symmeet_published_facts";

fn positional() -> Arg {
    Arg::new("args").num_args(0..)
}

pub fn cli() -> Command {
    Command::new("meeting")
        .subcommand(
            Command::new("list")
                .arg(
                    Arg::new("include-errors")
                        .long("include-errors")
                        .action(ArgAction::SetTrue),
                )
                .arg(positional()),
        )
        .subcommand(Command::new("show").arg(positional()))
        .subcommand(Command::new("review").arg(positional()))
        .subcommand(
            Command::new("publish")
                .arg(
                    Arg::new("fact")
                        .long("fact")
                        .num_args(1)
                        .allow_hyphen_values(true)
                        .action(ArgAction::Append),
                )
                .arg(positional()),
        )
        .subcommand(
            Command::new("participant")
                .subcommand(Command::new("candidates").arg(positional()))
                .subcommand(Command::new("confirm").arg(positional()))
                .subcommand(Command::new("create").arg(positional()))
                .subcommand(Command::new("contact").arg(positional()))
                .subcommand(Command::new("link-contact").arg(positional()))
                .subcommand(Command::new("unlink-contact").arg(positional())),
        )
}

fn args_of(command: &ArgMatches) -> Vec<String> {
    command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .cloned()
        .collect()
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let result = match command.subcommand() {
        Some(("list", sub)) => with_vault(vault, |root, _| list(root, sub, json)),
        Some(("show", sub)) => with_vault(vault, |root, _| show(root, &args_of(sub), json)),
        Some(("review", sub)) => {
            with_vault(vault, |root, db| review(root, db, &args_of(sub), json))
        }
        Some(("publish", sub)) => with_vault(vault, |root, _| publish(root, sub, json)),
        Some(("participant", sub)) => match sub.subcommand() {
            Some(("candidates", leaf)) => {
                with_vault(vault, |_, _| candidates(&args_of(leaf), json))
            }
            Some(("confirm", leaf)) => {
                with_vault(vault, |root, db| confirm(root, db, &args_of(leaf), json))
            }
            Some(("create", leaf)) => {
                with_vault(vault, |root, db| create(root, db, &args_of(leaf), json))
            }
            Some(("unlink-contact", leaf)) => with_vault(vault, |root, db| {
                unlink_contact(root, db, &args_of(leaf), json)
            }),
            Some(("contact", leaf)) => with_vault(vault, |_, _| contact(&args_of(leaf), json)),
            Some(("link-contact", leaf)) => with_vault(vault, |root, db| {
                link_contact(root, db, &args_of(leaf), json)
            }),
            Some(_) | None => Ok(crate::process_exit(symaira_core_exit::ExitCode::Ok)),
        },
        _ => Ok(crate::process_exit(symaira_core_exit::ExitCode::Ok)),
    };
    result.unwrap_or_else(|error| emit_error(error, json))
}

/// Go `initServiceDeps` + `defer db.Close()` around one command body.
fn with_vault(
    vault: Option<&str>,
    body: impl FnOnce(&Path, &mut symdesk_index::Sidecar) -> Result<ExitCode, String>,
) -> Result<ExitCode, String> {
    let (root, mut sidecar) = init_service_deps(vault)?;
    let result = body(&root, &mut sidecar);
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    result
}

fn emit<T: Serialize>(value: &T, text: String, json: bool) -> ExitCode {
    if json {
        write_go_json(value)
    } else {
        write_stdout(format!("{text}\n"))
    }
}

fn status_map(entries: &[(&str, &str)], json: bool) -> ExitCode {
    let map: BTreeMap<String, String> = entries
        .iter()
        .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
        .collect();
    emit(&map, go_string_map(map.iter()), json)
}

// ---------------------------------------------------------------- list/show

fn summary_text(summary: &meetings::Summary) -> String {
    format!(
        "{{Path:{} Title:{} MeetingID:{} StartedAt:{} DurationMS:{} Language:{} ReviewState:{}}}",
        summary.path,
        summary.title,
        summary.meeting_id,
        summary.started_at,
        summary.duration_ms,
        summary.language,
        summary.review_state
    )
}

fn list(root: &Path, command: &ArgMatches, json: bool) -> Result<ExitCode, String> {
    let (summaries, failures) = meetings::summaries(root)?;
    let rows = format!(
        "[{}]",
        summaries
            .iter()
            .map(summary_text)
            .collect::<Vec<_>>()
            .join(" ")
    );
    if command.get_flag("include-errors") {
        #[derive(Serialize)]
        struct Detailed<'a> {
            meetings: &'a [meetings::Summary],
            failures: &'a [meetings::Failure],
        }
        let text = format!(
            "&{{Meetings:{rows} Failures:[{}]}}",
            failures
                .iter()
                .map(|failure| format!("{{Path:{} Message:{}}}", failure.path, failure.message))
                .collect::<Vec<_>>()
                .join(" ")
        );
        return Ok(emit(
            &Detailed {
                meetings: &summaries,
                failures: &failures,
            },
            text,
            json,
        ));
    }
    Ok(emit(&summaries, rows, json))
}

fn show(root: &Path, args: &[String], json: bool) -> Result<ExitCode, String> {
    let requested = args.first().map_or("", String::as_str);
    if json {
        let encoded = meetings::show(root, requested)?;
        let text = encoded.as_str().unwrap_or_default();
        return Ok(write_stdout(format!("{text}\n")));
    }
    let doc = meetings::show_document(root, requested)?;
    let modified = std::fs::metadata(&doc.path)
        .and_then(|meta| meta.modified())
        .map_err(|error| error.to_string())?;
    Ok(write_stdout(format!(
        "{}\n",
        document_text(&doc, time::OffsetDateTime::from(modified))
    )))
}

fn join_list(items: &[String]) -> String {
    format!("[{}]", items.join(" "))
}

/// Go `%+v` of a `*vault.Document`.
fn document_text(doc: &Document, modified: time::OffsetDateTime) -> String {
    let mut frontmatter: Vec<(&String, String)> = doc
        .frontmatter
        .iter()
        .map(|(key, value)| {
            let text = match doc.yaml_timestamps.get(key) {
                Some(stamp) => go_time_text(stamp),
                None => value_text(value),
            };
            (key, text)
        })
        .collect();
    frontmatter.sort_by(|left, right| left.0.as_bytes().cmp(right.0.as_bytes()));
    let frontmatter = frontmatter
        .iter()
        .map(|(key, value)| format!("{key}:{value}"))
        .collect::<Vec<_>>()
        .join(" ");
    let asn = doc
        .asn
        .map_or_else(|| "<nil>".to_owned(), |value| format!("{value}"));
    format!(
        "&{{Path:{} SHA256:{} Title:{} Created:{} Tags:{} Aliases:{} Frontmatter:map[{frontmatter}] Body:{} Links:{} Size:{} ModTime:{} DocumentDate:{} Person:{} Status:{} DueDate:{} Confidence:{} OcrJSONPath:{} Simhash:{} ASN:{asn} Type:{} DerivedFrom:{} Derived:{}}}",
        doc.path,
        doc.sha256,
        doc.title,
        doc.created,
        join_list(&doc.tags),
        join_list(&doc.aliases),
        doc.body,
        join_list(&doc.links),
        doc.size,
        go_mtime_text(modified),
        doc.document_date,
        doc.person,
        doc.status,
        doc.due_date,
        doc.confidence,
        doc.ocr_json_path,
        doc.simhash,
        doc.document_type,
        doc.derived_from,
        doc.derived,
    )
}

/// Go `%v` of a decoded YAML value.
fn value_text(value: &Value) -> String {
    match value {
        Value::Null => "<nil>".to_owned(),
        Value::Bool(flag) => flag.to_string(),
        Value::String(text) => text.clone(),
        Value::Number(number) => match number {
            Number::Integer(value) => value.to_string(),
            Number::Unsigned(value) => value.to_string(),
            Number::Float(value) => go_float_text(*value),
            _ => String::new(),
        },
        Value::Sequence(items) => format!(
            "[{}]",
            items.iter().map(value_text).collect::<Vec<_>>().join(" ")
        ),
        Value::Mapping(mapping) => {
            let mut entries: Vec<(&String, &Value)> = mapping.iter().collect();
            entries.sort_by(|left, right| left.0.as_bytes().cmp(right.0.as_bytes()));
            format!(
                "map[{}]",
                entries
                    .iter()
                    .map(|(key, value)| format!("{key}:{}", value_text(value)))
                    .collect::<Vec<_>>()
                    .join(" ")
            )
        }
        Value::Tagged(_) => String::new(),
    }
}

/// Go `%v` of a `float64` (`%g`, shortest representation).
fn go_float_text(value: f64) -> String {
    if value.is_nan() {
        return "NaN".to_owned();
    }
    if value.is_infinite() {
        return if value > 0.0 { "+Inf" } else { "-Inf" }.to_owned();
    }
    if value == 0.0 {
        return "0".to_owned();
    }
    let scientific = format!("{value:e}");
    let (mantissa, exponent) = scientific.split_once('e').unwrap_or((&scientific, "0"));
    let exponent: i32 = exponent.parse().unwrap_or(0);
    if !(-4..21).contains(&exponent) {
        let sign = if exponent < 0 { '-' } else { '+' };
        return format!("{mantissa}e{sign}{:02}", exponent.abs());
    }
    format!("{value}")
}

/// Go `time.Time` `%v` for an RFC3339 instant.
fn go_time_text(stamp: &str) -> String {
    time::OffsetDateTime::parse(stamp, &time::format_description::well_known::Rfc3339).map_or_else(
        |_| stamp.to_owned(),
        |parsed| format_go_instant(parsed, parsed.offset()),
    )
}

/// Go `%v` of the modification time, in the process-local zone.
fn go_mtime_text(instant: time::OffsetDateTime) -> String {
    let offset = if std::env::var("TZ").ok().as_deref() == Some("UTC") {
        time::UtcOffset::UTC
    } else {
        time::UtcOffset::local_offset_at(instant).unwrap_or(time::UtcOffset::UTC)
    };
    format_go_instant(instant, offset)
}

fn format_go_instant(instant: time::OffsetDateTime, offset: time::UtcOffset) -> String {
    let local = instant.to_offset(offset);
    let mut text = format!(
        "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
        local.year(),
        u8::from(local.month()),
        local.day(),
        local.hour(),
        local.minute(),
        local.second()
    );
    let nanos = local.nanosecond();
    if nanos != 0 {
        let fraction = format!("{nanos:09}");
        text.push('.');
        text.push_str(fraction.trim_end_matches('0'));
    }
    let (hours, minutes, _) = offset.as_hms();
    let sign = if hours < 0 || minutes < 0 { '-' } else { '+' };
    let numeric = format!("{sign}{:02}{:02}", hours.abs(), minutes.abs());
    if offset.is_utc() {
        format!("{text} {numeric} UTC")
    } else {
        format!("{text} {numeric} {numeric}")
    }
}

// ----------------------------------------------------------- typed frontmatter

#[derive(Default)]
struct ContactRef {
    provider: String,
    schema_version: i64,
    id: String,
    kind: String,
    display_name: String,
    extras: Vec<(String, Value)>,
}

#[derive(Default)]
struct Participant {
    label: String,
    speaker_ids: Vec<String>,
    entity_id: String,
    contact_ref: Option<ContactRef>,
    extras: Vec<(String, Value)>,
}

#[derive(Default)]
struct MeetingFrontmatter {
    kind: String,
    title: String,
    created: String,
    tags: Vec<String>,
    meeting_id: String,
    started_at: String,
    ended_at: String,
    duration_ms: i64,
    language: String,
    participants: Vec<Participant>,
    artifact_schema_version: i64,
    review_state: String,
    extras: Vec<(String, Value)>,
    /// Extras that were YAML timestamps: Go re-encodes `time.Time` plain.
    stamp_extras: Vec<(String, String)>,
}

/// yaml.v3's short tag for a decoded scalar.
fn short_tag(value: &Value, timestamp: bool) -> &'static str {
    if timestamp {
        return "!!timestamp";
    }
    match value {
        Value::Null => "!!null",
        Value::Bool(_) => "!!bool",
        Value::String(_) => "!!str",
        Value::Number(Number::Float(_)) => "!!float",
        Value::Number(_) => "!!int",
        Value::Sequence(_) => "!!seq",
        _ => "!!map",
    }
}

fn scalar_source_text(value: &Value, stamp: Option<&String>) -> String {
    if let Some(stamp) = stamp {
        return stamp.clone();
    }
    match value {
        Value::Null => "null".to_owned(),
        Value::Bool(flag) => flag.to_string(),
        Value::String(text) => text.clone(),
        Value::Number(Number::Integer(number)) => number.to_string(),
        Value::Number(Number::Unsigned(number)) => number.to_string(),
        Value::Number(Number::Float(number)) => go_float_text(*number),
        _ => String::new(),
    }
}

/// Go's `terror` text for a scalar or collection that cannot be decoded.
fn mismatch(line: usize, value: &Value, stamp: Option<&String>, target: &str) -> String {
    let tag = short_tag(value, stamp.is_some());
    let shown = if matches!(value, Value::Sequence(_) | Value::Mapping(_)) {
        String::new()
    } else {
        let text = scalar_source_text(value, stamp);
        if text.len() > 10 {
            let mut cut = 7;
            while !text.is_char_boundary(cut) {
                cut -= 1;
            }
            format!(" `{}...`", &text[..cut])
        } else {
            format!(" `{text}`")
        }
    };
    format!("line {line}: cannot unmarshal {tag}{shown} into {target}")
}

/// A scalar rendered the way a `string` field receives it, `None` when the
/// decoder refuses it.
fn string_field(value: &Value, stamp: Option<&String>) -> Option<String> {
    if let Some(stamp) = stamp {
        return Some(normalized_stamp(stamp));
    }
    match value {
        Value::Null => Some(String::new()),
        Value::Bool(flag) => Some(flag.to_string()),
        Value::String(text) => Some(text.clone()),
        Value::Number(Number::Integer(number)) => Some(number.to_string()),
        Value::Number(Number::Unsigned(number)) => Some(number.to_string()),
        Value::Number(Number::Float(number)) => Some(go_float_text(*number)),
        _ => None,
    }
}

fn normalized_stamp(stamp: &str) -> String {
    time::OffsetDateTime::parse(stamp, &time::format_description::well_known::Rfc3339)
        .ok()
        .and_then(|parsed| {
            parsed
                .format(&time::format_description::well_known::Rfc3339)
                .ok()
        })
        .unwrap_or_else(|| stamp.to_owned())
}

fn int_field(value: &Value, stamp: Option<&String>) -> Option<i64> {
    if stamp.is_some() {
        return None;
    }
    match value {
        Value::Null => Some(0),
        Value::Number(Number::Integer(number)) => Some(*number),
        Value::Number(Number::Unsigned(number)) => i64::try_from(*number).ok(),
        #[allow(clippy::cast_possible_truncation)]
        Value::Number(Number::Float(number)) => Some(*number as i64),
        _ => None,
    }
}

fn strings_field(value: &Value) -> Option<Vec<String>> {
    match value {
        Value::Null => Some(Vec::new()),
        Value::Sequence(items) => items
            .iter()
            .map(|item| string_field(item, None))
            .collect::<Option<Vec<_>>>(),
        _ => None,
    }
}

fn extras_of(mapping: &Mapping, known: &[&str]) -> Vec<(String, Value)> {
    mapping
        .iter()
        .filter(|(key, _)| !known.contains(&key.as_str()))
        .map(|(key, value)| (key.clone(), value.clone()))
        .collect()
}

fn decode_contact_ref(value: &Value) -> Option<ContactRef> {
    let Value::Mapping(mapping) = value else {
        return None;
    };
    let text = |key: &str| {
        mapping
            .get(key)
            .and_then(|v| string_field(v, None))
            .unwrap_or_default()
    };
    Some(ContactRef {
        provider: text("provider"),
        schema_version: mapping
            .get("schema_version")
            .and_then(|v| int_field(v, None))
            .unwrap_or_default(),
        id: text("id"),
        kind: text("kind"),
        display_name: text("display_name"),
        extras: extras_of(
            mapping,
            &["provider", "schema_version", "id", "kind", "display_name"],
        ),
    })
}

fn decode_participants(value: &Value) -> Option<Vec<Participant>> {
    let Value::Sequence(items) = value else {
        return if matches!(value, Value::Null) {
            Some(Vec::new())
        } else {
            None
        };
    };
    let mut out = Vec::new();
    for item in items {
        let Value::Mapping(mapping) = item else {
            return None;
        };
        let text = |key: &str| {
            mapping
                .get(key)
                .map_or(Some(String::new()), |v| string_field(v, None))
        };
        out.push(Participant {
            label: text("label")?,
            speaker_ids: match mapping.get("speaker_ids") {
                Some(ids) => strings_field(ids)?,
                None => Vec::new(),
            },
            entity_id: text("entity_id")?,
            contact_ref: match mapping.get("contact_ref") {
                None | Some(Value::Null) => None,
                Some(other) => Some(decode_contact_ref(other)?),
            },
            extras: extras_of(
                mapping,
                &["label", "speaker_ids", "entity_id", "contact_ref"],
            ),
        });
    }
    Some(out)
}

/// Line of a top-level key in yaml.v3's sorted, block-style re-encoding.
fn key_lines(doc: &Document) -> BTreeMap<String, usize> {
    let mut rendered: Vec<(String, usize)> = Vec::new();
    for (key, value) in &doc.frontmatter {
        let value = match doc.yaml_timestamps.get(key) {
            Some(stamp) => Value::String(stamp.clone()),
            None => value.clone(),
        };
        let one = BTreeMap::from([(key.clone(), value)]);
        let lines = render_go_yaml_map(&one)
            .map(|text| text.lines().count())
            .unwrap_or(1);
        rendered.push((key.clone(), lines));
    }
    // The map renderer owns the natural key order; recover it from one pass.
    let all: BTreeMap<String, Value> = doc
        .frontmatter
        .iter()
        .map(|(key, value)| {
            let value = match doc.yaml_timestamps.get(key) {
                Some(stamp) => Value::String(stamp.clone()),
                None => value.clone(),
            };
            (key.clone(), value)
        })
        .collect();
    let order: Vec<String> = render_go_yaml_map(&all)
        .map(|text| {
            text.lines()
                .filter(|line| !line.starts_with([' ', '\t', '-']))
                .filter_map(|line| line.split_once(':').map(|(key, _)| key.trim().to_owned()))
                .collect()
        })
        .unwrap_or_default();
    let counts: BTreeMap<String, usize> = rendered.into_iter().collect();
    let mut line = 1;
    let mut result = BTreeMap::new();
    for key in order {
        let key = key.trim_matches('"').trim_matches('\'').to_owned();
        let Some(count) = counts.get(&key) else {
            continue;
        };
        result.insert(key, line);
        line += count;
    }
    result
}

/// Go `loadMeetingFrontmatter`.
fn load(root: &Path, note: &str) -> Result<(Document, MeetingFrontmatter), String> {
    let doc = meetings::show_document(root, note)?;
    let lines = key_lines(&doc);
    let mut errors: Vec<(usize, String)> = Vec::new();
    let mut fm = MeetingFrontmatter::default();
    let stamp = |key: &str| doc.yaml_timestamps.get(key);
    let line_of = |key: &str, value: &Value| {
        let base = lines.get(key).copied().unwrap_or(1);
        if matches!(value, Value::Sequence(_) | Value::Mapping(_)) {
            base + 1
        } else {
            base
        }
    };
    for (key, value) in &doc.frontmatter {
        let ts = stamp(key);
        let mut record = |line: usize, target: &str| {
            errors.push((line, mismatch(line, value, ts, target)));
        };
        let string_target = |slot: &mut String, record: &mut dyn FnMut(usize, &str)| {
            match string_field(value, ts) {
                Some(text) => *slot = text,
                None => record(line_of(key, value), "string"),
            }
        };
        match key.as_str() {
            "type" => string_target(&mut fm.kind, &mut record),
            "title" => string_target(&mut fm.title, &mut record),
            "created" => string_target(&mut fm.created, &mut record),
            "meeting_id" => string_target(&mut fm.meeting_id, &mut record),
            "started_at" => string_target(&mut fm.started_at, &mut record),
            "ended_at" => string_target(&mut fm.ended_at, &mut record),
            "language" => string_target(&mut fm.language, &mut record),
            "tags" => match strings_field(value) {
                Some(tags) if ts.is_none() => fm.tags = tags,
                _ => record(line_of(key, value), "[]string"),
            },
            "duration_ms" => match int_field(value, ts) {
                Some(number) => fm.duration_ms = number,
                None => record(line_of(key, value), "int64"),
            },
            "participants" => match decode_participants(value) {
                Some(participants) if ts.is_none() => fm.participants = participants,
                _ => record(line_of(key, value), "[]service.MeetingParticipant"),
            },
            "symmeet_source" => {
                if let Value::Mapping(source) = value {
                    if let Some(number) = source.get("artifact_schema_version") {
                        match int_field(number, None) {
                            Some(number) => fm.artifact_schema_version = number,
                            None => {
                                let line = line_of(key, value)
                                    + source
                                        .iter()
                                        .position(|(name, _)| name == "artifact_schema_version")
                                        .unwrap_or(0);
                                errors.push((line, mismatch(line, number, None, "int")));
                            }
                        }
                    }
                    if let Some(state) = source.get("review_state") {
                        fm.review_state = string_field(state, None).unwrap_or_default();
                    }
                } else if !matches!(value, Value::Null) {
                    record(line_of(key, value), "service.MeetingSourceInfo");
                }
            }
            _ => {
                let value = match ts {
                    Some(stamp) => {
                        let stamp = normalized_stamp(stamp);
                        fm.stamp_extras.push((key.clone(), stamp.clone()));
                        Value::String(stamp)
                    }
                    None => value.clone(),
                };
                fm.extras.push((key.clone(), value));
            }
        }
    }
    if !errors.is_empty() {
        errors.sort_by_key(|(line, _)| *line);
        let joined = errors
            .into_iter()
            .map(|(_, text)| text)
            .collect::<Vec<_>>()
            .join("\n  ");
        return Err(format!(
            "failed to decode meeting frontmatter: yaml: unmarshal errors:\n  {joined}"
        ));
    }
    Ok((doc, fm))
}

fn strings_value(items: &[String]) -> Value {
    Value::Sequence(items.iter().cloned().map(Value::String).collect())
}

fn mapping_of(fields: Vec<(&str, Value)>, extras: &[(String, Value)]) -> Mapping {
    let mut mapping = Mapping::new();
    for (key, value) in fields {
        mapping.insert(key.to_owned(), value);
    }
    for (key, value) in extras {
        mapping.insert(key.clone(), value.clone());
    }
    mapping
}

fn int_value(number: i64) -> Value {
    Value::Number(Number::Integer(number))
}

/// Go `yaml.Marshal(meetingFrontmatter)`.
fn encode(fm: &MeetingFrontmatter) -> Result<String, String> {
    let mut entries: BTreeMap<String, Value> = BTreeMap::new();
    entries.insert("type".into(), Value::String(fm.kind.clone()));
    entries.insert("title".into(), Value::String(fm.title.clone()));
    entries.insert("created".into(), Value::String(fm.created.clone()));
    entries.insert("tags".into(), strings_value(&fm.tags));
    entries.insert("meeting_id".into(), Value::String(fm.meeting_id.clone()));
    entries.insert("started_at".into(), Value::String(fm.started_at.clone()));
    if !fm.ended_at.is_empty() {
        entries.insert("ended_at".into(), Value::String(fm.ended_at.clone()));
    }
    if fm.duration_ms != 0 {
        entries.insert("duration_ms".into(), int_value(fm.duration_ms));
    }
    if !fm.language.is_empty() {
        entries.insert("language".into(), Value::String(fm.language.clone()));
    }
    if !fm.participants.is_empty() {
        let participants = fm
            .participants
            .iter()
            .map(|participant| {
                let mut fields = vec![
                    ("label", Value::String(participant.label.clone())),
                    ("speaker_ids", strings_value(&participant.speaker_ids)),
                ];
                if !participant.entity_id.is_empty() {
                    fields.push(("entity_id", Value::String(participant.entity_id.clone())));
                }
                if let Some(reference) = &participant.contact_ref {
                    let mut ref_fields = vec![
                        ("provider", Value::String(reference.provider.clone())),
                        ("schema_version", int_value(reference.schema_version)),
                        ("id", Value::String(reference.id.clone())),
                        ("kind", Value::String(reference.kind.clone())),
                    ];
                    if !reference.display_name.is_empty() {
                        ref_fields.push((
                            "display_name",
                            Value::String(reference.display_name.clone()),
                        ));
                    }
                    fields.push((
                        "contact_ref",
                        Value::Mapping(mapping_of(ref_fields, &reference.extras)),
                    ));
                }
                Value::Mapping(mapping_of(fields, &participant.extras))
            })
            .collect();
        entries.insert("participants".into(), Value::Sequence(participants));
    }
    entries.insert(
        "symmeet_source".into(),
        Value::Mapping(mapping_of(
            vec![
                (
                    "artifact_schema_version",
                    int_value(fm.artifact_schema_version),
                ),
                ("review_state", Value::String(fm.review_state.clone())),
            ],
            &[],
        )),
    );
    for (key, value) in &fm.extras {
        entries.insert(key.clone(), value.clone());
    }
    let mut rendered = render_go_yaml_meeting_map(&entries)
        .map_err(|error| format!("failed to encode meeting frontmatter: {error}"))?;
    for (key, stamp) in &fm.stamp_extras {
        let quoted = format!("\n{key}: \"{stamp}\"\n");
        if rendered.contains(&quoted) {
            rendered = rendered.replacen(&quoted, &format!("\n{key}: {stamp}\n"), 1);
        }
    }
    Ok(rendered)
}

/// Go `writeMeetingFrontmatter`: replaces the frontmatter block, keeps the
/// body byte for byte, refuses when the file changed since it was read.
fn write(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    note: &str,
    doc: &Document,
    fm: &MeetingFrontmatter,
) -> Result<(), String> {
    let path = secure_path(root, note).map_err(|error| error.to_string())?;
    let encoded = encode(fm)?;
    let raw = std::fs::read(&path).map_err(|error| format!("failed to re-read {note}: {error}"))?;
    if symdesk_vault::sha256_hex(&raw) != doc.sha256 {
        return Err(format!("{note} changed on disk since it was read; re-run"));
    }
    let content = format!("---\n{encoded}---\n{}", doc.body);
    snapshot(root, &path);
    std::fs::write(&path, content.as_bytes())
        .map_err(|error| format!("failed to write {note}: {error}"))?;
    reindex(db, &path).map_err(|error| format!("failed to re-index {note}: {error}"))?;
    Ok(())
}

fn participant_index(fm: &MeetingFrontmatter, speaker: &str) -> Option<usize> {
    fm.participants
        .iter()
        .position(|participant| participant.speaker_ids.iter().any(|id| id == speaker))
}

fn arg(args: &[String], index: usize) -> &str {
    args.get(index).map_or("", String::as_str)
}

// ------------------------------------------------------------- local writes

fn review(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    args: &[String],
    json: bool,
) -> Result<ExitCode, String> {
    let note = arg(args, 0);
    let (doc, mut fm) = load(root, note)?;
    "reviewed".clone_into(&mut fm.review_state);
    write(root, db, note, &doc, &fm)?;
    Ok(status_map(
        &[("path", note), ("review_state", "reviewed")],
        json,
    ))
}

fn confirm_participant(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    note: &str,
    speaker: &str,
    entity_id: &str,
) -> Result<(), String> {
    let (doc, mut fm) = load(root, note)?;
    let index = participant_index(&fm, speaker).ok_or_else(|| {
        format!(
            "no participant with speaker id {} in {note}",
            go_quote(speaker)
        )
    })?;
    fm.participants[index].entity_id = entity_id.to_owned();
    write(root, db, note, &doc, &fm)
}

fn confirm(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    args: &[String],
    json: bool,
) -> Result<ExitCode, String> {
    let (note, speaker, entity_id) = (arg(args, 0), arg(args, 1), arg(args, 2));
    confirm_participant(root, db, note, speaker, entity_id)?;
    let status = if entity_id.is_empty() {
        "unlinked"
    } else {
        "confirmed"
    };
    Ok(status_map(
        &[
            ("speaker_id", speaker),
            ("entity_id", entity_id),
            ("status", status),
        ],
        json,
    ))
}

fn unlink_contact(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    args: &[String],
    json: bool,
) -> Result<ExitCode, String> {
    let (note, speaker) = (arg(args, 0), arg(args, 1));
    let (doc, mut fm) = load(root, note)?;
    let index = participant_index(&fm, speaker).ok_or_else(|| {
        format!(
            "no participant with speaker id {} in {note}",
            go_quote(speaker)
        )
    })?;
    fm.participants[index].contact_ref = None;
    write(root, db, note, &doc, &fm)?;
    Ok(status_map(
        &[("speaker_id", speaker), ("status", "unlinked")],
        json,
    ))
}

// ---------------------------------------------------------- contact store

const CONTACT_STORE_UNAVAILABLE: &str = "symdesk contact store unavailable";
const CONTACT_NOT_FOUND: &str = "symrelate contact not found";

/// Go `app.Open`: resolve the store paths, create the profile directories
/// (0700) and open (and migrate) the SQLite store.
fn open_contact_store() -> Result<rusqlite::Connection, String> {
    let environment = symdesk_core::config::environment_snapshot()?;
    let paths = symdesk_core::config::contacts_paths(&environment);
    for directory in [&paths.config_dir, &paths.data_dir, &paths.cache_dir] {
        if directory.is_empty() {
            continue;
        }
        let mut builder = std::fs::DirBuilder::new();
        builder.recursive(true);
        std::os::unix::fs::DirBuilderExt::mode(&mut builder, 0o700);
        builder
            .create(directory)
            .map_err(|error| error.to_string())?;
    }
    symdesk_contacts::open(&paths.db_path).map_err(|error| error.to_string())
}

/// Go `contacts.Available`.
fn contact_store_available() -> bool {
    open_contact_store()
        .and_then(|db| {
            db.query_row("SELECT 1", [], |row| row.get::<_, i64>(0))
                .map_err(|error| error.to_string())
        })
        .is_ok()
}

/// Go `contacts.ResolveRef`: persons first, then organizations.
fn resolve_contact_ref(id: &str) -> Result<ContactRef, String> {
    if id.is_empty() {
        return Err("a symrelate contact id is required".to_owned());
    }
    let db = open_contact_store()?;
    for (query, kind) in [
        ("SELECT display_name FROM persons WHERE id = ?", "person"),
        (
            "SELECT name FROM organizations WHERE id = ?",
            "organization",
        ),
    ] {
        let mut statement = db.prepare(query).map_err(|error| error.to_string())?;
        let names: Vec<String> = statement
            .query_map([id], |row| row.get::<_, String>(0))
            .map_err(|error| error.to_string())?
            .collect::<Result<_, _>>()
            .map_err(|error| error.to_string())?;
        if let Some(name) = names.into_iter().next() {
            return Ok(ContactRef {
                provider: "symrelate".to_owned(),
                schema_version: 1,
                id: id.to_owned(),
                kind: kind.to_owned(),
                display_name: name,
                extras: Vec::new(),
            });
        }
    }
    Err(CONTACT_NOT_FOUND.to_owned())
}

/// Go `ResolveMeetingContactRef`.
fn resolve_meeting_contact_ref(id: &str) -> Result<ContactRef, String> {
    if !contact_store_available() {
        return Err(CONTACT_STORE_UNAVAILABLE.to_owned());
    }
    resolve_contact_ref(id)
}

fn contact(args: &[String], json: bool) -> Result<ExitCode, String> {
    let reference = resolve_meeting_contact_ref(arg(args, 0))?;
    #[derive(Serialize)]
    struct Wire<'a> {
        provider: &'a str,
        schema_version: i64,
        id: &'a str,
        kind: &'a str,
        #[serde(skip_serializing_if = "str::is_empty")]
        display_name: &'a str,
    }
    let text = format!(
        "&{{Provider:{} SchemaVersion:{} ID:{} Kind:{} DisplayName:{} Extras:map[]}}",
        reference.provider,
        reference.schema_version,
        reference.id,
        reference.kind,
        reference.display_name
    );
    Ok(emit(
        &Wire {
            provider: &reference.provider,
            schema_version: reference.schema_version,
            id: &reference.id,
            kind: &reference.kind,
            display_name: &reference.display_name,
        },
        text,
        json,
    ))
}

fn link_contact(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    args: &[String],
    json: bool,
) -> Result<ExitCode, String> {
    let (note, speaker, contact_id) = (arg(args, 0), arg(args, 1), arg(args, 2));
    let reference = resolve_meeting_contact_ref(contact_id)?;
    let (doc, mut fm) = load(root, note)?;
    let index = participant_index(&fm, speaker).ok_or_else(|| {
        format!(
            "no participant with speaker id {} in {note}",
            go_quote(speaker)
        )
    })?;
    let linked_id = reference.id.clone();
    fm.participants[index].contact_ref = Some(reference);
    write(root, db, note, &doc, &fm)?;
    Ok(status_map(
        &[
            ("speaker_id", speaker),
            ("contact_id", &linked_id),
            ("status", "linked"),
        ],
        json,
    ))
}

// ----------------------------------------------------------- memory-backed

fn symmemory() -> Result<Resolver, String> {
    let resolver = Resolver::from_environment().map_err(|error| error.to_string())?;
    if ToolCache::default()
        .has_tool(&resolver, "symmemory")
        .available
    {
        Ok(resolver)
    } else {
        Err(SYMMEMORY_UNAVAILABLE.to_owned())
    }
}

fn candidates(args: &[String], json: bool) -> Result<ExitCode, String> {
    let resolver = symmemory()?;
    let resolve = |name: &str| resolver.resolve(name);
    let label = arg(args, 0);
    let matches = memory::resolve_candidates(&resolve, label)
        .map_err(|error| {
            format!(
                "failed to resolve candidates for {}: {error}",
                go_quote(label)
            )
        })?
        .unwrap_or_default();
    #[derive(Serialize)]
    struct Candidate {
        entity_id: String,
        name: String,
        match_reason: String,
    }
    let rows: Vec<Candidate> = matches
        .into_iter()
        .map(|found| Candidate {
            entity_id: found.entity.id,
            name: found.entity.name,
            match_reason: found.match_reason,
        })
        .collect();
    let text = format!(
        "[{}]",
        rows.iter()
            .map(|row| format!(
                "{{EntityID:{} Name:{} MatchReason:{}}}",
                row.entity_id, row.name, row.match_reason
            ))
            .collect::<Vec<_>>()
            .join(" ")
    );
    Ok(emit(&rows, text, json))
}

fn create(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    args: &[String],
    json: bool,
) -> Result<ExitCode, String> {
    let (note, speaker, name) = (arg(args, 0), arg(args, 1), arg(args, 2));
    let resolver = symmemory()?;
    let resolve = |tool: &str| resolver.resolve(tool);
    let trimmed = name.trim();
    if trimmed.is_empty() {
        return Err("a person name is required".to_owned());
    }
    let entity = memory::ensure_entity(&resolve, trimmed, "person").map_err(|error| {
        format!(
            "failed to create Memory person {}: {error}",
            go_quote(trimmed)
        )
    })?;
    confirm_participant(root, db, note, speaker, &entity.id)?;
    Ok(status_map(
        &[
            ("speaker_id", speaker),
            ("entity_id", &entity.id),
            ("name", name),
            ("status", "created"),
        ],
        json,
    ))
}

fn fact_hash(value: &str) -> String {
    symdesk_vault::sha256_hex(value.trim().as_bytes())
}

#[derive(Serialize)]
struct PublishResult {
    meeting_entity_id: String,
    relations_created: i64,
    facts_published: Option<Vec<String>>,
    facts_skipped: i64,
}

fn publish(root: &Path, command: &ArgMatches, json: bool) -> Result<ExitCode, String> {
    let note = arg(&args_of(command), 0).to_owned();
    let facts: Vec<String> = command
        .get_many::<String>("fact")
        .into_iter()
        .flatten()
        .cloned()
        .collect();
    let resolver = symmemory()?;
    let resolve = |tool: &str| resolver.resolve(tool);
    let (doc, fm) = load(root, &note)?;
    if fm.meeting_id.is_empty() {
        return Err(format!("{note} has no meeting_id in frontmatter"));
    }
    let meeting_name = format!("Meeting {}", fm.meeting_id);
    let meeting = memory::ensure_entity(&resolve, &meeting_name, "other")
        .map_err(|error| format!("failed to ensure a Memory entity for {meeting_name}: {error}"))?;
    let mut result = PublishResult {
        meeting_entity_id: meeting.id,
        relations_created: 0,
        facts_published: None,
        facts_skipped: 0,
    };
    if !fm.participants.is_empty() {
        let entities = memory::list_entities(&resolve)
            .map_err(|error| format!("failed to list Memory entities: {error}"))?
            .unwrap_or_default();
        for participant in &fm.participants {
            if participant.entity_id.is_empty() {
                continue;
            }
            let Some(entity) = entities
                .iter()
                .rev()
                .find(|entity| entity.id == participant.entity_id)
            else {
                return Err(format!(
                    "confirmed participant {} references unknown Memory entity id {}",
                    go_quote(&participant.label),
                    go_quote(&participant.entity_id)
                ));
            };
            memory::relate_entities(&resolve, &entity.name, "attended", &meeting_name).map_err(
                |error| {
                    format!(
                        "failed to relate {} to {meeting_name}: {error}",
                        entity.name
                    )
                },
            )?;
            result.relations_created += 1;
        }
    }

    let mut ledger: Vec<String> = Vec::new();
    if let Some(Value::Sequence(items)) = doc.frontmatter.get(PUBLISHED_FACTS_KEY) {
        for item in items {
            if let Value::String(hash) = item
                && !ledger.contains(hash)
            {
                ledger.push(hash.clone());
            }
        }
    }
    let mut known = ledger.clone();
    for fact in &facts {
        let value = fact.trim();
        if value.is_empty() {
            continue;
        }
        let hash = fact_hash(value);
        if known.contains(&hash) {
            result.facts_skipped += 1;
            continue;
        }
        let record = memory::set_memory(
            &resolve,
            value,
            "project",
            std::slice::from_ref(&meeting_name),
        )
        .map_err(|error| format!("failed to publish fact {}: {error}", go_quote(value)))?;
        result
            .facts_published
            .get_or_insert_with(Vec::new)
            .push(record.id);
        known.push(hash.clone());
        ledger.push(hash);
        let path = secure_path(root, &note).map_err(|error| error.to_string())?;
        set_frontmatter_value(&path, PUBLISHED_FACTS_KEY, &strings_value(&ledger)).map_err(
            |error| {
                format!(
                    "published fact to Memory but failed to record it on the note (a repeat apply may duplicate it): {error}"
                )
            },
        )?;
    }

    let published = result.facts_published.clone().unwrap_or_default();
    let text = format!(
        "&{{MeetingEntityID:{} RelationsCreated:{} FactsPublished:{} FactsSkipped:{}}}",
        result.meeting_entity_id,
        result.relations_created,
        join_list(&published),
        result.facts_skipped
    );
    Ok(emit(&result, text, json))
}
