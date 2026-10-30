//! Run approval mutations ported from Go `internal/room/approval`.

use std::path::Path;

use serde::Serialize;
use sha2::{Digest, Sha256};

use crate::{
    authorization,
    event::{self, Event, EventError},
    identity::Identity,
    journal, members,
    runs::{self, RunQueryError},
};

/// Approve a requested run and append its signed journal event.
pub fn approve(
    room_dir: &Path,
    run_id: &str,
    scope: &str,
    ttl: time::Duration,
    signer: &Identity,
) -> Result<Event, ApprovalError> {
    approve_with_barrier(room_dir, run_id, scope, ttl, signer, None)
}

// `after_authorization` is a per-call barrier seam for concurrency tests; public
// callers pass `None`. It runs with the room transaction held (Go `approve`).
fn approve_with_barrier(
    room_dir: &Path,
    run_id: &str,
    scope: &str,
    ttl: time::Duration,
    signer: &Identity,
    after_authorization: Option<&dyn Fn()>,
) -> Result<Event, ApprovalError> {
    runs::read_room_config(room_dir)?;
    authorization::with_room(room_dir, || {
        approve_locked(room_dir, run_id, scope, ttl, signer, after_authorization)
    })
}

fn approve_locked(
    room_dir: &Path,
    run_id: &str,
    scope: &str,
    ttl: time::Duration,
    signer: &Identity,
    after_authorization: Option<&dyn Fn()>,
) -> Result<Event, ApprovalError> {
    let config = runs::read_room_config(room_dir)?;
    let events = journal::merge_all(room_dir)
        .map_err(|error| ApprovalError::ReadJournal(error.to_string()))?;
    let mut state = members::State::default();
    for event in &events {
        if event.room == config.id
            && matches!(
                event.kind.as_str(),
                "room.created" | "member.added" | "member.removed" | "member.role_changed"
            )
        {
            let _ =
                state.apply_signed_event_with_root(event, &config.root_event, &config.root_pubkey);
        }
    }
    let member = state
        .members
        .get(&signer.member_id)
        .ok_or(ApprovalError::MemberNotFound)?;
    if member.role == "agent" {
        return Err(ApprovalError::AgentForbidden);
    }
    if !member.can_perform("approve") {
        return Err(if member.role == "observer" {
            ApprovalError::ObserverForbidden
        } else {
            ApprovalError::InvalidRole
        });
    }
    if let Some(barrier) = after_authorization {
        barrier();
    }

    let run = runs::get(room_dir, run_id)?;
    if run.state != "requested" {
        return Err(ApprovalError::InvalidTransition(format!(
            "cannot approve run in state '{}'",
            run.state
        )));
    }

    let scope = if scope.is_empty() { "all" } else { scope };
    let expiration = time::OffsetDateTime::now_utc()
        .checked_add(ttl)
        .ok_or_else(|| ApprovalError::Encoding("approval expiry is out of range".into()))?;
    let format = time::format_description::parse_borrowed::<1>(
        "[year]-[month]-[day]T[hour]:[minute]:[second]Z",
    )
    .expect("valid fixed RFC3339 format");
    let expires_at = expiration
        .format(&format)
        .map_err(|error| ApprovalError::Encoding(error.to_string()))?;
    let approval_id = approval_id(&format!("{run_id}{scope}{expires_at}"));
    let body = ApproveBody {
        run_id,
        approval_id: &approval_id,
        scope,
        expires_at: &expires_at,
    };
    append_approval_event(
        room_dir,
        signer,
        format!("ev_{}", &approval_id[4..]),
        "run.approved",
        &body,
    )
}

/// Deny a requested run and append its signed journal event.
pub fn deny(
    room_dir: &Path,
    run_id: &str,
    reason: &str,
    signer: &Identity,
) -> Result<Event, ApprovalError> {
    // Go `Deny` reads the room config before it looks the run up.
    runs::read_room_config(room_dir)?;
    let run = runs::get(room_dir, run_id)?;
    if run.state != "requested" {
        return Err(ApprovalError::InvalidTransition(format!(
            "cannot deny run in state '{}'",
            run.state
        )));
    }
    let approval_id = approval_id(&format!("{run_id}{reason}"));
    let body = DenyBody {
        run_id,
        approval_id: &approval_id,
        reason,
    };
    append_approval_event(
        room_dir,
        signer,
        format!("ev_{}", &approval_id[4..]),
        "run.denied",
        &body,
    )
}

