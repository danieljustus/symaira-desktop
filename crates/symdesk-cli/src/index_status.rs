use std::{collections::BTreeMap, process::Command, time::Duration};

use clap::ArgMatches;
use serde::Serialize;
use symdesk_index::{RetrievalDb, index_location_for_vault, retrieval_embedding_config};
use time::{OffsetDateTime, UtcOffset, format_description::well_known::Rfc3339};
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
    path: String,
    index_state: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    index_failure_reason: String,
    index_updated_at: String,
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
        eprintln!("{WORKER_PHASE_PREFIX}retrieval status");
        return match run_in_process(
            vault,
            json_output,
            matches.get_flag("documents"),
            matches.get_one::<String>("state").map(String::as_str),
        ) {
            Ok(rendered) => super::super::write_stdout(rendered),
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
        Ok(output) if output.status.success() => match String::from_utf8(output.stdout) {
            Ok(rendered) => super::super::write_stdout(rendered),
            Err(error) => super::super::emit_error(
                format!("index status worker emitted non-UTF-8 output: {error}"),
                json_output,
            ),
        },
        Ok(output) => super::super::emit_error(worker_error(&output), json_output),
        Err(ProcessError::TimedOut(output)) => {
            let phase =
                worker_phase(&output.stderr).unwrap_or_else(|| "retrieval status".to_owned());
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
) -> Result<String, String> {
    if documents && let Some(state) = state.filter(|state| !VALID_STATES.contains(state)) {
        return Err(format!("invalid index state {state:?}"));
    }
    if documents {
        return render_documents(vault, state, json_output);
    }
    render_aggregate(vault, json_output)
}

fn render_documents(
    vault: Option<&str>,
    state: Option<&str>,
    json_output: bool,
) -> Result<String, String> {
    let root = required_vault(vault)?;
    let sidecar = symdesk_index::open_for_vault(&root).map_err(|error| error.to_string())?;
    let mut statuses = sidecar
        .list_index_statuses()
        .map_err(|error| error.to_string())?
        .into_iter()
        .filter(|status| state.is_none_or(|state| status.state == state))
        .map(|status| {
            Ok(DocumentStatus {
                path: status.path,
                index_state: status.state,
                index_failure_reason: status.reason,
                index_updated_at: format_go_rfc3339(&status.updated_at, false, true)?,
            })
        })
        .collect::<Result<Vec<_>, String>>()?;
    statuses.sort_by(|left, right| left.path.cmp(&right.path));

    if json_output {
        if statuses.is_empty() {
            return Ok("null\n".to_owned());
        }
        let mut rendered = serde_json::to_string(&statuses).map_err(|error| error.to_string())?;
        rendered.push('\n');
        return Ok(rendered);
    }

    let rendered = statuses
        .iter()
        .map(|status| {
            format!(
                "{{Path:{} State:{} Reason:{} UpdatedAt:{}}}",
                status.path,
                status.index_state,
                status.index_failure_reason,
                go_time_text(&status.index_updated_at)
                    .unwrap_or_else(|_| status.index_updated_at.clone())
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    Ok(format!("[{rendered}]\n"))
}

fn render_aggregate(vault: Option<&str>, json_output: bool) -> Result<String, String> {
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
        .filter(|value| !value.is_empty())
        .map(|value| format_go_rfc3339(value, true, false))
        .transpose()?;
    let embedding_config =
        retrieval_embedding_config(&environment, &cwd).map_err(|error| error.to_string())?;
    let backend_available = embedding_backend_available(&embedding_config)?;
    let embedding_model = if backend_available {
        embedding_config.model.clone()
    } else {
        "local-hash".to_owned()
    };
    let vault_document_count = optional_vault(vault)?
        .map(|root| symdesk_vault::walk_markdown(&root).map(|paths| paths.len()))
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
        return Ok(rendered);
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
    let inputs = ["symdesk index status".to_owned()];
    let vectors = runtime.block_on(super::embed_with_retries(config, &inputs));
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
    match super::super::resolve_vault(vault) {
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
    let nanoseconds = parse_go_duration_nanos(value)
        .ok_or_else(|| format!("invalid timeout duration {value:?}"))?;
    if nanoseconds < 0 {
        return Err("--timeout must be non-negative".to_owned());
    }
    let nanoseconds =
        u64::try_from(nanoseconds).map_err(|_| format!("invalid timeout duration {value:?}"))?;
    Ok(Duration::from_nanos(nanoseconds))
}

fn parse_go_duration_nanos(input: &str) -> Option<i128> {
    if input == "0" {
        return Some(0);
    }
    let (negative, input) = match input.strip_prefix('-') {
        Some(rest) => (true, rest),
        None => match input.strip_prefix('+') {
            Some(rest) => (false, rest),
            None => (false, input),
        },
    };
    if input.is_empty() {
        return None;
    }

    let mut remaining = input;
    let mut total: u128 = 0;
    while !remaining.is_empty() {
        let integer_end = remaining
            .find(|character: char| !character.is_ascii_digit())
            .unwrap_or(remaining.len());
        let integer = &remaining[..integer_end];
        remaining = &remaining[integer_end..];
        let mut fraction = "";
        let mut had_decimal = false;
        if let Some(after_dot) = remaining.strip_prefix('.') {
            had_decimal = true;
            let fraction_end = after_dot
                .find(|character: char| !character.is_ascii_digit())
                .unwrap_or(after_dot.len());
            fraction = &after_dot[..fraction_end];
            remaining = &after_dot[fraction_end..];
        }
        if (integer.is_empty() && fraction.is_empty()) || (had_decimal && fraction.is_empty()) {
            return None;
        }
        if remaining.starts_with('.') || remaining.is_empty() {
            return None;
        }

        let (unit, scale) = [
            ("ns", 1_u128),
            ("us", 1_000),
            ("µs", 1_000),
            ("μs", 1_000),
            ("ms", 1_000_000),
            ("s", 1_000_000_000),
            ("m", 60_000_000_000),
            ("h", 3_600_000_000_000),
        ]
        .into_iter()
        .find(|(unit, _)| remaining.starts_with(unit))?;
        let whole = if integer.is_empty() {
            0
        } else {
            integer.parse::<u128>().ok()?
        };
        let mut component = whole.checked_mul(scale)?;
        if !fraction.is_empty() {
            let numerator = fraction.parse::<u128>().ok()?;
            let denominator = 10_u128.checked_pow(u32::try_from(fraction.len()).ok()?)?;
            component =
                component.checked_add(numerator.checked_mul(scale)?.checked_div(denominator)?)?;
        }
        total = total.checked_add(component)?;
        if total > i64::MAX as u128 + u128::from(negative) {
            return None;
        }
        remaining = &remaining[unit.len()..];
    }
    let total = i128::try_from(total).ok()?;
    Some(if negative { -total } else { total })
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
    let fraction = remainder * 1_000 / scale;
    let fraction = format!("{fraction:03}");
    format!("{whole}.{}{unit}", fraction.trim_end_matches('0'))
}

fn format_go_rfc3339(value: &str, utc: bool, fractional: bool) -> Result<String, String> {
    let parsed = OffsetDateTime::parse(value, &Rfc3339)
        .map_err(|error| format!("invalid index timestamp {value:?}: {error}"))?;
    let parsed = if utc {
        parsed.to_offset(UtcOffset::UTC)
    } else {
        parsed
    };
    let mut rendered = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
        parsed.year(),
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
    Ok(rendered)
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
        assert_eq!(parse_go_duration_nanos("1h2m3.5s"), Some(3_723_500_000_000));
        assert_eq!(parse_go_duration_nanos(".5s"), Some(500_000_000));
        assert_eq!(parse_go_duration_nanos("1.5µs"), Some(1_500));
        assert_eq!(parse_go_duration_nanos("1.s"), None);
        assert_eq!(go_duration(Duration::from_millis(150)), "150ms");
        assert_eq!(go_duration(Duration::from_millis(1_500)), "1.5s");
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
