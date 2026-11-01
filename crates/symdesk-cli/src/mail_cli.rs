//! `symdesk mail status` and `mail fetch` (Go `cmd/symdesk/mail.go`,
//! `internal/mail/mail.go`, `internal/ingest/api.go` `FetchMail`): the
//! one-shot view of the configured IMAP accounts and one fetch cycle.
use std::{
    collections::BTreeMap,
    path::Path,
    process::ExitCode,
    sync::{Arc, Mutex},
};

use clap::{Arg, Command};
use serde::Serialize;
use symdesk_ingest::mail::{MailPoller, MailPollerOptions};
use symdesk_vault::go_quote;
use time::OffsetDateTime;

use crate::{
    emit_error,
    ingest_cli::{init_service_deps, service_ingest},
    ingest_common::{go_join, go_log, open_store, resolve},
    mail_rules_cli::{
        ImapAccount, absolute_config_path, account_id, global_config_path, read_accounts,
    },
    write_stdout,
};

pub fn status_cli() -> Command {
    Command::new("status").arg(Arg::new("extra").num_args(0..).hide(true))
}

pub fn fetch_cli() -> Command {
    Command::new("fetch").arg(Arg::new("extra").num_args(0..).hide(true))
}

#[derive(Serialize)]
struct AccountInfo {
    id: String,
    host: String,
    username: String,
}

#[derive(Clone, Serialize)]
struct Status {
    account_id: String,
    host: String,
    username: String,
    last_run: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    last_error: String,
    messages_total: i64,
    last_run_count: i64,
}

fn now_text() -> String {
    let now = OffsetDateTime::now_utc();
    let offset = crate::local_offset_at(now);
    symdesk_ingest::writer::rfc3339_nano(now.to_offset(offset))
}

/// Go `MailWatcher.listAccounts`.
fn list_accounts(config_path: &str) -> Result<Vec<AccountInfo>, String> {
    let path = absolute_config_path(config_path)
        .map_err(|e| format!("read mail accounts: resolve mail config path: {e}"))?;
    let accounts = read_accounts(&path)
        .map_err(|e| format!("read mail accounts: {e}"))?
        .unwrap_or_default();
    Ok(accounts
        .iter()
        .map(|account| AccountInfo {
            id: account_id(account),
            host: account.host.clone(),
            username: account.username.clone(),
        })
        .collect())
}

fn print_indented<T: Serialize>(value: &T) -> ExitCode {
    match serde_json::to_string_pretty(value) {
        Ok(text) => write_stdout(format!("{}\n", crate::go_escape_json(text))),
        Err(error) => emit_error(error.to_string(), false),
    }
}

pub fn run(sub: &str, vault: Option<&str>, json: bool) -> ExitCode {
    let outcome = if sub == "status" {
        status(vault)
    } else {
        fetch(vault)
    };
    outcome.unwrap_or_else(|error| emit_error(error, json))
}

fn close_sidecar(mut sidecar: symdesk_index::Sidecar) {
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
}

fn status(vault: Option<&str>) -> Result<ExitCode, String> {
    let (_root, sidecar) = init_service_deps(vault)?;
    let outcome = (|| {
        let config_path =
            global_config_path().map_err(|e| format!("resolve mail config path: {e}"))?;
        let accounts = list_accounts(&config_path)
            .map_err(|e| format!("failed to list mail accounts: {e}"))?;
        Ok::<_, String>(accounts)
    })();
    close_sidecar(sidecar);
    let accounts = outcome?;
    Ok(print_indented(&serde_json::json!({
        "accounts": accounts,
        "statuses": Vec::<Status>::new(),
    })))
}

/// Go `mail.redactCredentials`.
fn redact_credentials(text: &str) -> String {
    let mut s = text.to_owned();
    loop {
        let lower = s.to_lowercase();
        let Some(index) = lower.find("symvault://") else {
            break;
        };
        let rest = &s[index..];
        match rest.find([' ', '\t', '\n', '\r', '"', '\'', ')', '\\']) {
            None => {
                s = format!("{}<redacted>", &s[..index]);
                break;
            }
            Some(end) => s = format!("{}<redacted>{}", &s[..index], &s[index + end..]),
        }
    }
    for (prefix, suffix) in [
        ("password=\"", "\""),
        ("password: \"", "\""),
        ("password_secret=\"", "\""),
        ("password_secret: \"", "\""),
    ] {
        let mut search_from = 0;
        while let Some(found) = s[search_from..].find(prefix) {
            let start = search_from + found + prefix.len();
            match s[start..].find(suffix) {
                None => {
                    s = format!("{}<redacted>{}", &s[..start], &s[start..]);
                    break;
                }
                Some(end) => {
                    s = format!("{}<redacted>{}", &s[..start], &s[start + end..]);
                    search_from = start + "<redacted>".len();
                }
            }
        }
    }
    s
}

struct Attachment {
    path: String,
}

