use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
    time::Duration,
};

use crate::goflag::parse_go_duration;
use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{
    approval, identity,
    runs::{self, RunMutationError, RunQueryError, RunWaitError},
};

const USAGE: &str = "Usage: symroom run <request|list|show|start|cancel> [flags] [args]\n";

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
        "approve" => approve(&args[1..]),
        "deny" => deny(&args[1..]),
        action => stderr(
            &format!("Unknown run action: {action}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn approve(args: &[OsString]) -> ExitCode {
    const USAGE: &str =
        "Usage: symroom run approve <run_id> [--scope ...] [--ttl 30m] [--identity <name>]\n";
    const FLAGS: &str = "Usage of run approve:\n  -identity string\n    \tAuthor identity name\n  -scope string\n    \tApproval scope (default \"all\")\n  -ttl duration\n    \tApproval TTL duration (default 30m0s)\n";
    let parsed = match parse_string_flags("run approve", args, &["identity", "scope", "ttl"], FLAGS)
    {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(run_id) = parsed.positionals.first() else {
        return stderr(USAGE, CoreExitCode::NoInput);
    };
    let identity_name = match resolve_identity_name(parsed.values.get("identity")) {
        Ok(name) => name,
        Err(code) => return code,
    };
    let signer = match identity::load(&identity_name) {
        Ok(identity) => identity,
        Err(error) => return identity_error(&identity_name, error),
    };
    let ttl = match parsed.values.get("ttl") {
        Some(value) => match parse_signed_duration(value) {
            Some(ttl) => ttl,
            None => {
                return stderr(
                    &format!("invalid value \"{value}\" for flag -ttl: parse error\n{FLAGS}"),
                    CoreExitCode::NoInput,
                );
            }
        },
        None => time::Duration::minutes(30),
    };
    let ttl = if ttl.is_zero() {
        configured_approval_ttl().unwrap_or_else(|| time::Duration::minutes(30))
    } else {
        ttl
    };
    match approval::approve(
        &room_dir(),
        run_id,
        parsed.values.get("scope").map_or("all", String::as_str),
        ttl,
        &signer,
    ) {
        Ok(event) => stdout(format!("{}\n", event.id), CoreExitCode::Ok),
        Err(approval::ApprovalError::AgentForbidden) => stderr(
            "Error: agent identity is forbidden from approving runs\n",
            CoreExitCode::NoInput,
        ),
        Err(error) => stderr(
            &format!("Error approving run: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn deny(args: &[OsString]) -> ExitCode {
    const USAGE: &str = "Usage: symroom run deny <run_id> --reason ... [--identity <name>]\n";
    let parsed = match parse_string_flags(
        "run deny",
        args,
        &["identity", "reason"],
        "Usage of run deny:\n  -identity string\n    \tAuthor identity name\n  -reason string\n    \tReason for denial\n",
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(run_id) = parsed.positionals.first() else {
        return stderr(USAGE, CoreExitCode::NoInput);
    };
    let Some(reason) = parsed
        .values
        .get("reason")
        .filter(|reason| !reason.is_empty())
    else {
        return stderr(USAGE, CoreExitCode::NoInput);
    };
    let identity_name = match resolve_identity_name(parsed.values.get("identity")) {
        Ok(name) => name,
        Err(code) => return code,
    };
    let signer = match identity::load(&identity_name) {
        Ok(identity) => identity,
        Err(error) => return identity_error(&identity_name, error),
    };
    match approval::deny(&room_dir(), run_id, reason, &signer) {
        Ok(event) => stdout(format!("{}\n", event.id), CoreExitCode::Ok),
        Err(error) => stderr(
            &format!("Error denying run: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn parse_signed_duration(input: &str) -> Option<time::Duration> {
    let negative = input.starts_with('-');
    let positive = if negative {
        &input[1..]
    } else {
        input.strip_prefix('+').unwrap_or(input)
    };
    if positive.starts_with(['-', '+']) {
        return None;
    }
    let duration = parse_go_duration(positive)?;
    let nanos = i128::try_from(duration.as_nanos()).ok()?;
    let nanos = if negative {
        nanos.checked_neg()?
    } else {
        nanos
    };
    Some(time::Duration::nanoseconds(i64::try_from(nanos).ok()?))
}

fn configured_approval_ttl() -> Option<time::Duration> {
    let mut value = None;
    let home = std::env::var_os("HOME").map(PathBuf::from)?;
    for path in [
        home.join(".config/symroom/config.toml"),
        std::env::current_dir().ok()?.join(".symroom.toml"),
    ] {
        let Ok(contents) = std::fs::read_to_string(path) else {
            continue;
        };
        let Ok(config) = toml::from_str::<toml::Value>(&contents) else {
            continue;
        };
        if let Some(ttl) = config
            .get("approval")
            .and_then(|approval| approval.get("default_ttl"))
            .and_then(toml::Value::as_str)
        {
            value = Some(ttl.to_owned());
        }
    }
    if let Ok(ttl) = std::env::var("SYMROOM_APPROVAL_DEFAULT_TTL")
        && !ttl.is_empty()
    {
        value = Some(ttl);
    }
    value
        .as_deref()
        .and_then(parse_signed_duration)
        .filter(|duration| !duration.is_zero())
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
    use crate::goflag::{Spec, check_go_duration};
    let _ = command;
    let specs: Vec<Spec> = allowed
        .iter()
        .map(|name| {
            if *name == "ttl" {
                Spec::checked(name, check_go_duration)
            } else {
                Spec::string(name)
            }
        })
        .collect();
    let parsed = crate::goflag::parse_or_exit(args, &specs, usage)?;
    Ok(ParsedStringFlags {
        values: parsed.values,
        positionals: parsed.positionals,
    })
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
    use crate::goflag::Spec;
    let usage = flag_usage("run list", true, false);
    let parsed = match crate::goflag::parse_or_exit(
        args,
        &[Spec::boolean("pending"), Spec::boolean("json")],
        &usage,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let pending = parsed.boolean("pending");
    let json = parsed.boolean("json");

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
    use crate::goflag::Spec;
    let usage = flag_usage("run show", false, false);
    let parsed = match crate::goflag::parse_or_exit(args, &[Spec::boolean("json")], &usage) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let json = parsed.boolean("json");
    let positional = parsed.positionals;
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
    use crate::goflag::{Spec, check_go_duration};
    let usage = flag_usage("run wait", false, true);
    let parsed = match crate::goflag::parse_or_exit(
        args,
        &[
            Spec::checked("timeout", check_go_duration),
            Spec::boolean("json"),
        ],
        &usage,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let json = parsed.boolean("json");
    let timeout = parsed
        .values
        .get("timeout")
        .map_or(Duration::from_secs(15 * 60), |value| {
            parse_go_duration(value).unwrap_or_default()
        });
    let positional = parsed.positionals;
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
