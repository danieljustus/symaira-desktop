//! The IMAP mail poller (`internal/ingest/internal/ingest/mail.go`): login,
//! select, UID cursor, UID search, envelope de-duplication, body fetch,
//! go-message attachment staging, enqueue, mark-seen/move, idempotency record
//! and the persisted poll status.

use crate::gmime::go_quote;
use crate::goos;
use crate::imap::{ImapClient, TlsImapClient, default_tls_config};
use crate::ingest_fs::{clean_optional_dir, go_base, go_join, is_path_within};
use crate::mail_reader::{MAX_CONSECUTIVE_PART_ERRORS, Next, PartKind, Reader};
use crate::{Store, extract, pipeline, secretref};
use std::io::Write;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::JoinHandle;
use std::time::Duration;
use time::OffsetDateTime;

/// `config.IMAPAccount`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ImapAccount {
    pub host: String,
    pub port: i64,
    pub username: String,
    pub password_secret: String,
    pub folder: String,
    pub from: Vec<String>,
    pub subject: Vec<String>,
    pub has_attachment: bool,
    pub action: String,
    pub move_to: String,
    pub archive_mail: bool,
}

/// `config.AccountID`.
pub fn account_id(a: &ImapAccount) -> String {
    let folder = if a.folder.is_empty() {
        "INBOX"
    } else {
        &a.folder
    };
    format!("{}@{}:{}/{folder}", a.username, a.host, a.port)
}

/// `secret.IsPlaintext`.
pub fn is_plaintext_secret(s: &str) -> bool {
    !s.starts_with("env://") && !s.starts_with("symvault://") && !s.starts_with("keychain://")
}

/// How a failed poll is classified for logs and the persisted status
/// (`mailPollLogReason`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ErrKind {
    Canceled,
    Deadline,
    NetTimeout,
    Net,
    Other,
}

#[derive(Clone, Debug)]
pub struct PollError {
    pub text: String,
    pub kind: ErrKind,
}

impl PollError {
    fn other(text: String) -> Self {
        Self {
            text,
            kind: ErrKind::Other,
        }
    }

    /// `mailPollLogReason`: a static, credential-free classification.
    pub fn log_reason(&self) -> &'static str {
        match self.kind {
            ErrKind::Canceled => "shutting down",
            ErrKind::Deadline => "deadline exceeded",
            ErrKind::NetTimeout => "network timeout",
            ErrKind::Net => "network error",
            ErrKind::Other => "authentication or configuration error",
        }
    }
}

impl std::fmt::Display for PollError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.text)
    }
}

pub type Dial = Arc<dyn Fn(&str, &str) -> Result<Box<dyn ImapClient>, String> + Send + Sync>;
/// `MailPoller.Enqueue(workPath, msgID, correspondent)`.
pub type EnqueueFn = Arc<dyn Fn(&str, &str, &str) -> Result<(), String> + Send + Sync>;
pub type Log = Arc<dyn Fn(String) + Send + Sync>;

#[derive(Clone, Debug, Default)]
pub struct MailPollerOptions {
    pub interval: Option<Duration>,
    pub processing_dir: String,
    pub failed_dir: String,
}

/// The production dial function: IMAP over TLS with the platform roots.
pub fn default_dial() -> Dial {
    Arc::new(|addr, host| {
        let config = default_tls_config()?;
        let client = TlsImapClient::dial_tls(addr, host, config)?;
        Ok(Box::new(client) as Box<dyn ImapClient>)
    })
}

/// The default logger: Go's `log.Printf` to stderr (`YYYY/MM/DD HH:MM:SS msg`).
pub fn stderr_log() -> Log {
    Arc::new(|msg| {
        let now = OffsetDateTime::now_local().unwrap_or_else(|_| OffsetDateTime::now_utc());
        eprintln!(
            "{:04}/{:02}/{:02} {:02}:{:02}:{:02} {msg}",
            now.year(),
            u8::from(now.month()),
            now.day(),
            now.hour(),
            now.minute(),
            now.second()
        );
    })
}

struct Shared {
    cancel: AtomicBool,
    wake: Condvar,
    lock: Mutex<()>,
}

pub struct MailPoller {
    store: Arc<Mutex<Store>>,
    accounts: Vec<ImapAccount>,
    interval: Duration,
    processing_dir: String,
    #[allow(dead_code)]
    failed_dir: String,
    dial: Dial,
    log: Log,
    enqueue: Option<EnqueueFn>,
    shared: Arc<Shared>,
    handles: Vec<JoinHandle<()>>,
}

