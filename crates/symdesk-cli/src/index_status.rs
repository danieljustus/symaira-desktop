use std::{collections::BTreeMap, process::Command, time::Duration};

use clap::ArgMatches;
use serde::Serialize;
use symdesk_index::{RetrievalDb, index_location_for_vault, retrieval_embedding_config};
use time::{
    OffsetDateTime, PrimitiveDateTime, UtcOffset, format_description::well_known::Rfc3339,
    macros::format_description,
};
use tokio::runtime::Builder;

use super::status_process::{self, ProcessError, ProcessOutput};

const WORKER_PHASE_PREFIX: &str = "SYMDESK_INDEX_STATUS_PHASE ";
const NO_VAULT_ERROR: &str = "vault path not configured (use flag or SYMDESK_VAULT env)";
const VALID_STATES: &[&str] = &[
    "queued",
    "indexing",
    "indexed",
    "failed",
    "encrypted",
    "unsupported",
];

#[derive(Serialize)]
struct AggregateStatus {
    document_count: i64,
    chunk_count: i64,
    database_bytes: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    last_indexed_at: Option<String>,
    embedding_model: String,
    backend_available: bool,
    pending_chunk_count: i64,
    mixed_embedding_spaces: bool,
    index_scope: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    vault_document_count: Option<usize>,
    index_location: String,
}

#[derive(Serialize)]
struct DocumentStatus {
    #[serde(serialize_with = "serialize_diagnostic_text")]
    path: Vec<u8>,
    #[serde(serialize_with = "serialize_diagnostic_text")]
    index_state: Vec<u8>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        serialize_with = "serialize_diagnostic_text"
    )]
    index_failure_reason: Vec<u8>,
    index_updated_at: String,
}

// This read-only diagnostic surface retains SQLite text bytes. Do not use its
// replacement-rendered JSON values as filesystem identities or mutation keys.
fn serialize_diagnostic_text<S: serde::Serializer>(
    bytes: &[u8],
    serializer: S,
) -> Result<S::Ok, S::Error> {
    use serde::ser::Error;
    let mut remaining = bytes;
    let mut quoted = String::from("\"");
    while !remaining.is_empty() {
        let (valid, invalid) = match std::str::from_utf8(remaining) {
            Ok(text) => (text, false),
            Err(error) => (
                std::str::from_utf8(&remaining[..error.valid_up_to()]).map_err(S::Error::custom)?,
                true,
            ),
        };
        let encoded = serde_json::to_string(valid).map_err(S::Error::custom)?;
        quoted.push_str(&encoded[1..encoded.len() - 1]);
        remaining = &remaining[valid.len()..];
        if invalid {
            // encoding/json consumes one invalid byte per replacement, unlike
            // from_utf8_lossy which may collapse a truncated sequence.
            quoted.push_str("\\ufffd");
            remaining = &remaining[1..];
        }
    }
    quoted.push('"');
    serde_json::value::RawValue::from_string(quoted)
        .map_err(S::Error::custom)?
        .serialize(serializer)
}

pub fn run(matches: &ArgMatches, vault: Option<&str>, json_output: bool) -> std::process::ExitCode {
    let timeout_value = matches
        .get_one::<String>("timeout")
        .map_or("10s", String::as_str);
    let timeout = match parse_timeout(timeout_value) {
        Ok(timeout) => timeout,
        Err(error) => return super::super::emit_error(error, json_output),
    };

    if matches.get_flag("worker") {
        eprintln!("{WORKER_PHASE_PREFIX}worker startup");
        return match run_in_process(
            vault,
            json_output,
            matches.get_flag("documents"),
            matches.get_one::<String>("state").map(String::as_str),
        ) {
            Ok(rendered) => super::super::write_stdout_bytes(&rendered),
            Err(error) => super::super::write_stderr(
                &format!("{error}\n"),
                symaira_core_exit::ExitCode::Generic,
            ),
        };
    }

    run_parent(matches, vault, json_output, timeout)
}