fn approval_id(input: &str) -> String {
    let digest = Sha256::digest(input.as_bytes());
    format!("app_{}", &hex::encode(digest)[..16])
}

fn append_approval_event(
    room_dir: &Path,
    signer: &Identity,
    id: String,
    kind: &str,
    body: &impl Serialize,
) -> Result<Event, ApprovalError> {
    let room_id = runs::read_room_id(room_dir)?;
    let stats = journal::read_journal_stats(room_dir).map_err(ApprovalError::WriteJournal)?;
    let author =
        journal::author_stats(room_dir, &signer.member_id).map_err(ApprovalError::WriteJournal)?;
    let body = serde_json::to_string(body)
        .map_err(|error| ApprovalError::Encoding(error.to_string()))?
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    let body = serde_json::value::RawValue::from_string(body)
        .map_err(|error| ApprovalError::Encoding(error.to_string()))?;
    let mut event = Event {
        v: event::CURRENT_VERSION,
        id,
        room: room_id,
        author: signer.member_id.clone(),
        seq: author.seq.saturating_add(1).max(1),
        prev: author.prev,
        lamport: stats.max_lamport.saturating_add(1),
        ts: String::new(),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    event.sign(signer)?;
    journal::append_event(room_dir, &event).map_err(ApprovalError::WriteEvent)?;
    Ok(event)
}

#[derive(Serialize)]
struct ApproveBody<'a> {
    run_id: &'a str,
    approval_id: &'a str,
    scope: &'a str,
    expires_at: &'a str,
}

#[derive(Serialize)]
struct DenyBody<'a> {
    run_id: &'a str,
    approval_id: &'a str,
    reason: &'a str,
}

