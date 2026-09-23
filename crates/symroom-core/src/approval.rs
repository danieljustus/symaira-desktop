//! Run approval mutations ported from Go `internal/room/approval`.

use std::path::Path;

use serde::Serialize;
use sha2::{Digest, Sha256};

use crate::{
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
    let events = journal::merge_all(room_dir)
        .map_err(|error| ApprovalError::ReadJournal(error.to_string()))?;
    let mut state = members::State::default();
    for event in &events {
        state
            .apply_event(event)
            .map_err(ApprovalError::MembershipState)?;
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
    let format = time::format_description::parse("[year]-[month]-[day]T[hour]:[minute]:[second]Z")
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
        room: "rm_test".to_owned(),
        author: signer.member_id.clone(),
        seq: author.seq.saturating_add(1).max(1),
        prev: author.prev,
        lamport: stats.max_lamport.saturating_add(1),
        ts: event::format_timestamp(time::OffsetDateTime::now_utc()),
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
}