fn run_parent(
    matches: &ArgMatches,
    vault: Option<&str>,
    json_output: bool,
    timeout: Duration,
) -> std::process::ExitCode {
    let executable = match std::env::current_exe() {
        Ok(executable) => executable,
        Err(error) => {
            return super::super::emit_error(
                format!("failed to resolve index status worker executable: {error}"),
                json_output,
            );
        }
    };
    let mut command = Command::new(executable);
    if json_output {
        command.arg("--json");
    }
    if let Some(vault) = vault.filter(|value| !value.is_empty()) {
        command.arg("--vault").arg(vault);
    }
    command
        .arg("index")
        .arg("status")
        .arg("--worker")
        .arg("--timeout")
        .arg("0s");
    if matches.get_flag("documents") {
        command.arg("--documents");
    }
    if let Some(state) = matches.get_one::<String>("state") {
        command.arg("--state").arg(state);
    }

    let deadline = (!timeout.is_zero()).then_some(timeout);
    match status_process::run_bounded(command, deadline) {
        Ok(output) if output.status.success() => super::super::write_stdout_bytes(&output.stdout),
        Ok(output) => super::super::emit_error(worker_error(&output), json_output),
        Err(ProcessError::TimedOut(output)) => {
            let phase = worker_phase(&output.stderr).unwrap_or_else(|| "worker startup".to_owned());
            super::super::emit_error(
                format!(
                    "index status timed out during {phase} after {}: context deadline exceeded",
                    go_duration(timeout)
                ),
                json_output,
            )
        }
        Err(ProcessError::Spawn(error)) => super::super::emit_error(
            format!("failed to start index status worker: {error}"),
            json_output,
        ),
        Err(ProcessError::Wait(error)) => super::super::emit_error(
            format!("failed to wait for index status worker: {error}"),
            json_output,
        ),
        Err(ProcessError::Read(error)) => super::super::emit_error(
            format!("failed to read index status worker output: {error}"),
            json_output,
        ),
    }
}

fn run_in_process(
    vault: Option<&str>,
    json_output: bool,
    documents: bool,
    state: Option<&str>,
) -> Result<Vec<u8>, String> {
    eprintln!("{WORKER_PHASE_PREFIX}worker startup");
    if documents {
        return render_documents(vault, state, json_output);
    }
    render_aggregate(vault, json_output).map(String::into_bytes)
}

fn render_documents(
    vault: Option<&str>,
    state: Option<&str>,
    json_output: bool,
) -> Result<Vec<u8>, String> {
    eprintln!("{WORKER_PHASE_PREFIX}resolve vault root");
    let root = required_vault(vault)?;
    eprintln!("{WORKER_PHASE_PREFIX}open sidecar database");
    let sidecar = symdesk_index::open_for_vault(&root).map_err(|error| error.to_string())?;
    eprintln!("{WORKER_PHASE_PREFIX}document status listing");
    let state = state.filter(|state| !state.is_empty());
    if let Some(state) = state.filter(|state| !VALID_STATES.contains(state)) {
        return Err(format!(
            "invalid index state {}",
            symdesk_vault::go_quote(state)
        ));
    }
    let statuses = sidecar
        .list_index_statuses()
        .map_err(|error| error.to_string())?
        .into_iter()
        .filter(|status| state.is_none_or(|state| status.state == state.as_bytes()))
        .map(|status| {
            Ok(DocumentStatus {
                path: status.path,
                index_state: status.state,
                index_failure_reason: status.reason,
                index_updated_at: format_go_rfc3339(&status.updated_at, false, true)?,
            })
        })
        .collect::<Result<Vec<_>, String>>()?;
    if json_output {
        if statuses.is_empty() {
            return Ok(b"null\n".to_vec());
        }
        let mut rendered = serde_json::to_string(&statuses).map_err(|error| error.to_string())?;
        rendered.push('\n');
        return Ok(super::super::go_escape_json(rendered).into_bytes());
    }

    let mut rendered = b"[".to_vec();
    for (index, status) in statuses.iter().enumerate() {
        if index != 0 {
            rendered.push(b' ');
        }
        rendered.extend_from_slice(b"{Path:");
        rendered.extend_from_slice(&status.path);
        rendered.extend_from_slice(b" State:");
        rendered.extend_from_slice(&status.index_state);
        rendered.extend_from_slice(b" Reason:");
        rendered.extend_from_slice(&status.index_failure_reason);
        rendered.extend_from_slice(b" UpdatedAt:");
        rendered.extend_from_slice(go_time_text(&status.index_updated_at)?.as_bytes());
        rendered.push(b'}');
    }
    rendered.extend_from_slice(b"]\n");
    Ok(rendered)
}

