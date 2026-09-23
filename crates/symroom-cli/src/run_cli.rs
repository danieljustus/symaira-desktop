use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
    time::Duration,
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{
    identity,
    runs::{self, RunMutationError, RunQueryError, RunWaitError},
};

const USAGE: &str = "Usage: symroom run <request|list|show|start|cancel|wait> [flags] [args]\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let Some(action) = args.first() else {
        return stdout(USAGE.to_owned(), CoreExitCode::Ok);
    };
    match action.to_string_lossy().as_ref() {
        "request" => request(&args[1..]),
        "list" => list(&args[1..]),
        "show" => show(&args[1..]),
        "wait" => wait(&args[1..]),
        "start" => start(&args[1..]),
        "cancel" => cancel(&args[1..]),
        action => stderr(
            &format!("Unknown run action: {action}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn request(args: &[OsString]) -> ExitCode {
    let usage = "Usage of run request:\n  -adapter string\n    \tAdapter name\n  -identity string\n    \tAuthor identity name\n  -plan-file string\n    \tPlan file path\n  -title string\n    \tRun title\n";
    let parsed = match parse_string_flags(
        "run request",
        args,
        &["adapter", "identity", "plan-file", "title"],
        usage,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(title) = parsed.values.get("title").filter(|title| !title.is_empty()) else {
        return stderr("Error: --title is required\n", CoreExitCode::NoInput);
    };
    let identity_name = match resolve_identity_name(parsed.values.get("identity")) {
        Ok(name) => name,
        Err(code) => return code,
    };
    let signer = match identity::load(&identity_name) {
        Ok(identity) => identity,
        Err(error) => return identity_error(&identity_name, error),
    };
    match runs::request(
        &room_dir(),
        title,
        parsed.values.get("plan-file").map_or("", String::as_str),
        parsed.values.get("adapter").map_or("", String::as_str),
        &signer,
    ) {
        Ok(event) => stdout(format!("{}\n", event.id), CoreExitCode::Ok),
        Err(error) => stderr(
            &format!("Error requesting run: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn start(args: &[OsString]) -> ExitCode {
    let usage = "Usage of run start:\n  -identity string\n    \tAuthor identity name\n";
    let parsed = match parse_string_flags("run start", args, &["identity"], usage) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(run_id) = parsed.positionals.first() else {
        return stderr(
            "Usage: symroom run start <run_id> [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    };
    let identity_name = match resolve_identity_name(parsed.values.get("identity")) {
        Ok(name) => name,
        Err(code) => return code,
    };
    let signer = match identity::load(&identity_name) {
        Ok(identity) => identity,
        Err(error) => return identity_error(&identity_name, error),
    };
    match runs::start(&room_dir(), run_id, &signer) {
        Ok(event) => stdout(format!("{}\n", event.id), CoreExitCode::Ok),
        Err(RunMutationError::InvalidTransition(error)) => stderr(
            &format!("Error: invalid run state transition: {error}\n"),
            CoreExitCode::NoInput,
        ),
        Err(error) => stderr(
            &format!("Error starting run: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn cancel(args: &[OsString]) -> ExitCode {
    let usage = "Usage of run cancel:\n  -identity string\n    \tAuthor identity name\n  -reason string\n    \tReason for cancellation\n";
    let parsed = match parse_string_flags("run cancel", args, &["identity", "reason"], usage) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(run_id) = parsed.positionals.first() else {
        return stderr(
            "Usage: symroom run cancel <run_id> [--reason ...] [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    };
    let identity_name = match resolve_identity_name(parsed.values.get("identity")) {
        Ok(name) => name,
        Err(code) => return code,
    };
    let signer = match identity::load(&identity_name) {
        Ok(identity) => identity,
        Err(error) => return identity_error(&identity_name, error),
    };
    match runs::cancel(
        &room_dir(),
        run_id,
        parsed.values.get("reason").map_or("", String::as_str),
        &signer,
    ) {
        Ok(event) => stdout(format!("{}\n", event.id), CoreExitCode::Ok),
        Err(RunMutationError::InvalidTransition(error)) => stderr(
            &format!("Error: invalid run state transition: {error}\n"),
            CoreExitCode::NoInput,
        ),
        Err(error) => stderr(
            &format!("Error cancelling run: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

struct ParsedStringFlags {
    values: std::collections::BTreeMap<String, String>,
    positionals: Vec<String>,
}

fn parse_string_flags(
    command: &str,
    args: &[OsString],
    allowed: &[&str],
    usage: &str,
) -> Result<ParsedStringFlags, ExitCode> {
    let mut parsed = ParsedStringFlags {
        values: std::collections::BTreeMap::new(),
        positionals: Vec::new(),
    };
    let mut parsing_flags = true;
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if parsing_flags && argument == "--" {
            parsing_flags = false;
            index += 1;
            continue;
        }
        if parsing_flags && argument.starts_with('-') {
            let name_value = argument.trim_start_matches('-');
            let (name, inline_value) = name_value
                .split_once('=')
                .map_or((name_value, None), |(name, value)| (name, Some(value)));
            if is_help_flag(&argument) {
                return Err(stderr(usage, CoreExitCode::Ok));
            }
            if !allowed.contains(&name) {
                return Err(stderr(
                    &format!("flag provided but not defined: -{name}\n{usage}"),
                    CoreExitCode::NoInput,
                ));
            }
            let value = if let Some(value) = inline_value {
                value.to_owned()
            } else if let Some(value) = args.get(index + 1) {
                index += 1;
                value.to_string_lossy().into_owned()
            } else {
                return Err(stderr(
                    &format!("flag needs an argument: -{name}\n{usage}"),
                    CoreExitCode::NoInput,
                ));
            };
            parsed.values.insert(name.to_owned(), value);
            index += 1;
        } else {
            parsing_flags = false;
            parsed.positionals.push(argument.into_owned());
            index += 1;
        }
    }
    let _ = command;
    Ok(parsed)
}

fn resolve_identity_name(value: Option<&String>) -> Result<String, ExitCode> {
    match value.filter(|value| !value.is_empty()) {
        Some(name) => Ok(name.clone()),
        None => match crate::member_cli::default_identity() {
            Ok(name) if !name.is_empty() => Ok(name),
            Ok(_) => Err(stderr(
                "Error: --identity is required when default_identity is not configured\n",
                CoreExitCode::NoInput,
            )),
            Err(error) => Err(stderr(
                &format!("Error loading configuration: {error}\n"),
                CoreExitCode::NoInput,
            )),
        },
    }
}

fn identity_error(name: &str, error: symroom_core::identity::IdentityError) -> ExitCode {
    stderr(
        &format!("Error loading identity {name}: {error}\n"),
        CoreExitCode::NotFound,
    )
}

fn list(args: &[OsString]) -> ExitCode {
    let mut pending = false;
    let mut json = false;
    let mut parsing_flags = true;
    for argument in args {
        let value = argument.to_string_lossy();
        if parsing_flags && value == "--" {
            parsing_flags = false;
            continue;
        }
        if parsing_flags && value.starts_with('-') {
            match flag_value(&value, "pending") {
                Some(Ok(value)) => pending = value,
                Some(Err(invalid)) => {
                    return invalid_bool("run list", "pending", invalid, true, false);
                }
                None => match flag_value(&value, "json") {
                    Some(Ok(value)) => json = value,
                    Some(Err(invalid)) => {
                        return invalid_bool("run list", "json", invalid, true, false);
                    }
                    None if is_help_flag(&value) => return flag_help("run list", true, false),
                    None => return unknown_flag("run list", &value, true, false),
                },
            }
        } else {
            parsing_flags = false;
        }
    }

    let room = room_dir();
    let records = match runs::list(&room, pending) {
        Ok(records) => records,
        Err(error) => {
            return stderr(
                &format!("Error listing runs: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if json {
        let rendered = if records.is_empty() {
            "null".to_owned()
        } else {
            match pretty_go_json(&records) {
                Ok(rendered) => rendered,
                Err(_) => return process_exit(CoreExitCode::Generic),
            }
        };
        return stdout(format!("{rendered}\n"), CoreExitCode::Ok);
    }
    let mut output = String::new();
    for record in records {
        output.push_str(&format!(
            "{}\t[{}]\t{}\t{}\n",
            record.id, record.state, record.author, record.title
        ));
    }
    stdout(output, CoreExitCode::Ok)
}

fn show(args: &[OsString]) -> ExitCode {
    let mut json = false;
    let mut positional = Vec::new();
    let mut parsing_flags = true;
    for argument in args {
        let value = argument.to_string_lossy();
        if parsing_flags && value == "--" {
            parsing_flags = false;
            continue;
        }
        if parsing_flags && value.starts_with('-') {
            match flag_value(&value, "json") {
                Some(Ok(value)) => json = value,
                Some(Err(invalid)) => {
                    return invalid_bool("run show", "json", invalid, false, false);
                }
                None if is_help_flag(&value) => return flag_help("run show", false, false),
                None => return unknown_flag("run show", &value, false, false),
            }
        } else {
            parsing_flags = false;
            positional.push(value.into_owned());
        }
    }
    let Some(run_id) = positional.first() else {
        return stderr(
            "Usage: symroom run show <run_id> [--json]\n",
            CoreExitCode::NoInput,
        );
    };
    let record = match runs::get(&room_dir(), run_id) {
        Ok(record) => record,
        Err(RunQueryError::NotFound) => {
            return stderr(
                &format!("Error: run {run_id} not found\n"),
                CoreExitCode::NotFound,
            );
        }
        Err(error) => {
            return stderr(
                &format!("Error showing run: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if json {
        let rendered = match pretty_go_json(&record) {
            Ok(rendered) => rendered,
            Err(_) => return process_exit(CoreExitCode::Generic),
        };
        return stdout(format!("{rendered}\n"), CoreExitCode::Ok);
    }
    let mut output = format!(
        "Run ID:     {}\nTitle:      {}\nState:      {}\nAuthor:     {}\nCreated At: {}\n",
        record.id, record.title, record.state, record.author, record.created_at
    );
    if let Some(summary) = &record.summary {
        output.push_str(&format!("Summary:    {summary}\n"));
    }
    if let Some(error) = &record.error {
        output.push_str(&format!("Error:      {error}\n"));
    }
    stdout(output, CoreExitCode::Ok)
}

fn wait(args: &[OsString]) -> ExitCode {
    let mut timeout = Duration::from_secs(15 * 60);
    let mut json = false;
    let mut positional = Vec::new();
    let mut parsing_flags = true;
    let mut index = 0;
    while index < args.len() {
        let value = args[index].to_string_lossy();
        if parsing_flags && value == "--" {
            parsing_flags = false;
            index += 1;
            continue;
        }
        if parsing_flags && value.starts_with('-') {
            let flag = value.trim_start_matches('-');
            if flag == "timeout" {
                let Some(argument) = args.get(index + 1) else {
                    return flag_error("flag needs an argument: -timeout\n", true);
                };
                let duration = argument.to_string_lossy();
                timeout = match parse_go_duration(&duration) {
                    Some(timeout) => timeout,
                    None => return invalid_duration(&duration),
                };
                index += 2;
                continue;
            }
            if let Some(duration) = flag.strip_prefix("timeout=") {
                timeout = match parse_go_duration(duration) {
                    Some(timeout) => timeout,
                    None => return invalid_duration(duration),
                };
                index += 1;
                continue;
            }
            match flag_value(&value, "json") {
                Some(Ok(value)) => json = value,
                Some(Err(invalid)) => {
                    return invalid_bool("run wait", "json", invalid, false, true);
                }
                None if is_help_flag(&value) => {
                    return flag_help("run wait", false, true);
                }
                None => return unknown_flag("run wait", &value, false, true),
            }
            index += 1;
        } else {
            parsing_flags = false;
            positional.push(value.into_owned());
            index += 1;
        }
    }
    let Some(run_id) = positional.first() else {
        return stderr(
            "Usage: symroom run wait <run_id> [--timeout 15m] [--json]\n",
            CoreExitCode::NoInput,
        );
    };
    match runs::wait(&room_dir(), run_id, timeout) {
        Ok(record) if json => match pretty_go_json(&record) {
            Ok(rendered) => stdout(format!("{rendered}\n"), CoreExitCode::Ok),
            Err(_) => process_exit(CoreExitCode::Generic),
        },
        Ok(record) => stdout(
            format!(
                "Run {} approved [{}]\n",
                record.id,
                record.scope.as_deref().unwrap_or("")
            ),
            CoreExitCode::Ok,
        ),
        Err(RunWaitError::Timeout) => stderr(
            &format!("Error: wait timed out for run {run_id}\n"),
            CoreExitCode::Interrupted,
        ),
        Err(RunWaitError::Denied | RunWaitError::Cancelled) => stderr(
            &format!("Error: run {run_id} was denied\n"),
            CoreExitCode::Forbidden,
        ),
    }
}

fn invalid_duration(value: &str) -> ExitCode {
    let message = format!("invalid value \"{value}\" for flag -timeout: parse error\n");
    flag_error(&message, true)
}

fn flag_error(message: &str, timeout: bool) -> ExitCode {
    let usage = flag_usage("run wait", false, timeout);
    stderr(&format!("{message}{usage}"), CoreExitCode::NoInput)
}

fn parse_go_duration(input: &str) -> Option<Duration> {
    let (negative, input) = input
        .strip_prefix('-')
        .map_or((false, input), |rest| (true, rest));
    let input = input.strip_prefix('+').unwrap_or(input);
    if input == "0" {
        return Some(Duration::ZERO);
    }
    if input.is_empty() {
        return None;
    }
    let mut rest = input;
    let mut total_nanos = 0_u128;
    while !rest.is_empty() {
        let count = rest
            .bytes()
            .take_while(|b| b.is_ascii_digit() || *b == b'.')
            .count();
        if count == 0 {
            return None;
        }
        let amount = &rest[..count];
        rest = &rest[count..];
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
        .find(|(unit, _)| rest.starts_with(unit))?;
        let (whole, fraction) = amount
            .split_once('.')
            .map_or((amount, ""), |(whole, fraction)| (whole, fraction));
        if whole.is_empty() && fraction.is_empty()
            || !whole.bytes().all(|byte| byte.is_ascii_digit())
            || !fraction.bytes().all(|byte| byte.is_ascii_digit())
        {
            return None;
        }
        let whole = if whole.is_empty() {
            0
        } else {
            whole.parse::<u128>().ok()?
        };
        total_nanos = total_nanos.checked_add(whole.checked_mul(scale)?)?;
        let fractional_digits = fraction.len().min(19);
        if fractional_digits > 0 {
            let numerator = fraction[..fractional_digits].parse::<u128>().ok()?;
            let denominator = 10_u128.checked_pow(fractional_digits as u32)?;
            total_nanos = total_nanos.checked_add(numerator.checked_mul(scale)? / denominator)?;
        }
        rest = &rest[unit.len()..];
    }
    let maximum = i64::MAX as u128 + u128::from(negative);
    if total_nanos > maximum {
        return None;
    }
    Some(if negative || total_nanos == 0 {
        Duration::ZERO
    } else {
        Duration::from_nanos(total_nanos as u64)
    })
}

fn flag_value<'a>(argument: &'a str, name: &str) -> Option<Result<bool, &'a str>> {
    let trimmed = argument.trim_start_matches('-');
    if trimmed == name {
        return Some(Ok(true));
    }
    let value = trimmed.strip_prefix(&format!("{name}="))?;
    Some(match value {
        "1" | "t" | "T" | "TRUE" | "True" | "true" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "False" | "false" => Ok(false),
        _ => Err(value),
    })
}

fn is_help_flag(argument: &str) -> bool {
    matches!(argument.trim_start_matches('-'), "h" | "help")
}

fn flag_help(command: &str, pending: bool, timeout: bool) -> ExitCode {
    stderr(&flag_usage(command, pending, timeout), CoreExitCode::Ok)
}

fn flag_usage(command: &str, pending: bool, timeout: bool) -> String {
    let mut usage = format!("Usage of {command}:\n  -json\n    \tOutput as JSON\n");
    if pending {
        usage.push_str("  -pending\n    \tShow pending runs only\n");
    }
    if timeout {
        usage.push_str("  -timeout duration\n    \tTimeout duration (default 15m0s)\n");
    }
    usage
}

fn invalid_bool(command: &str, name: &str, value: &str, pending: bool, timeout: bool) -> ExitCode {
    let usage = flag_usage(command, pending, timeout);
    stderr(
        &format!("invalid boolean value \"{value}\" for -{name}: parse error\n{usage}"),
        CoreExitCode::NoInput,
    )
}

fn unknown_flag(command: &str, argument: &str, pending_flag: bool, timeout: bool) -> ExitCode {
    let name = argument.trim_start_matches('-');
    let usage = flag_usage(command, pending_flag, timeout);
    stderr(
        &format!("flag provided but not defined: -{name}\n{usage}"),
        CoreExitCode::NoInput,
    )
}

fn pretty_go_json(value: &impl serde::Serialize) -> Result<String, serde_json::Error> {
    let json = serde_json::to_string_pretty(value)?;
    Ok(json
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029"))
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn stdout(value: String, code: CoreExitCode) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(code)
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        return process_exit(CoreExitCode::Generic);
    }
    process_exit(code)
}

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}