impl MailPoller {
    /// `NewMailPoller`.
    pub fn new(
        store: Arc<Mutex<Store>>,
        accounts: Vec<ImapAccount>,
        opts: MailPollerOptions,
    ) -> Result<Self, String> {
        let interval = match opts.interval {
            Some(d) if !d.is_zero() => d,
            _ => Duration::from_secs(5 * 60),
        };
        let processing_dir = clean_optional_dir(&opts.processing_dir)
            .map_err(|e| format!("resolve processing dir: {e}"))?;
        let failed_dir =
            clean_optional_dir(&opts.failed_dir).map_err(|e| format!("resolve failed dir: {e}"))?;
        Ok(Self {
            store,
            accounts,
            interval,
            processing_dir,
            failed_dir,
            dial: default_dial(),
            log: stderr_log(),
            enqueue: None,
            shared: Arc::new(Shared {
                cancel: AtomicBool::new(false),
                wake: Condvar::new(),
                lock: Mutex::new(()),
            }),
            handles: Vec::new(),
        })
    }

    pub fn set_dial(&mut self, dial: Dial) {
        self.dial = dial;
    }

    pub fn set_log(&mut self, log: Log) {
        self.log = log;
    }

    /// An embedding consumer's replacement for the store-backed job enqueue.
    pub fn set_enqueue(&mut self, enqueue: EnqueueFn) {
        self.enqueue = Some(enqueue);
    }

    pub fn accounts(&self) -> &[ImapAccount] {
        &self.accounts
    }

    /// `Start`: one polling loop per account.
    pub fn start(&mut self) -> Result<(), String> {
        for (i, acc) in self.accounts.clone().into_iter().enumerate() {
            if is_plaintext_secret(&acc.password_secret) {
                (self.log)(format!(
                    "[MailPoller] Account {i} ({}) has a bare password_secret value; it will be resolved as an environment variable name, not as a password. Use symvault://, keychain:// or env:// explicitly (run 'symdesk doctor' for details)",
                    acc.username
                ));
            }
            let worker = self.worker();
            self.handles
                .push(std::thread::spawn(move || worker.poll_loop(&acc, i)));
        }
        Ok(())
    }

    /// `Close`: stop the loops and wait for them.
    pub fn close(&mut self) {
        self.shared.cancel.store(true, Ordering::SeqCst);
        {
            let _guard = self.shared.lock.lock().unwrap_or_else(|e| e.into_inner());
            self.shared.wake.notify_all();
        }
        for h in self.handles.drain(..) {
            let _ = h.join();
        }
    }

    /// `PollOnce`: one poll cycle for `acc`, recording its outcome.
    pub fn poll_once(&self, acc: &ImapAccount) -> Result<(), PollError> {
        self.worker().poll_account_and_record(acc)
    }

    fn worker(&self) -> Worker {
        Worker {
            store: Arc::clone(&self.store),
            interval: self.interval,
            processing_dir: self.processing_dir.clone(),
            dial: Arc::clone(&self.dial),
            log: Arc::clone(&self.log),
            enqueue: self.enqueue.clone(),
            shared: Arc::clone(&self.shared),
        }
    }
}

impl Drop for MailPoller {
    fn drop(&mut self) {
        self.close();
    }
}

#[derive(Clone)]
struct Worker {
    store: Arc<Mutex<Store>>,
    interval: Duration,
    processing_dir: String,
    dial: Dial,
    log: Log,
    enqueue: Option<EnqueueFn>,
    shared: Arc<Shared>,
}

impl Worker {
    fn cancelled(&self) -> bool {
        self.shared.cancel.load(Ordering::SeqCst)
    }

    fn store(&self) -> std::sync::MutexGuard<'_, Store> {
        self.store.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn poll_loop(&self, acc: &ImapAccount, index: usize) {
        if let Err(err) = self.poll_account_and_record(acc) {
            (self.log)(format!(
                "[MailPoller] Account {index} ({}) initial poll failed: {} (run 'symdesk doctor' for details)",
                acc.username,
                err.log_reason()
            ));
        }
        loop {
            let guard = self.shared.lock.lock().unwrap_or_else(|e| e.into_inner());
            if self.cancelled() {
                return;
            }
            let (guard, timeout) = self
                .shared
                .wake
                .wait_timeout(guard, self.interval)
                .unwrap_or_else(|e| e.into_inner());
            drop(guard);
            if self.cancelled() {
                return;
            }
            if !timeout.timed_out() {
                continue;
            }
            if let Err(err) = self.poll_account_and_record(acc) {
                (self.log)(format!(
                    "[MailPoller] Account {index} ({}) poll failed: {} (run 'symdesk doctor' for details)",
                    acc.username,
                    err.log_reason()
                ));
            }
        }
    }