fn render_aggregate(vault: Option<&str>, json_output: bool) -> Result<String, String> {
    eprintln!("{WORKER_PHASE_PREFIX}retrieval status");
    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let cwd = std::env::current_dir()
        .map_err(|error| format!("failed to get current directory: {error}"))?;
    let temp_root = std::env::temp_dir();
    let location = index_location_for_vault("", &environment, &cwd, &temp_root)
        .map_err(|error| error.to_string())?;
    let database = RetrievalDb::open_at(&location).map_err(retrieval_open_error)?;
    let snapshot = database
        .status_snapshot()
        .map_err(|error| error.to_string())?;
    let last_indexed_at = snapshot
        .last_indexed_at
        .as_deref()
        .and_then(aggregate_timestamp);
    let embedding_config =
        retrieval_embedding_config(&environment, &cwd).map_err(|error| error.to_string())?;
    let backend_available = embedding_backend_available(&embedding_config)?;
    let embedding_model = if backend_available {
        embedding_config.model.clone()
    } else {
        "local-hash".to_owned()
    };
    let vault_document_count = optional_vault(vault)?
        .map(|root| {
            eprintln!("{WORKER_PHASE_PREFIX}vault counting");
            symdesk_vault::walk_markdown(&root).map(|paths| paths.len())
        })
        .transpose()
        .map_err(|error| error.to_string())?;

    let status = AggregateStatus {
        document_count: snapshot.document_count,
        chunk_count: snapshot.chunk_count,
        database_bytes: snapshot.database_bytes,
        last_indexed_at,
        embedding_model,
        backend_available,
        pending_chunk_count: snapshot.pending_chunk_count,
        mixed_embedding_spaces: snapshot.mixed_embedding_spaces,
        index_scope: "shared",
        vault_document_count,
        index_location: location.to_string_lossy().into_owned(),
    };
    if json_output {
        let mut rendered = serde_json::to_string(&status).map_err(|error| error.to_string())?;
        rendered.push('\n');
        return Ok(super::super::go_escape_json(rendered));
    }
    Ok(aggregate_text(&status))
}

fn embedding_backend_available(
    config: &symdesk_index::RetrievalEmbeddingConfig,
) -> Result<bool, String> {
    let runtime = Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|error| format!("failed to create index status runtime: {error}"))?;
    let inputs = ["symdesk retrieval status probe".to_owned()];
    let vectors = runtime.block_on(symdesk_protocol::embed_local_ollama(
        &config.ollama_url,
        &config.model,
        &inputs,
        config.embedding_dim,
        Duration::from_secs(config.timeout_seconds),
    ));
    Ok(vectors.is_ok_and(|vectors| {
        vectors.len() == 1
            && vectors[0].iter().all(|value| value.is_finite())
            && config
                .embedding_dim
                .is_none_or(|dimension| vectors[0].len() == dimension)
    }))
}

fn required_vault(vault: Option<&str>) -> Result<std::path::PathBuf, String> {
    match super::super::resolve_vault(vault) {
        Ok(root) => Ok(root),
        Err(error) if error.starts_with("vault path not configured") => {
            Err(NO_VAULT_ERROR.to_owned())
        }
        Err(error) => Err(super::index_vault_error(vault, &error)),
    }
}

