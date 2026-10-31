//! Rust-only mail poller behaviour the Go oracle cannot pin deterministically:
//! concurrent `Start` loops over two accounts, go-imap's racy double CAPABILITY
//! (Rust asks once, lazily), and the bounded multipart error loop (Go spins
//! forever on a multipart part without boundary).
#![cfg(unix)]

use base64::Engine as _;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use symdesk_ingest::Store;
use symdesk_ingest::imap::{ImapClient, ImapEnvelope, ImapMessage, MailboxStatus, TlsImapClient};
use symdesk_ingest::mail::{ImapAccount, MailPoller, MailPollerOptions, account_id};

#[path = "support/imap_server.rs"]
mod imap_server;
use imap_server::{Folder, Msg, Server, Spec, client_config};

static ENV_LOCK: Mutex<()> = Mutex::new(());

fn crlf(s: &str) -> Vec<u8> {
    s.replace('\n', "\r\n").into_bytes()
}

fn mail(boundary_line: &str, filename: &str) -> Vec<u8> {
    let pdf =
        base64::engine::general_purpose::STANDARD.encode(format!("%PDF-1.4 {filename}\n%%EOF\n"));
    crlf(&format!(
        "From: a@example.test\nContent-Type: multipart/mixed; boundary=\"B\"\n\n--B\n{boundary_line}Content-Type: application/pdf\nContent-Transfer-Encoding: base64\nContent-Disposition: attachment; filename=\"{filename}\"\n\n{pdf}\n--B--\n"
    ))
}

fn spec(folders: &[(&str, Vec<Msg>)], caps: &str, no_cap_codes: bool) -> Spec {
    let mut s = Spec {
        password: "pw".into(),
        caps: caps.into(),
        no_cap_codes,
        ..Spec::default()
    };
    for (name, msgs) in folders {
        s.folders.insert(
            (*name).to_owned(),
            Folder {
                uid_validity: 1,
                messages: msgs.clone(),
                ..Folder::default()
            },
        );
    }
    s
}

fn msg(uid: u32, id: &str, raw: Vec<u8>) -> Msg {
    Msg {
        uid,
        message_id: id.into(),
        raw,
        ..Msg::default()
    }
}

fn account(server: &Server, folder: &str, user: &str) -> ImapAccount {
    ImapAccount {
        host: "127.0.0.1".into(),
        port: i64::from(server.addr.port()),
        username: user.into(),
        password_secret: "env://MAILBEHAVIOUR_PW".into(),
        folder: folder.into(),
        ..ImapAccount::default()
    }
}

fn poller(dir: &std::path::Path, accounts: Vec<ImapAccount>) -> (MailPoller, Arc<Mutex<Store>>) {
    let store = Arc::new(Mutex::new(Store::open(&dir.join("db.sqlite")).unwrap()));
    std::fs::create_dir_all(dir.join("proc")).unwrap();
    let mut p = MailPoller::new(
        Arc::clone(&store),
        accounts,
        MailPollerOptions {
            interval: Some(Duration::from_secs(3600)),
            processing_dir: dir.join("proc").to_string_lossy().into_owned(),
            failed_dir: String::new(),
        },
    )
    .unwrap();
    let config = client_config();
    p.set_dial(Arc::new(move |addr, host| {
        Ok(
            Box::new(TlsImapClient::dial_tls(addr, host, Arc::clone(&config))?)
                as Box<dyn ImapClient>,
        )
    }));
    p.set_log(Arc::new(|_| {}));
    (p, store)
}