/// Go `FetchMail` for one account: the staged attachments, or the account's
/// failure reason.
fn fetch_account(
    config_path: &str,
    account: &str,
    staging: &str,
) -> Result<Vec<Attachment>, String> {
    let wrap = |e: String| format!("mail fetch failed: {e}");
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode_700()
        .create(staging)
        .map_err(|e| {
            wrap(format!(
                "create mail staging directory: {}",
                symdesk_vault::go_path_error("mkdir", Path::new(staging), &e)
            ))
        })?;
    let path = absolute_config_path(config_path)
        .map_err(|e| wrap(format!("resolve mail config path: {e}")))?;
    let Some(accounts) = read_accounts(&path).map_err(wrap)? else {
        return Ok(Vec::new());
    };
    let selected: Vec<&ImapAccount> = accounts
        .iter()
        .filter(|candidate| account.is_empty() || account_id(candidate) == account)
        .collect();
    if selected.is_empty() {
        if account.is_empty() {
            return Ok(Vec::new());
        }
        return Err(wrap(format!(
            "no configured mail account with ID {}",
            go_quote(account)
        )));
    }
    let resolved = resolve("").map_err(wrap)?;
    let store = open_store(&resolved).map_err(wrap)?;
    let converted: Vec<symdesk_ingest::mail::ImapAccount> =
        selected.iter().map(|a| (*a).into()).collect();
    let mut poller = MailPoller::new(
        Arc::new(Mutex::new(store)),
        converted.clone(),
        MailPollerOptions {
            interval: None,
            processing_dir: staging.to_owned(),
            failed_dir: go_join(&[staging, "failed"]),
        },
    )
    .map_err(|e| wrap(format!("create mail poller: {e}")))?;
    let collected: Arc<Mutex<Vec<Attachment>>> = Arc::new(Mutex::new(Vec::new()));
    let sink = Arc::clone(&collected);
    poller.set_log(Arc::new(|message| go_log(&message)));
    poller.set_enqueue(Arc::new(move |work_path, _message, _correspondent| {
        sink.lock()
            .unwrap_or_else(|e| e.into_inner())
            .push(Attachment {
                path: work_path.to_owned(),
            });
        Ok(())
    }));
    for account in &converted {
        if let Err(error) = poller.poll_once(account) {
            return Err(wrap(error.text));
        }
    }
    let attachments = std::mem::take(&mut *collected.lock().unwrap_or_else(|e| e.into_inner()));
    Ok(attachments)
}

trait DirBuilderExt700 {
    fn mode_700(&mut self) -> &mut Self;
}

impl DirBuilderExt700 for std::fs::DirBuilder {
    fn mode_700(&mut self) -> &mut Self {
        use std::os::unix::fs::DirBuilderExt;
        self.mode(0o700)
    }
}

fn fetch(vault: Option<&str>) -> Result<ExitCode, String> {
    let (root, mut sidecar) = init_service_deps(vault)?;
    let log = |message: String| go_log(&message);
    let outcome = (|| {
        let config_path =
            global_config_path().map_err(|e| format!("resolve mail config path: {e}"))?;
        let mut statuses: BTreeMap<String, Status> = BTreeMap::new();
        let accounts = match list_accounts(&config_path) {
            Ok(accounts) => accounts,
            Err(error) => {
                log(format!("MailWatcher: failed to list accounts: {error}"));
                return Ok(Vec::new());
            }
        };
        let staging = go_join(&[&root.to_string_lossy(), "inbox", ".mail_tmp"]);
        for account in &accounts {
            let stable_id = if account.id.is_empty() {
                format!("{}@{}", account.username, account.host)
            } else {
                account.id.clone()
            };
            let started = now_text();
            statuses.insert(
                stable_id.clone(),
                Status {
                    account_id: stable_id.clone(),
                    host: account.host.clone(),
                    username: account.username.clone(),
                    last_run: started,
                    last_error: String::new(),
                    messages_total: 0,
                    last_run_count: 0,
                },
            );
            let now = now_text();
            match fetch_account(&config_path, &account.id, &staging) {
                Err(error) => {
                    let error = redact_credentials(&error);
                    log(format!(
                        "MailWatcher: fetch failed for {stable_id}: {error}"
                    ));
                    if let Some(status) = statuses.get_mut(&stable_id) {
                        status.last_run = now_text();
                        status.last_error = error;
                        status.last_run_count = 0;
                    }
                }
                Ok(messages) => {
                    let mut new_count = 0;
                    for message in &messages {
                        let ingested = if Path::new(&message.path).exists() {
                            service_ingest(&root, &mut sidecar, &message.path)
                        } else {
                            Err("attachment missing".to_owned())
                        };
                        match ingested {
                            Ok(_) => new_count += 1,
                            Err(error) => log(format!(
                                "MailWatcher: ingest failed for message from {stable_id}: {error}"
                            )),
                        }
                    }
                    if new_count > 0 {
                        log(format!(
                            "MailWatcher: fetched {} messages ({new_count} new) for {stable_id}",
                            messages.len()
                        ));
                    }
                    if let Some(status) = statuses.get_mut(&stable_id) {
                        status.last_run = now;
                        status.messages_total += new_count;
                        status.last_run_count = new_count;
                    }
                }
            }
        }
        Ok::<_, String>(statuses.into_values().collect::<Vec<_>>())
    })();
    close_sidecar(sidecar);
    Ok(print_indented(&outcome?))
}