fn optional_vault(vault: Option<&str>) -> Result<Option<std::path::PathBuf>, String> {
    match super::super::resolve_vault_with_report(vault, || {
        eprintln!("{WORKER_PHASE_PREFIX}resolve vault root");
    }) {
        Ok(root) => Ok(Some(root)),
        Err(error)
            if vault.filter(|value| !value.is_empty()).is_none()
                && error.starts_with("vault path not configured") =>
        {
            Ok(None)
        }
        Err(error) => Err(super::index_vault_error(vault, &error)),
    }
}

fn retrieval_open_error(error: impl ToString) -> String {
    let error = error.to_string();
    if error.contains("file is not a database") {
        return "failed to open sqlite database: failed to open sqlite database: file is not a database (26)".to_owned();
    }
    error
}

fn worker_phase(stderr: &[u8]) -> Option<String> {
    String::from_utf8_lossy(stderr)
        .lines()
        .filter_map(|line| line.strip_prefix(WORKER_PHASE_PREFIX))
        .next_back()
        .map(str::to_owned)
}

fn worker_error(output: &ProcessOutput) -> String {
    let error = String::from_utf8_lossy(&output.stderr)
        .lines()
        .filter(|line| !line.starts_with(WORKER_PHASE_PREFIX))
        .collect::<Vec<_>>()
        .join("\n")
        .trim()
        .to_owned();
    if error.is_empty() {
        format!(
            "index status worker exited unsuccessfully: {}",
            output.status
        )
    } else {
        error
    }
}

fn parse_timeout(value: &str) -> Result<Duration, String> {
    let nanoseconds = parse_go_duration_nanos(value).map_err(|error| {
        format!(
            "invalid argument {} for \"--timeout\" flag: {error}",
            symdesk_vault::go_quote(value)
        )
    })?;
    if nanoseconds < 0 {
        return Err("--timeout must be non-negative".to_owned());
    }
    // Parsing already enforces Go's signed 64-bit nanosecond range.
    Ok(Duration::from_nanos(nanoseconds as u64))
}

fn duration_quote(value: &str) -> String {
    // time.quote is not strconv.Quote: non-ASCII/control bytes use hex,
    // but DEL is retained literally. Keep this local to time diagnostics.
    let mut output = String::from("\"");
    for byte in value.bytes() {
        match byte {
            b'"' => output.push_str("\\\""),
            b'\\' => output.push_str("\\\\"),
            b' '..=0x7f => output.push(char::from(byte)),
            _ => output.push_str(&format!("\\x{byte:02x}")),
        }
    }
    output.push('"');
    output
}

fn parse_go_duration_nanos(input: &str) -> Result<i128, String> {
    let original = input;
    let invalid = || format!("time: invalid duration {}", duration_quote(original));
    let (negative, input) = match input.strip_prefix('-') {
        Some(rest) => (true, rest),
        None => match input.strip_prefix('+') {
            Some(rest) => (false, rest),
            None => (false, input),
        },
    };
    // Go checks the zero sentinel after consuming the optional sign.
    if input == "0" {
        return Ok(0);
    }
    if input.is_empty() {
        return Err(invalid());
    }

    let mut remaining = input;
    let mut total: u128 = 0;
    while !remaining.is_empty() {
        let integer_end = remaining
            .find(|character: char| !character.is_ascii_digit())
            .unwrap_or(remaining.len());
        let integer = &remaining[..integer_end];
        remaining = &remaining[integer_end..];
        let whole = if integer.is_empty() {
            0
        } else {
            integer
                .parse::<u128>()
                .ok()
                .filter(|value| *value <= 1_u128 << 63)
                .ok_or_else(&invalid)?
        };
        let mut fraction = "";
        if let Some(after_dot) = remaining.strip_prefix('.') {
            let fraction_end = after_dot
                .find(|character: char| !character.is_ascii_digit())
                .unwrap_or(after_dot.len());
            fraction = &after_dot[..fraction_end];
            remaining = &after_dot[fraction_end..];
        }
        if integer.is_empty() && fraction.is_empty() {
            return Err(invalid());
        }
        let unit_end = remaining
            .find(|character: char| character == '.' || character.is_ascii_digit())
            .unwrap_or(remaining.len());
        if unit_end == 0 {
            return Err(format!(
                "time: missing unit in duration {}",
                duration_quote(original)
            ));
        }
        let unit = &remaining[..unit_end];
        let scale = match unit {
            "ns" => 1_u128,
            "us" | "µs" | "μs" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            _ => {
                return Err(format!(
                    "time: unknown unit {} in duration {}",
                    duration_quote(unit),
                    duration_quote(original)
                ));
            }
        };
        let mut component = whole.checked_mul(scale).ok_or_else(&invalid)?;
        if !fraction.is_empty() {
            // Match time.leadingFraction in the pinned Go toolchain: excess
            // digits are consumed but stop contributing after integer overflow.
            // Go deliberately uses float64 here for fractions of hours.
            let mut numerator = 0_u64;
            let mut denominator = 1_f64;
            for digit in fraction.bytes() {
                if numerator > (i64::MAX as u64) / 10 {
                    break;
                }
                let next = numerator * 10 + u64::from(digit - b'0');
                if next > 1_u64 << 63 {
                    break;
                }
                numerator = next;
                denominator *= 10.0;
            }
            component = component
                .checked_add((numerator as f64 * (scale as f64 / denominator)) as u128)
                .ok_or_else(&invalid)?;
        }
        total = total.checked_add(component).ok_or_else(&invalid)?;
        if total > 1_u128 << 63 {
            return Err(invalid());
        }
        remaining = &remaining[unit.len()..];
    }
    if !negative && total > i64::MAX as u128 {
        return Err(invalid());
    }
    let total = total as i128;
    Ok(if negative { -total } else { total })
}