#[derive(Debug, thiserror::Error)]
pub enum ApprovalError {
    #[error(transparent)]
    RunQuery(#[from] RunQueryError),
    #[error("{0}")]
    ReadJournal(String),
    #[error("{0}")]
    MembershipState(String),
    #[error("member not found")]
    MemberNotFound,
    #[error("agent identity is forbidden from approving runs")]
    AgentForbidden,
    #[error("observer role has read-only access")]
    ObserverForbidden,
    #[error("invalid member role")]
    InvalidRole,
    #[error("invalid run state transition: {0}")]
    InvalidTransition(String),
    #[error("{0}")]
    WriteJournal(std::io::Error),
    #[error("{0}")]
    WriteEvent(journal::JournalError),
    #[error("canonical encoding: {0}")]
    Encoding(String),
    #[error(transparent)]
    Event(#[from] EventError),
    #[error("{0}")]
    Authorization(#[from] std::io::Error),
}

#[cfg(test)]
mod tests {
    use std::io::{BufRead, BufReader};
    use std::process::{Command, Stdio};
    use std::sync::mpsc;
    use std::time::Duration;

    use super::*;
    use crate::identity;

    const CHILD: &str = "SYMROOM_APPROVAL_ROLE_CHILD";

    fn room(owner: &Identity, actor: &Identity) -> (std::path::PathBuf, String) {
        let mut nonce = [0_u8; 8];
        getrandom::fill(&mut nonce).expect("nonce");
        let parent = std::env::temp_dir().join(format!(
            "symroom-approval-race-{}-{}",
            std::process::id(),
            hex::encode(nonce)
        ));
        let dir = parent.join("room");
        crate::room_init::init(
            &dir,
            "race",
            owner,
            "room_race",
            "ev_race_root",
            time::OffsetDateTime::now_utc(),
        )
        .expect("init room");
        let run = runs::request(&dir, "deploy", "", "", owner).expect("request run");
        let run_id =
            serde_json::from_str::<serde_json::Value>(run.body.get()).expect("body")["run_id"]
                .as_str()
                .expect("run id")
                .to_owned();
        members::add_member(
            &dir,
            "reviewer",
            &hex::encode(&actor.public_key),
            "member",
            "human",
            owner,
        )
        .expect("add member");
        (dir, run_id)
    }

    // The subprocess must observe real kernel contention before the parent
    // releases its approval barrier, so the test does not depend on timing.
    #[test]
    fn approve_serializes_independent_role_changes() {
        if std::env::var_os(CHILD).is_some() {
            return;
        }
        for action in ["remove", "observer", "agent"] {
            let owner = identity::generate("owner").expect("owner");
            let actor = identity::generate("reviewer").expect("actor");
            let (dir, run_id) = room(&owner, &actor);
            let (checked_tx, checked_rx) = mpsc::channel();
            let (resume_tx, resume_rx) = mpsc::channel::<()>();
            let approved = std::thread::scope(|scope| {
                let (dir, run_id, actor) = (&dir, &run_id, &actor);
                let approval = scope.spawn(move || {
                    let barrier = || {
                        checked_tx.send(()).expect("checked");
                        resume_rx.recv().expect("resume");
                    };
                    approve_with_barrier(
                        dir,
                        run_id,
                        "all",
                        time::Duration::hours(1),
                        actor,
                        Some(&barrier),
                    )
                });
                checked_rx
                    .recv_timeout(Duration::from_secs(30))
                    .expect("approval reached barrier");
                let mut child = Command::new(std::env::current_exe().expect("test image"))
                    .args([
                        "--exact",
                        "approval::tests::approval_role_change_child",
                        "--nocapture",
                        "--test-threads=1",
                    ])
                    .env(CHILD, action)
                    .env("SYMROOM_APPROVAL_ROOM", dir)
                    .env("SYMROOM_APPROVAL_MEMBER", &actor.member_id)
                    .env("SYMROOM_APPROVAL_OWNER", hex::encode(&owner.private_key))
                    .stdout(Stdio::piped())
                    .spawn()
                    .expect("spawn role writer");
                let mut lines = BufReader::new(child.stdout.take().expect("stdout")).lines();
                let contended = lines
                    .by_ref()
                    .map_while(Result::ok)
                    // libtest may prefix the line with its own `test name ... ` progress.
                    .any(|line| line.ends_with("AUTHORIZATION_LOCK_BUSY"));
                if !contended {
                    resume_tx.send(()).ok();
                    panic!("independent writer did not see held kernel lock: {action}");
                }
                assert!(!approval.is_finished(), "approval left paused barrier");
                resume_tx.send(()).expect("resume");
                let approved = approval.join().expect("approval thread");
                lines.for_each(drop);
                assert!(
                    child.wait().expect("role writer").success(),
                    "role writer failed"
                );
                approved.expect("serialized approval")
            });

            let events = journal::merge_all(&dir).expect("merge");
            let approval_index = events.iter().position(|e| e.id == approved.id);
            let change_index = events
                .iter()
                .rposition(|e| matches!(e.kind.as_str(), "member.removed" | "member.role_changed"));
            assert!(
                matches!((approval_index, change_index), (Some(a), Some(c)) if c > a),
                "role change overtook locked authorization: {approval_index:?} {change_index:?}"
            );
            assert_eq!(runs::get(&dir, &run_id).expect("run").state, "approved");
            assert!(journal::verify(&dir).expect("verify").valid);
            // A later attempt observes the changed role and keeps existing denials.
            let error = approve(&dir, &run_id, "all", time::Duration::hours(1), &actor)
                .expect_err("post-change approval");
            let want = match action {
                "remove" => "member not found",
                "observer" => "observer role has read-only access",
                _ => "agent identity is forbidden from approving runs",
            };
            assert_eq!(error.to_string(), want);
            std::fs::remove_dir_all(dir.parent().expect("parent")).ok();
        }
    }

    #[test]
    fn approval_role_change_child() {
        let Some(action) = std::env::var_os(CHILD) else {
            return;
        };
        let env = |key| std::env::var(key).expect(key);
        let dir = std::path::PathBuf::from(env("SYMROOM_APPROVAL_ROOM"));
        let guard = authorization::try_acquire(&dir).expect("contention probe");
        assert!(
            guard.is_none(),
            "approval released its transaction before append"
        );
        // Bypass libtest output capture so the parent sees the signal at once.
        let mut stdout = std::io::stdout().lock();
        std::io::Write::write_all(&mut stdout, b"AUTHORIZATION_LOCK_BUSY\n").expect("signal");
        std::io::Write::flush(&mut stdout).expect("flush");
        drop(stdout);
        let key = hex::decode(env("SYMROOM_APPROVAL_OWNER")).expect("owner key");
        let owner = identity::identity_from_private_key("owner", &key).expect("owner");
        let member = env("SYMROOM_APPROVAL_MEMBER");
        match action.to_str().expect("action") {
            "remove" => members::remove_member(&dir, &member, &owner),
            role => members::set_member_role(&dir, &member, role, &owner),
        }
        .expect("role change");
    }
}