    fn poll_account_and_record(&self, acc: &ImapAccount) -> Result<(), PollError> {
        let result = self.poll_account(acc);
        let (status, last_error) = match &result {
            Ok(()) => ("ok", String::new()),
            Err(e) => ("error", e.log_reason().to_owned()),
        };
        let recorded = self.store().record_mail_poll_status(
            &account_id(acc),
            OffsetDateTime::now_utc(),
            status,
            &last_error,
        );
        if let Err(e) = recorded {
            (self.log)(format!(
                "[MailPoller] failed to record poll status for {}: {e}",
                acc.username
            ));
        }
        result
    }

    fn poll_account(&self, acc: &ImapAccount) -> Result<(), PollError> {
        let pwd = secretref::resolve(&acc.password_secret, "").map_err(|e| {
            PollError::other(format!("resolve password_secret for {}: {e}", acc.username))
        })?;
        let addr = format!("{}:{}", acc.host, acc.port);
        let mut client = (self.dial)(&addr, &acc.host).map_err(|e| PollError {
            kind: if e.starts_with("dial tcp ") {
                if e.ends_with(": i/o timeout") {
                    ErrKind::NetTimeout
                } else {
                    ErrKind::Net
                }
            } else {
                ErrKind::Other
            },
            text: format!("dial tls: {e}"),
        })?;
        let result = self.poll_session(acc, client.as_mut(), &pwd);
        if let Err(e) = client.close() {
            (self.log)(format!("[MailPoller] IMAP client close failed: {e}"));
        }
        result
    }

    fn poll_session(
        &self,
        acc: &ImapAccount,
        client: &mut dyn ImapClient,
        pwd: &str,
    ) -> Result<(), PollError> {
        let other = PollError::other;
        client
            .login(&acc.username, pwd)
            .map_err(|e| other(format!("login failed: {e}")))?;
        let folder = if acc.folder.is_empty() {
            "INBOX"
        } else {
            &acc.folder
        };
        let status = client.select(folder).map_err(|e| {
            other(format!(
                "select folder {}: {e}",
                go_quote(folder.as_bytes())
            ))
        })?;

        let account_id = account_id(acc);
        let cursor = self
            .store()
            .get_mail_poll_cursor(&account_id)
            .map_err(|e| other(format!("load poll cursor: {e}")))?;
        let mut start_uid = 1u32;
        if let Some(c) = &cursor
            && c.uid_validity == status.uid_validity
        {
            start_uid = c.last_uid.wrapping_add(1);
        }
        let uids = client
            .search_uid(start_uid, acc.action == "mark_seen")
            .map_err(|e| other(format!("uid search: {e}")))?;

        if !uids.is_empty() {
            let envelopes = client
                .fetch_envelopes_uid(&uids)
                .map_err(|e| other(format!("fetch envelopes: {e}")))?;
            let mut new_uids = Vec::new();
            for env in &envelopes {
                match &env.envelope {
                    Some(e) if !e.message_id.is_empty() => {
                        let has = self
                            .store()
                            .has_mail_message(&e.message_id)
                            .map_err(|e| other(format!("check idempotency: {e}")))?;
                        if !has {
                            new_uids.push(env.uid);
                        }
                    }
                    _ => new_uids.push(env.uid),
                }
            }
            if !new_uids.is_empty() {
                let messages = client
                    .fetch_uid(&new_uids)
                    .map_err(|e| other(format!("fetch: {e}")))?;
                for msg in &messages {
                    if let Err(e) = self.process_message(acc, client, msg) {
                        (self.log)(format!(
                            "[MailPoller] Failed to process message {}: {e}",
                            msg.seq_num
                        ));
                    }
                }
            }
        }

        // UIDNext is one past the highest UID the server knows about.
        let new_last_uid = status.uid_next.saturating_sub(1);
        self.store()
            .set_mail_poll_cursor(&account_id, folder, status.uid_validity, new_last_uid)
            .map_err(|e| other(format!("save poll cursor: {e}")))?;
        Ok(())
    }