fn go_duration(value: Duration) -> String {
    let nanos = value.as_nanos();
    if nanos == 0 {
        return "0s".to_owned();
    }
    if nanos < 1_000_000_000 {
        if nanos >= 1_000_000 {
            return scaled_duration(nanos, 1_000_000, "ms");
        }
        if nanos >= 1_000 {
            return scaled_duration(nanos, 1_000, "µs");
        }
        return format!("{nanos}ns");
    }

    let mut remaining = nanos;
    let hours = remaining / 3_600_000_000_000;
    remaining %= 3_600_000_000_000;
    let minutes = remaining / 60_000_000_000;
    remaining %= 60_000_000_000;
    let seconds = remaining / 1_000_000_000;
    let fraction = remaining % 1_000_000_000;
    let seconds = if fraction == 0 {
        format!("{seconds}s")
    } else {
        let fraction = format!("{fraction:09}");
        format!("{seconds}.{}s", fraction.trim_end_matches('0'))
    };
    if hours > 0 {
        format!("{hours}h{minutes}m{seconds}")
    } else if minutes > 0 {
        format!("{minutes}m{seconds}")
    } else {
        seconds
    }
}

fn scaled_duration(nanos: u128, scale: u128, unit: &str) -> String {
    let whole = nanos / scale;
    let remainder = nanos % scale;
    if remainder == 0 {
        return format!("{whole}{unit}");
    }
    let width = scale.ilog10() as usize;
    let fraction = format!("{remainder:0width$}");
    format!("{whole}.{}{unit}", fraction.trim_end_matches('0'))
}

