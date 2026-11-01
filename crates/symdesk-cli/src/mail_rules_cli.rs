//! `symdesk mail rules list|create|update|delete` (Go `cmd/symdesk/mail.go`,
//! `internal/ingest/api.go`, `internal/ingest/internal/config/mail_contract.go`):
//! the IMAP account section of the symingest configuration.
use std::{io::Read, path::Path, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde::{Deserialize, Serialize};
use symdesk_vault::go_quote;

use crate::{
    emit_error,
    ingest_common::{SCHEMA_VERSION, go_abs, go_join},
    write_go_json, write_stdout,
};

pub fn cli() -> Command {
    let leaf = |name: &'static str| Command::new(name).arg(Arg::new("args").num_args(0..));
    Command::new("rules")
        .arg(Arg::new("config").long("config").num_args(1).global(true))
        .subcommand(leaf("list"))
        .subcommand(leaf("create"))
        .subcommand(leaf("update"))
        .subcommand(leaf("delete"))
}

/// Go `config.IMAPAccount`.
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub(crate) struct ImapAccount {
    pub(crate) host: String,
    pub(crate) port: i64,
    pub(crate) username: String,
    pub(crate) password_secret: String,
    pub(crate) folder: String,
    pub(crate) from: Vec<String>,
    pub(crate) subject: Vec<String>,
    pub(crate) has_attachment: bool,
    pub(crate) action: String,
    pub(crate) move_to: String,
    pub(crate) archive_mail: bool,
}

/// Go `ingest.MailAccountRecord` (stdin payload).
#[derive(Default, Deserialize)]
#[serde(default)]
struct AccountRecord {
    host: String,
    port: i64,
    username: String,
    password_secret: String,
    folder: String,
    from: Vec<String>,
    subject: Vec<String>,
    has_attachment: bool,
    action: String,
    move_to: String,
    archive_mail: bool,
}

impl From<AccountRecord> for ImapAccount {
    fn from(a: AccountRecord) -> Self {
        Self {
            host: a.host,
            port: a.port,
            username: a.username,
            password_secret: a.password_secret,
            folder: a.folder,
            from: a.from,
            subject: a.subject,
            has_attachment: a.has_attachment,
            action: a.action,
            move_to: a.move_to,
            archive_mail: a.archive_mail,
        }
    }
}

#[derive(Serialize)]
struct AccountView {
    id: String,
    host: String,
    port: i64,
    username: String,
    password_secret: String,
    folder: String,
    from: Vec<String>,
    subject: Vec<String>,
    has_attachment: bool,
    action: String,
    move_to: String,
    archive_mail: bool,
}

struct Document {
    accounts: Vec<ImapAccount>,
    poll_interval: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct MailFile {
    imap_accounts: Vec<ImapAccount>,
    imap_poll_interval: String,
}

pub(crate) fn account_id(account: &ImapAccount) -> String {
    let folder = if account.folder.is_empty() {
        "INBOX"
    } else {
        &account.folder
    };
    format!(
        "{}@{}:{}/{}",
        account.username, account.host, account.port, folder
    )
}

fn secret_kind(value: &str) -> &'static str {
    let value = value.trim();
    if value.is_empty() {
        return "missing";
    }
    let lower = symdesk_vault::go_lowercase(value);
    if ["symvault://", "env://", "keychain://"]
        .iter()
        .any(|prefix| lower.starts_with(prefix))
    {
        "reference"
    } else {
        "plaintext"
    }
}

fn mask(value: &str) -> String {
    match secret_kind(value) {
        "missing" => String::new(),
        "reference" => value.to_owned(),
        _ => "<redacted>".to_owned(),
    }
}

fn view(account: &ImapAccount) -> AccountView {
    AccountView {
        id: account_id(account),
        host: account.host.clone(),
        port: account.port,
        username: account.username.clone(),
        password_secret: mask(&account.password_secret),
        folder: account.folder.clone(),
        from: account.from.clone(),
        subject: account.subject.clone(),
        has_attachment: account.has_attachment,
        action: account.action.clone(),
        move_to: account.move_to.clone(),
        archive_mail: account.archive_mail,
    }
}

/// Go `config.ConfigPath`.
fn config_path(explicit: &str) -> Result<String, String> {
    if !explicit.trim().is_empty() {
        return go_abs(explicit).map_err(|error| format!("resolve config path: {error}"));
    }
    let cwd = std::env::current_dir()
        .map_err(|error| format!("determine current directory: {error}"))?
        .to_string_lossy()
        .into_owned();
    let project = go_join(&[&cwd, ".symingest.toml"]);
    match std::fs::metadata(&project) {
        Ok(_) => return Ok(project),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => {
            return Err(format!(
                "stat project config: {}",
                symdesk_vault::go_path_error("stat", Path::new(&project), &error)
            ));
        }
    }
    let environment = symdesk_core::config::environment_snapshot()?;
    let xdg = environment
        .get("XDG_CONFIG_HOME")
        .map(String::as_str)
        .unwrap_or_default();
    if xdg.starts_with('/') {
        return Ok(go_join(&[xdg, "symingest", "config.toml"]));
    }
    let home = environment
        .get("HOME")
        .map(String::as_str)
        .filter(|home| !home.is_empty());
    Ok(match home {
        Some(home) => go_join(&[home, ".config", "symingest", "config.toml"]),
        None => go_join(&[".config", "symingest", "config.toml"]),
    })
}

enum ReadError {
    NotFound(String),
    Other(String),
}

/// Go `config.ReadMailConfig`.
fn read_config(path: &str) -> Result<Document, ReadError> {
    let path =
        go_abs(path).map_err(|error| ReadError::Other(format!("resolve config path: {error}")))?;
    if let Err(error) = std::fs::metadata(&path) {
        return Err(if error.kind() == std::io::ErrorKind::NotFound {
            ReadError::NotFound(format!("symingest configuration file not found: {path}"))
        } else {
            ReadError::Other(format!(
                "stat config: {}",
                symdesk_vault::go_path_error("stat", Path::new(&path), &error)
            ))
        });
    }
    let data = symdesk_vault::go_read(Path::new(&path)).map_err(|(operation, error)| {
        ReadError::Other(format!(
            "parse config {path}: {}",
            symdesk_vault::go_path_error(operation, Path::new(&path), &error)
        ))
    })?;
    let text = String::from_utf8_lossy(&data);
    let file: MailFile = toml::from_str(&text).map_err(|error| {
        ReadError::Other(format!(
            "parse config {path}: {}",
            crate::ingest_common::go_toml_error_text(&error.to_string())
        ))
    })?;
    Ok(Document {
        accounts: file.imap_accounts,
        poll_interval: file.imap_poll_interval,
    })
}

fn read_lenient(path: &str) -> Result<Document, String> {
    match read_config(path) {
        Ok(document) => Ok(document),
        Err(ReadError::NotFound(_)) => Ok(Document {
            accounts: Vec::new(),
            poll_interval: String::new(),
        }),
        Err(ReadError::Other(error)) => Err(format!("read mail configuration: {error}")),
    }
}

fn read_strict(path: &str) -> Result<Document, String> {
    read_config(path).map_err(|error| match error {
        ReadError::NotFound(message) | ReadError::Other(message) => {
            format!("read mail configuration: {message}")
        }
    })
}

struct Validation {
    errors: Vec<String>,
    warnings: Vec<String>,
}

/// Go `config.ValidateMailAccounts`.
fn validate(accounts: &[ImapAccount], poll_interval: &str) -> Validation {
    let mut report = Validation {
        errors: Vec::new(),
        warnings: Vec::new(),
    };
    if !poll_interval.is_empty()
        && crate::index_cli::parse_go_duration_nanos(poll_interval).is_err()
    {
        report.errors.push(format!(
            "imap_poll_interval: invalid duration {}",
            go_quote(poll_interval)
        ));
    }
    for (index, account) in accounts.iter().enumerate() {
        let prefix = format!("accounts[{index}]");
        if account.host.trim().is_empty() {
            report.errors.push(format!("{prefix}.host is required"));
        }
        if account.port < 1 || account.port > 65535 {
            report
                .errors
                .push(format!("{prefix}.port must be between 1 and 65535"));
        }
        if account.username.trim().is_empty() {
            report.errors.push(format!("{prefix}.username is required"));
        }
        if account.password_secret.trim().is_empty() {
            report
                .errors
                .push(format!("{prefix}.password_secret is required"));
        } else if secret_kind(&account.password_secret) == "plaintext" {
            report.warnings.push(format!(
                "{prefix}.password_secret is plaintext; use a secret reference"
            ));
        }
        if !account.action.is_empty() && account.action != "mark_seen" && account.action != "move" {
            report
                .errors
                .push(format!("{prefix}.action must be mark_seen or move"));
        }
        if account.action == "move" && account.move_to.trim().is_empty() {
            report
                .errors
                .push(format!("{prefix}.move_to is required when action is move"));
        }
    }
    report
}

fn toml_strings(key: &str, values: &[String]) -> String {
    let items: Vec<String> = values.iter().map(|value| go_quote(value)).collect();
    format!("{key} = [{}]\n", items.join(", "))
}

fn render_section(accounts: &[ImapAccount]) -> String {
    let mut out = String::new();
    for account in accounts {
        out.push_str("[[imap_accounts]]\n");
        out.push_str(&format!("host = {}\n", go_quote(&account.host)));
        out.push_str(&format!("port = {}\n", account.port));
        out.push_str(&format!("username = {}\n", go_quote(&account.username)));
        out.push_str(&format!(
            "password_secret = {}\n",
            go_quote(&account.password_secret)
        ));
        if !account.folder.is_empty() {
            out.push_str(&format!("folder = {}\n", go_quote(&account.folder)));
        }
        out.push_str(&toml_strings("from", &account.from));
        out.push_str(&toml_strings("subject", &account.subject));
        out.push_str(&format!("has_attachment = {}\n", account.has_attachment));
        if !account.action.is_empty() {
            out.push_str(&format!("action = {}\n", go_quote(&account.action)));
        }
        if !account.move_to.is_empty() {
            out.push_str(&format!("move_to = {}\n", go_quote(&account.move_to)));
        }
        out.push_str(&format!("archive_mail = {}\n\n", account.archive_mail));
    }
    out
}

/// Go `strings.SplitAfter(s, "\n")`.
fn split_after_newline(text: &str) -> Vec<String> {
    let mut lines: Vec<String> = text.split_inclusive('\n').map(str::to_owned).collect();
    if text.is_empty() || text.ends_with('\n') {
        lines.push(String::new());
    }
    lines
}

fn replace_root_poll_interval(content: &str, interval: &str) -> String {
    let mut lines = split_after_newline(content);
    let mut in_array = false;
    let replacement = format!("imap_poll_interval = {}\n", go_quote(interval));
    for line in &mut lines {
        let trimmed = line.trim().to_owned();
        if trimmed.starts_with("[[") {
            in_array = true;
        }
        if in_array && trimmed.starts_with('[') && !trimmed.starts_with("[[imap_accounts]]") {
            in_array = false;
        }
        if !in_array && trimmed.starts_with("imap_poll_interval") {
            *line = replacement.clone();
            return lines.concat();
        }
    }
    format!("{replacement}{content}")
}

fn replace_section(original: &str, accounts: &[ImapAccount], poll_interval: &str) -> String {
    let section = render_section(accounts);
    let lines = split_after_newline(original);
    let (mut start, mut end) = (None, None);
    for (index, line) in lines.iter().enumerate() {
        let trimmed = line.trim();
        if trimmed == "[[imap_accounts]]" {
            if start.is_none() {
                start = Some(index);
            }
            continue;
        }
        if let Some(first) = start
            && index > first
            && trimmed.starts_with('[')
            && !trimmed.starts_with("[[imap_accounts]]")
        {
            end = Some(index);
            break;
        }
    }
    let mut content = match start {
        None => {
            let mut prefix = original.to_owned();
            if !prefix.is_empty() && !prefix.ends_with('\n') {
                prefix.push('\n');
            }
            prefix + &section
        }
        Some(first) => {
            let end = end.unwrap_or(lines.len());
            let mut joined = lines[..first].concat();
            joined.push_str(&section);
            joined.push_str(&lines[end..].concat());
            joined
        }
    };
    if !poll_interval.is_empty() {
        content = replace_root_poll_interval(&content, poll_interval);
    }
    content
}

/// Go `config.WriteMailConfig`.
fn write_config(path: &str, accounts: &[ImapAccount], poll_interval: &str) -> Result<(), String> {
    let path = go_abs(path).map_err(|error| format!("resolve config path: {error}"))?;
    let report = validate(accounts, poll_interval);
    if !report.errors.is_empty() {
        return Err(format!(
            "invalid mail configuration: {}",
            report.errors.join("; ")
        ));
    }
    let mut original = String::new();
    let mut mode = 0o600;
    match std::fs::metadata(&path) {
        Ok(info) => {
            if !info.is_file() {
                return Err(format!("config path is not a regular file: {path}"));
            }
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                mode = info.permissions().mode() & 0o777;
            }
            let data = symdesk_vault::go_read(Path::new(&path)).map_err(|(_, error)| {
                format!(
                    "read config: {}",
                    symdesk_vault::go_path_error("open", Path::new(&path), &error)
                )
            })?;
            original = String::from_utf8_lossy(&data).into_owned();
            if let Err(error) = toml::from_str::<toml::Table>(&original) {
                return Err(format!(
                    "parse config {path}: {}",
                    crate::ingest_common::go_toml_error_text(&error.to_string())
                ));
            }
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => {
            return Err(format!(
                "stat config: {}",
                symdesk_vault::go_path_error("stat", Path::new(&path), &error)
            ));
        }
    }
    let updated = replace_section(&original, accounts, poll_interval);
    let parent = Path::new(&path).parent().unwrap_or(Path::new("."));
    {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(parent)
            .map_err(|error| {
                format!(
                    "create config directory: {}",
                    symdesk_vault::go_path_error("mkdir", parent, &error)
                )
            })?;
    }
    symdesk_ingest::writer::atomic_write_file(&path, updated.as_bytes(), mode)
        .map_err(|error| format!("write config atomically: {error}"))
}

struct Outcome {
    path: String,
    accounts: Vec<ImapAccount>,
    reload_required: bool,
    warnings: Vec<String>,
}

/// Go `writeAccountsAt`.
fn write_accounts(
    path: &str,
    accounts: Vec<ImapAccount>,
    poll_interval: &str,
) -> Result<Outcome, String> {
    let report = validate(&accounts, poll_interval);
    if !report.errors.is_empty() {
        return Err(format!(
            "invalid mail configuration: {}",
            report.errors.join("; ")
        ));
    }
    write_config(path, &accounts, poll_interval)
        .map_err(|error| format!("write mail configuration: {error}"))?;
    Ok(Outcome {
        path: path.to_owned(),
        accounts,
        reload_required: true,
        warnings: report.warnings,
    })
}

fn render(operation: &str, outcome: &Outcome, json: bool) -> ExitCode {
    if json {
        let accounts: Vec<AccountView> = outcome.accounts.iter().map(view).collect();
        // Go writes this through a json.Encoder (trailing newline, HTML escaping).
        return write_go_json(&serde_json::json!({
            "schema_version": SCHEMA_VERSION,
            "operation": operation,
            "config_path": outcome.path,
            "accounts": accounts,
            "reload_required": outcome.reload_required,
            "warnings": outcome.warnings,
        }));
    }
    let mut out = format!(
        "{operation}: {} account(s) at {}\n",
        outcome.accounts.len(),
        outcome.path
    );
    for warning in &outcome.warnings {
        out.push_str(&format!("  warning: {warning}\n"));
    }
    for account in &outcome.accounts {
        out.push_str(&format!(
            "  {}\t{}@{}:{}/{}\n",
            account_id(account),
            account.username,
            account.host,
            account.port,
            account.folder
        ));
    }
    write_stdout(out)
}

/// Go's `json.NewDecoder(r).Decode(&v)` syntax errors for the common cases.
fn go_json_error(text: &str, serde_message: &str) -> String {
    let trimmed = text.trim_start_matches([' ', '\t', '\r', '\n']);
    let Some(first) = trimmed.chars().next() else {
        return "EOF".to_owned();
    };
    let literal = |word: &str| -> Option<String> {
        let mut seen = trimmed.chars();
        let mut matched = 0;
        for want in word.chars() {
            match seen.next() {
                None => return Some("unexpected EOF".to_owned()),
                Some(got) if got == want => matched += 1,
                Some(got) => {
                    return Some(format!(
                        "invalid character {} in literal {word} (expecting {})",
                        go_quote_rune(got),
                        go_quote_rune(want)
                    ));
                }
            }
        }
        let _ = matched;
        None
    };
    match first {
        'n' => literal("null"),
        't' => literal("true"),
        'f' => literal("false"),
        '{' | '[' | '"' | '-' | '0'..='9' => None,
        other => Some(format!(
            "invalid character {} looking for beginning of value",
            go_quote_rune(other)
        )),
    }
    .unwrap_or_else(|| {
        if serde_message.contains("EOF while parsing") {
            "unexpected EOF".to_owned()
        } else {
            serde_message.to_owned()
        }
    })
}

fn go_quote_rune(ch: char) -> String {
    let quoted = go_quote(&ch.to_string());
    let inner = &quoted[1..quoted.len() - 1];
    if ch == '\'' {
        "'\\''".to_owned()
    } else if ch == '"' {
        "'\"'".to_owned()
    } else {
        format!("'{inner}'")
    }
}

fn read_stdin_account() -> Result<AccountRecord, String> {
    let mut input = Vec::new();
    std::io::stdin()
        .read_to_end(&mut input)
        .map_err(|error| format!("read account JSON from stdin: {error}"))?;
    let text = String::from_utf8_lossy(&input).into_owned();
    let mut stream = serde_json::Deserializer::from_str(&text).into_iter::<AccountRecord>();
    match stream.next() {
        Some(Ok(account)) => Ok(account),
        Some(Err(error)) => Err(format!(
            "read account JSON from stdin: {}",
            go_json_error(&text, &error.to_string())
        )),
        None => Err("read account JSON from stdin: EOF".to_owned()),
    }
}

fn index_of(accounts: &[ImapAccount], id: &str) -> Option<usize> {
    accounts
        .iter()
        .position(|account| account_id(account) == id)
}

pub fn run(command: &ArgMatches, json: bool) -> ExitCode {
    let Some((name, sub)) = command.subcommand() else {
        return crate::process_exit(symaira_core_exit::ExitCode::Ok);
    };
    let explicit = command
        .get_one::<String>("config")
        .or_else(|| sub.get_one::<String>("config"))
        .cloned()
        .unwrap_or_default();
    let args: Vec<String> = sub
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .cloned()
        .collect();
    match execute(name, &args, &explicit, json) {
        Ok(code) => code,
        Err(error) => emit_error(error, json),
    }
}

fn execute(name: &str, args: &[String], explicit: &str, json: bool) -> Result<ExitCode, String> {
    match name {
        "list" => {
            let path =
                config_path(explicit).map_err(|e| format!("resolve mail config path: {e}"))?;
            let document = read_lenient(&path)?;
            Ok(render(
                "list",
                &Outcome {
                    path,
                    accounts: document.accounts,
                    reload_required: false,
                    warnings: Vec::new(),
                },
                json,
            ))
        }
        "create" => {
            let account: ImapAccount = read_stdin_account()?.into();
            let path =
                config_path(explicit).map_err(|e| format!("resolve mail config path: {e}"))?;
            let mut document = read_lenient(&path)?;
            document.accounts.push(account);
            let outcome = write_accounts(&path, document.accounts, &document.poll_interval)?;
            Ok(render("create", &outcome, json))
        }
        "update" => {
            let account: ImapAccount = read_stdin_account()?.into();
            let path =
                config_path(explicit).map_err(|e| format!("resolve mail config path: {e}"))?;
            let mut document = read_strict(&path)?;
            let id = &args[0];
            let Some(index) = index_of(&document.accounts, id) else {
                return Err(format!(
                    "no configured mail account with ID {}",
                    go_quote(id)
                ));
            };
            let existing = &document.accounts[index];
            let mut updated = account;
            if updated.password_secret.is_empty()
                || updated.password_secret == mask(&existing.password_secret)
            {
                updated.password_secret = existing.password_secret.clone();
            }
            document.accounts[index] = updated;
            let outcome = write_accounts(&path, document.accounts, &document.poll_interval)?;
            Ok(render("update", &outcome, json))
        }
        _ => {
            let path =
                config_path(explicit).map_err(|e| format!("resolve mail config path: {e}"))?;
            let mut document = read_strict(&path)?;
            let id = &args[0];
            let Some(index) = index_of(&document.accounts, id) else {
                return Err(format!(
                    "no configured mail account with ID {}",
                    go_quote(id)
                ));
            };
            document.accounts.remove(index);
            let outcome = write_accounts(&path, document.accounts, &document.poll_interval)?;
            Ok(render("delete", &outcome, json))
        }
    }
}

impl From<&ImapAccount> for symdesk_ingest::mail::ImapAccount {
    fn from(a: &ImapAccount) -> Self {
        Self {
            host: a.host.clone(),
            port: a.port,
            username: a.username.clone(),
            password_secret: a.password_secret.clone(),
            folder: a.folder.clone(),
            from: a.from.clone(),
            subject: a.subject.clone(),
            has_attachment: a.has_attachment,
            action: a.action.clone(),
            move_to: a.move_to.clone(),
            archive_mail: a.archive_mail,
        }
    }
}

/// Go `config.MailConfigPath("")`: the global symingest configuration file.
pub(crate) fn global_config_path() -> Result<String, String> {
    let environment = symdesk_core::config::environment_snapshot()?;
    let xdg = environment
        .get("XDG_CONFIG_HOME")
        .map(String::as_str)
        .unwrap_or_default();
    if xdg.starts_with('/') {
        return Ok(go_join(&[xdg, "symingest", "config.toml"]));
    }
    let home = environment
        .get("HOME")
        .map(String::as_str)
        .filter(|home| !home.is_empty());
    Ok(match home {
        Some(home) => go_join(&[home, ".config", "symingest", "config.toml"]),
        None => go_join(&[".config", "symingest", "config.toml"]),
    })
}

/// Go `ingest.MailAccounts` / the account list of `config.ReadMailConfig`:
/// `None` when the file does not exist.
pub(crate) fn read_accounts(path: &str) -> Result<Option<Vec<ImapAccount>>, String> {
    match read_config(path) {
        Ok(document) => Ok(Some(document.accounts)),
        Err(ReadError::NotFound(_)) => Ok(None),
        Err(ReadError::Other(error)) => Err(format!("read mail configuration: {error}")),
    }
}

/// Go `config.ConfigPath(explicit)` for a non-empty explicit path.
pub(crate) fn absolute_config_path(explicit: &str) -> Result<String, String> {
    config_path(explicit)
}