fn scratch(name: &str) -> std::path::PathBuf {
    let dir = std::path::Path::new(env!("CARGO_TARGET_TMPDIR"))
        .join(format!("{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn start_polls_every_account_concurrently_and_close_stops_the_loops() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    // SAFETY: serialized by ENV_LOCK.
    unsafe { std::env::set_var("MAILBEHAVIOUR_PW", "pw") };
    let dir = scratch("start2");
    let server = Server::start(spec(
        &[
            ("INBOX", vec![msg(1, "<s1@x>", mail("", "one.pdf"))]),
            ("Archive", vec![msg(1, "<s2@x>", mail("", "two.pdf"))]),
        ],
        "MOVE",
        false,
    ));
    let accounts = vec![
        account(&server, "INBOX", "u1"),
        account(&server, "Archive", "u2"),
    ];
    let (mut p, store) = poller(&dir, accounts.clone());
    p.start().unwrap();
    let deadline = Instant::now() + Duration::from_secs(30);
    while Instant::now() < deadline {
        let all = accounts.iter().all(|a| {
            store
                .lock()
                .unwrap()
                .get_mail_poll_status(&account_id(a))
                .ok()
                .flatten()
                .is_some_and(|s| s.status == "ok")
        });
        if all {
            break;
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    p.close();
    for a in &accounts {
        let s = store
            .lock()
            .unwrap()
            .get_mail_poll_status(&account_id(a))
            .unwrap()
            .unwrap();
        assert_eq!(s.status, "ok", "{}", a.username);
    }
    let guard = store.lock().unwrap();
    assert!(guard.has_mail_message("s1@x").unwrap());
    assert!(guard.has_mail_message("s2@x").unwrap());
    assert_eq!(guard.list_jobs(100).unwrap().map_or(0, |j| j.len()), 2);
    // Close is idempotent and joins promptly once the loops are stopped.
    drop(guard);
    p.close();
    unsafe { std::env::remove_var("MAILBEHAVIOUR_PW") };
}

#[test]
fn move_asks_for_capabilities_once_when_the_server_sent_none() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    // SAFETY: serialized by ENV_LOCK.
    unsafe { std::env::set_var("MAILBEHAVIOUR_PW", "pw") };
    let dir = scratch("caps");
    let server = Server::start(spec(
        &[
            ("INBOX", vec![msg(1, "<c1@x>", mail("", "one.pdf"))]),
            ("Archive", vec![]),
        ],
        "MOVE",
        true,
    ));
    let mut acc = account(&server, "INBOX", "u1");
    acc.action = "move".into();
    acc.move_to = "Archive".into();
    let (p, _store) = poller(&dir, vec![acc.clone()]);
    p.poll_once(&acc).unwrap();
    let transcript = server.conns.lock().unwrap()[0].clone();
    let caps: Vec<_> = transcript.iter().filter(|c| *c == "CAPABILITY").collect();
    assert_eq!(caps.len(), 1, "{transcript:?}");
    let cap_at = transcript.iter().position(|c| c == "CAPABILITY").unwrap();
    let move_at = transcript
        .iter()
        .position(|c| c.starts_with("MOVE "))
        .unwrap();
    assert!(cap_at < move_at, "{transcript:?}");
    assert_eq!(
        server.spec.lock().unwrap().folders["Archive"]
            .messages
            .len(),
        1
    );
    unsafe { std::env::remove_var("MAILBEHAVIOUR_PW") };
}

/// A client that hands the poller one message whose multipart part has no
/// boundary: Go loops forever, Rust gives up after a bounded number of errors.
struct BoundaryLessClient;

impl ImapClient for BoundaryLessClient {
    fn login(&mut self, _: &str, _: &str) -> Result<(), String> {
        Ok(())
    }
    fn select(&mut self, _: &str) -> Result<MailboxStatus, String> {
        Ok(MailboxStatus {
            uid_validity: 1,
            uid_next: 2,
        })
    }
    fn search_uid(&mut self, _: u32, _: bool) -> Result<Vec<u32>, String> {
        Ok(vec![1])
    }
    fn fetch_envelopes_uid(&mut self, _: &[u32]) -> Result<Vec<ImapMessage>, String> {
        self.fetch_uid(&[1])
    }
    fn fetch_uid(&mut self, _: &[u32]) -> Result<Vec<ImapMessage>, String> {
        Ok(vec![ImapMessage {
            seq_num: 1,
            uid: 1,
            envelope: Some(ImapEnvelope {
                message_id: "nb@x".into(),
            }),
            body: crlf(
                "From: p@example.test\nContent-Type: multipart/mixed\n\n--XX\nContent-Type: application/pdf\n\nxxx\n--XX--\n",
            ),
        }])
    }
    fn store_seen(&mut self, _: u32) -> Result<(), String> {
        Ok(())
    }
    fn move_to(&mut self, _: u32, _: &str) -> Result<(), String> {
        Ok(())
    }
    fn close(&mut self) -> Result<(), String> {
        Ok(())
    }
}

#[test]
fn multipart_without_boundary_cannot_spin_forever() {
    let _lock = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    // SAFETY: serialized by ENV_LOCK.
    unsafe { std::env::set_var("MAILBEHAVIOUR_PW", "pw") };
    let dir = scratch("noboundary");
    let acc = ImapAccount {
        host: "h".into(),
        port: 993,
        username: "u".into(),
        password_secret: "env://MAILBEHAVIOUR_PW".into(),
        ..ImapAccount::default()
    };
    let (mut p, store) = poller(&dir, vec![acc.clone()]);
    p.set_dial(Arc::new(|_, _| {
        Ok(Box::new(BoundaryLessClient) as Box<dyn ImapClient>)
    }));
    let logs = Arc::new(Mutex::new(Vec::<String>::new()));
    {
        let logs = Arc::clone(&logs);
        p.set_log(Arc::new(move |m| logs.lock().unwrap().push(m)));
    }
    let started = Instant::now();
    p.poll_once(&acc).unwrap();
    assert!(started.elapsed() < Duration::from_secs(60));
    let logs = logs.lock().unwrap();
    assert!(
        logs.iter()
            .any(|l| l.contains("Failed to process message 1: read parts: giving up")),
        "{logs:?}"
    );
    assert!(!store.lock().unwrap().has_mail_message("nb@x").unwrap());
    unsafe { std::env::remove_var("MAILBEHAVIOUR_PW") };
}