// Retrieval GetStats is deliberately best-effort and accepts database/sql's
// time.Time.String storage. Document lifecycle timestamps remain RFC3339-only.
fn aggregate_timestamp(value: &str) -> Option<String> {
    let fields = value.split_whitespace().collect::<Vec<_>>();
    let normalized = if fields.len() >= 4 {
        fields[..4].join(" ")
    } else {
        value.to_owned()
    };
    let parsed = OffsetDateTime::parse(&normalized, &Rfc3339)
        .ok()
        .or_else(|| {
            let [date, clock, offset, zone, ..] = fields.as_slice() else {
                return None;
            };
            if date.len() != 10 || !go_zone_name(zone) {
                return None;
            }
            let offset = offset.as_bytes();
            if offset.len() != 5
                || !matches!(offset[0], b'+' | b'-')
                || !offset[1..].iter().all(u8::is_ascii_digit)
            {
                return None;
            }
            let hours = i64::from(offset[1] - b'0') * 10 + i64::from(offset[2] - b'0');
            let minutes = i64::from(offset[3] - b'0') * 10 + i64::from(offset[4] - b'0');
            if hours > 24 || minutes > 60 {
                return None;
            }
            let wall = format!("{date} {}", clock.replace(',', "."));
            let parsed = PrimitiveDateTime::parse(
                &wall,
                format_description!(
                    "[year]-[month]-[day] [hour padding:none]:[minute]:[second].[subsecond]"
                ),
            )
            .or_else(|_| {
                PrimitiveDateTime::parse(
                    &wall,
                    format_description!(
                        "[year]-[month]-[day] [hour padding:none]:[minute]:[second]"
                    ),
                )
            })
            .ok()?;
            // Go treats the literal UTC abbreviation specially, even when the
            // numeric offset disagrees. Other accepted names use that offset.
            let seconds = if *zone == "UTC" {
                0
            } else {
                (hours * 3_600 + minutes * 60) * if offset[0] == b'-' { -1 } else { 1 }
            };
            parsed
                .assume_utc()
                .checked_sub(time::Duration::seconds(seconds))
        })?
        .to_offset(UtcOffset::UTC);
    if parsed == time::macros::datetime!(0001-01-01 0:00 UTC) {
        return None;
    }
    Some(render_go_rfc3339(parsed, false))
}

fn go_zone_name(zone: &str) -> bool {
    if matches!(zone, "ChST" | "MeST" | "WITA") {
        return true;
    }
    let bytes = zone.as_bytes();
    if bytes.iter().all(u8::is_ascii_uppercase)
        && (bytes.len() == 3 || ((bytes.len() == 4 || bytes.len() == 5) && zone.ends_with('T')))
    {
        return true;
    }
    let signed = zone.strip_prefix("GMT").unwrap_or(zone);
    signed.strip_prefix(['+', '-']).is_some_and(|hours| {
        !hours.is_empty()
            && hours.len() <= 2
            && hours.bytes().all(|byte| byte.is_ascii_digit())
            && hours.parse::<u8>().is_ok_and(|hours| hours <= 23)
    })
}

fn format_go_rfc3339(value: &str, utc: bool, fractional: bool) -> Result<String, String> {
    let parsed = OffsetDateTime::parse(value, &Rfc3339)
        .map_err(|error| format!("invalid index timestamp {value:?}: {error}"))?;
    let parsed = if utc {
        parsed.to_offset(UtcOffset::UTC)
    } else {
        parsed
    };
    Ok(render_go_rfc3339(parsed, fractional))
}

fn render_go_rfc3339(parsed: OffsetDateTime, fractional: bool) -> String {
    let year = if parsed.year() < 0 {
        format!("-{:04}", -parsed.year())
    } else {
        format!("{:04}", parsed.year())
    };
    let mut rendered = format!(
        "{year}-{:02}-{:02}T{:02}:{:02}:{:02}",
        u8::from(parsed.month()),
        parsed.day(),
        parsed.hour(),
        parsed.minute(),
        parsed.second()
    );
    if fractional && parsed.nanosecond() != 0 {
        let fraction = format!("{:09}", parsed.nanosecond());
        rendered.push('.');
        rendered.push_str(fraction.trim_end_matches('0'));
    }
    let offset = parsed.offset().whole_seconds();
    if offset == 0 {
        rendered.push('Z');
    } else {
        let sign = if offset < 0 { '-' } else { '+' };
        let absolute = offset.unsigned_abs();
        rendered.push_str(&format!(
            "{sign}{:02}:{:02}",
            absolute / 3_600,
            (absolute % 3_600) / 60
        ));
    }
    rendered
}