    fn process_message(
        &self,
        acc: &ImapAccount,
        client: &mut dyn ImapClient,
        msg: &crate::imap::ImapMessage,
    ) -> Result<(), String> {
        let Some(envelope) = &msg.envelope else {
            return Ok(());
        };
        let msg_id = if envelope.message_id.is_empty() {
            format!("fallback-seq-{}", msg.seq_num)
        } else {
            envelope.message_id.clone()
        };
        let has = self
            .store()
            .has_mail_message(&msg_id)
            .map_err(|e| format!("check idempotency: {e}"))?;
        if has {
            return Ok(());
        }
        if msg.body.is_empty() {
            return Err("no body section found".into());
        }
        let mut reader =
            Reader::create(&msg.body).map_err(|e| format!("create mail reader: {e}"))?;
        let from = reader.header.get("From");
        let correspondent = String::from_utf8_lossy(&from).into_owned();

        let mut attachments: Vec<String> = Vec::new();
        let mut consecutive_errors = 0usize;
        while !self.cancelled() {
            match reader.next_part() {
                Next::Eof => break,
                Next::Error(e) => {
                    (self.log)(format!("[MailPoller] Error reading part: {e}"));
                    consecutive_errors += 1;
                    if consecutive_errors >= MAX_CONSECUTIVE_PART_ERRORS {
                        return Err(format!(
                            "read parts: giving up after {consecutive_errors} consecutive errors"
                        ));
                    }
                    continue;
                }
                Next::Part(_, Some(e)) => {
                    (self.log)(format!("[MailPoller] Error reading part: {e}"));
                    consecutive_errors = 0;
                    continue;
                }
                Next::Part(part, None) => {
                    consecutive_errors = 0;
                    if part.kind != PartKind::Attachment {
                        continue;
                    }
                    if self.processing_dir.is_empty() {
                        return Err(
                            "processing_dir is required for mail attachment ingestion".into()
                        );
                    }
                    let mut filename = go_base(&part.header.filename());
                    if filename.is_empty() || filename == "." || filename == ".." {
                        filename = "attachment.bin".into();
                    }
                    let out_path = go_join(
                        &self.processing_dir,
                        &format!("{}-{filename}", msg_id.replace('/', "_")),
                    );
                    if !is_path_within(&out_path, &self.processing_dir) {
                        (self.log)(format!(
                            "[MailPoller] Attachment path {} escapes processing directory, skipping",
                            go_quote(out_path.as_bytes())
                        ));
                        continue;
                    }
                    save_stream(&out_path, &part.body)
                        .map_err(|e| format!("save attachment {filename}: {e}"))?;
                    attachments.push(out_path);
                }
            }
        }

        if attachments.is_empty() && acc.has_attachment {
            // skip processing if no attachments and config requires it
        } else {
            for att in &attachments {
                let result = match &self.enqueue {
                    Some(hook) => hook(att, &msg_id, &correspondent),
                    None => self.enqueue_file(att, &msg_id, &correspondent),
                };
                result.map_err(|e| format!("enqueue attachment {att}: {e}"))?;
            }
        }

        if acc.action == "mark_seen" {
            client
                .store_seen(msg.seq_num)
                .map_err(|e| format!("mark seen: {e}"))?;
        } else if acc.action == "move" && !acc.move_to.is_empty() {
            client
                .move_to(msg.seq_num, &acc.move_to)
                .map_err(|e| format!("move to {}: {e}", acc.move_to))?;
        }

        self.store()
            .track_mail_message(&msg_id, &acc.username)
            .map_err(|e| format!("track mail message: {e}"))?;
        Ok(())
    }

    fn enqueue_file(
        &self,
        work_path: &str,
        msg_id: &str,
        correspondent: &str,
    ) -> Result<(), String> {
        let log = Arc::clone(&self.log);
        let kind = extract::detect_file_with(work_path, &mut |line| log(line))
            .map_err(|e| format!("detect file kind: {e}"))?;
        let hash = pipeline::hash_file(work_path).map_err(|e| format!("hash file: {e}"))?;
        let store = self.store();
        let (doc, created) = store
            .create_or_get(work_path, &hash, &kind, "")
            .map_err(|e| format!("create or get document: {e}"))?;
        if !created {
            (self.log)(format!(
                "[MailPoller] File {work_path} (hash {hash}) already ingested."
            ));
            return Ok(());
        }
        store
            .set_provenance(doc.id, msg_id, correspondent)
            .map_err(|e| format!("set provenance: {e}"))?;
        store
            .enqueue_job(doc.id, &kind)
            .map_err(|e| format!("enqueue job: {e}"))?;
        Ok(())
    }
}

/// `saveStream`: exclusive create (0600), write what was decoded, then report
/// the stream's error.
fn save_stream(path: &str, body: &crate::gmime::Stream<'_>) -> Result<(), String> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .map_err(|e| goos::path_error("open", path, &e))?;
    let write = f.write_all(&body.data);
    let close = f.flush();
    if let Some(e) = body.end.error() {
        return Err(e.to_owned());
    }
    write.map_err(|e| e.to_string())?;
    close.map_err(|e| e.to_string())
}