fn go_time_text(value: &str) -> Result<String, String> {
    let parsed = OffsetDateTime::parse(value, &Rfc3339)
        .map_err(|error| format!("invalid index timestamp {value:?}: {error}"))?;
    let mut rendered = format!(
        "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
        parsed.year(),
        u8::from(parsed.month()),
        parsed.day(),
        parsed.hour(),
        parsed.minute(),
        parsed.second()
    );
    if parsed.nanosecond() != 0 {
        let fraction = format!("{:09}", parsed.nanosecond());
        rendered.push('.');
        rendered.push_str(fraction.trim_end_matches('0'));
    }
    let offset = parsed.offset().whole_seconds();
    if offset == 0 {
        rendered.push_str(" +0000 UTC");
    } else {
        let sign = if offset < 0 { '-' } else { '+' };
        let absolute = offset.unsigned_abs();
        let zone = format!(
            "{sign}{:02}{:02}",
            absolute / 3_600,
            (absolute % 3_600) / 60
        );
        rendered.push_str(&format!(" {zone} {zone}"));
    }
    Ok(rendered)
}

fn aggregate_text(status: &AggregateStatus) -> String {
    let vault_document_count = status.vault_document_count.map_or_else(
        || "<nil>".to_owned(),
        |count| {
            let boxed = Box::new(count);
            format!("{:p}", boxed.as_ref())
        },
    );
    let last_indexed_at = status.last_indexed_at.as_deref().unwrap_or_default();
    format!(
        "&{{DocumentCount:{} ChunkCount:{} DatabaseBytes:{} LastIndexedAt:{} EmbeddingModel:{} BackendAvailable:{} PendingChunkCount:{} MixedEmbeddingSpaces:{} IndexScope:{} VaultDocumentCount:{} IndexLocation:{}}}\n",
        status.document_count,
        status.chunk_count,
        status.database_bytes,
        last_indexed_at,
        status.embedding_model,
        status.backend_available,
        status.pending_chunk_count,
        status.mixed_embedding_spaces,
        status.index_scope,
        vault_document_count,
        status.index_location
    )
}

#[cfg(test)]
mod tests {
    use super::{format_go_rfc3339, go_duration, parse_go_duration_nanos, parse_timeout};
    use std::time::Duration;

    #[test]
    fn aggregate_timestamp_replays_native_go_storage_without_broadening_documents() {
        // Expected values come from the canonical Go executable's SQLite status
        // reads, including UTC's special abbreviation and year rollover.
        for (input, expected) in [
            (
                "2026-01-02 03:04:05 +0000 UTC",
                Some("2026-01-02T03:04:05Z"),
            ),
            (
                "2026-01-02 03:04:05.123456789 +0000 UTC m=+1.25",
                Some("2026-01-02T03:04:05Z"),
            ),
            (
                "2026-01-02 03:04:05,123456789 +0200 CEST",
                Some("2026-01-02T01:04:05Z"),
            ),
            (
                "2026-01-02 03:04:05 -0530 ABC",
                Some("2026-01-02T08:34:05Z"),
            ),
            (
                "2026-01-02 03:04:05 +0200 UTC",
                Some("2026-01-02T03:04:05Z"),
            ),
            ("9999-12-31T23:59:59-01:00", Some("10000-01-01T00:59:59Z")),
            ("0000-01-01T00:00:00+01:00", Some("-0001-12-31T23:00:00Z")),
            ("not a timestamp", None),
            ("2026-01-02 03:04:05 +0200 abcd", None),
            ("0001-01-01T00:00:00Z", None),
            ("0001-01-01 00:00:00 +0000 UTC", None),
            ("", None),
        ] {
            assert_eq!(
                super::aggregate_timestamp(input).as_deref(),
                expected,
                "{input}"
            );
        }
        assert!(format_go_rfc3339("2026-01-02 03:04:05 +0000 UTC", false, true).is_err());
        assert_eq!(
            super::worker_phase(b"unmarked failure\n")
                .unwrap_or_else(|| "worker startup".to_owned()),
            "worker startup"
        );
        assert_eq!(super::worker_phase(b"SYMDESK_INDEX_STATUS_PHASE worker startup\nSYMDESK_INDEX_STATUS_PHASE document status listing\n").as_deref(), Some("document status listing"));
    }

    #[test]
    fn diagnostic_json_preserves_go_escaping_and_invalid_byte_boundaries() {
        // These bytes were compared against encoding/json through the real Go
        // index-status command, including a truncated two-byte UTF-8 fragment.
        let row = super::DocumentStatus {
            path: b"invalid-\xff-\xe2\x82.md".to_vec(),
            index_state: b"failed".to_vec(),
            index_failure_reason: "x<&>\u{2028}\u{2029}�".as_bytes().to_vec(),
            index_updated_at: "2026-01-02T03:04:05Z".into(),
        };
        let encoded = super::super::super::go_escape_json(serde_json::to_string(&row).unwrap());
        assert_eq!(
            encoded,
            "{\"path\":\"invalid-\\ufffd-\\ufffd\\ufffd.md\",\"index_state\":\"failed\",\"index_failure_reason\":\"x\\u003c\\u0026\\u003e\\u2028\\u2029�\",\"index_updated_at\":\"2026-01-02T03:04:05Z\"}"
        );
    }

    #[test]
    fn default_and_zero_deadlines_are_distinct() {
        assert_eq!(
            parse_timeout("10s").expect("default"),
            Duration::from_secs(10)
        );
        assert_eq!(parse_timeout("0s").expect("zero"), Duration::ZERO);
        assert_eq!(
            parse_timeout("-1ms").expect_err("negative"),
            "--timeout must be non-negative"
        );
    }

    #[test]
    fn cli_timeout_default_is_ten_seconds() {
        let matches = super::super::cli()
            .try_get_matches_from(["index", "status"])
            .expect("index status arguments");
        let (_, status) = matches.subcommand().expect("status subcommand");
        assert_eq!(
            status.get_one::<String>("timeout").map(String::as_str),
            Some("10s")
        );
    }

    #[test]
    fn duration_parser_accepts_compound_and_fractional_go_syntax() {
        assert_eq!(parse_go_duration_nanos("1h2m3.5s"), Ok(3_723_500_000_000));
        assert_eq!(parse_go_duration_nanos(".5s"), Ok(500_000_000));
        assert_eq!(parse_go_duration_nanos("1.5µs"), Ok(1_500));
        assert_eq!(parse_go_duration_nanos("1.s"), Ok(1_000_000_000));
        for zero in [
            "+0",
            "-0",
            "0.000000000000000000000000000000000000000000000001s",
        ] {
            assert_eq!(parse_go_duration_nanos(zero), Ok(0), "{zero}");
        }
        assert!(parse_go_duration_nanos(".s").is_err());
        for (input, expected) in [
            ("bad", "time: invalid duration \"bad\""),
            ("1..s", "time: missing unit in duration \"1..s\""),
            ("1.sx", "time: unknown unit \"sx\" in duration \"1.sx\""),
            (
                "1é",
                "time: unknown unit \"\\xc3\\xa9\" in duration \"1\\xc3\\xa9\"",
            ),
            ("1\n", "time: unknown unit \"\\x0a\" in duration \"1\\x0a\""),
            ("1\x7f", "time: unknown unit \"\x7f\" in duration \"1\x7f\""),
            (
                "9223372036854775808ns",
                "time: invalid duration \"9223372036854775808ns\"",
            ),
        ] {
            assert_eq!(parse_go_duration_nanos(input).unwrap_err(), expected);
        }
        assert_eq!(go_duration(Duration::from_millis(150)), "150ms");
        assert_eq!(go_duration(Duration::from_millis(1_500)), "1.5s");
        assert_eq!(go_duration(Duration::from_nanos(1_000_001)), "1.000001ms");
    }

    #[test]
    fn timestamp_rendering_matches_go_utc_precision() {
        assert_eq!(
            format_go_rfc3339("2026-01-02T03:04:05.123456Z", true, false).expect("seconds"),
            "2026-01-02T03:04:05Z"
        );
        assert_eq!(
            format_go_rfc3339("2026-01-02T03:04:05.123456Z", false, true).expect("fraction"),
            "2026-01-02T03:04:05.123456Z"
        );
    }
}
