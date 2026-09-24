//! Pure run-event projection from `internal/room/run.ProjectRuns`.

use std::collections::BTreeMap;
use std::{
    fmt, thread,
    time::{Duration, Instant},
};

use serde::de::{DeserializeSeed, IgnoredAny, MapAccess, Visitor};
use serde::{Deserializer, Serialize};
use serde_json::Value;
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};

use crate::{
    event::{self, Event},
    identity::Identity,
    journal,
};

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Run {
    pub id: String,
    pub title: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub plan_file: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub adapter: Option<String>,
    pub state: String,
    pub author: String,
    pub created_at: String,
    pub updated_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub approval_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub scope: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub expires_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub summary: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub artifacts: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub checkpoints: Option<Vec<Checkpoint>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Checkpoint {
    pub id: String,
    pub run_id: String,
    pub question: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub answer: Option<String>,
    pub state: String,
    pub author: String,
    pub created_at: String,
    pub updated_at: String,
}

/// Go: `run.ProjectCheckpoints`.
pub fn project_checkpoints(events: &[Event]) -> BTreeMap<String, Checkpoint> {
    let mut checkpoints = BTreeMap::new();
    for event in events {
        let kind = match event.kind.as_str() {
            "checkpoint.requested" | "checkpoint.resolved" => event.kind.as_str(),
            _ => continue,
        };
        let Some(body) = body_object(event.body.get(), kind) else {
            continue;
        };
        match kind {
            "checkpoint.requested" => {
                let (Some(id), Some(run_id), Some(question)) = (
                    string_field(&body, "checkpoint_id"),
                    string_field(&body, "run_id"),
                    string_field(&body, "question"),
                ) else {
                    continue;
                };
                if !id.is_empty() {
                    checkpoints.insert(
                        id.clone(),
                        Checkpoint {
                            id,
                            run_id,
                            question,
                            answer: None,
                            state: "requested".into(),
                            author: event.author.clone(),
                            created_at: event.ts.clone(),
                            updated_at: event.ts.clone(),
                        },
                    );
                }
            }
            "checkpoint.resolved" => {
                let (Some(id), Some(answer)) = (
                    string_field(&body, "checkpoint_id"),
                    string_field(&body, "answer"),
                ) else {
                    continue;
                };
                if let Some(checkpoint) = checkpoints.get_mut(&id) {
                    checkpoint.state = "resolved".into();
                    checkpoint.answer = nonempty(answer);
                    checkpoint.updated_at.clone_from(&event.ts);
                }
            }
            _ => unreachable!(),
        }
    }
    checkpoints
}

/// Applies only the seven `run.*` event kinds; checkpoint projection stays in
/// its separate ROOM slice, as it does not affect the records here.
pub fn project_runs(events: &[Event]) -> BTreeMap<String, Run> {
    let mut runs = BTreeMap::new();
    for event in events {
        let kind = match event.kind.as_str() {
            "run.requested" | "run.approved" | "run.denied" | "run.started" | "run.finished"
            | "run.failed" | "run.cancelled" => event.kind.as_str(),
            _ => continue,
        };
        let Some(body) = body_object(event.body.get(), kind) else {
            continue;
        };
        match kind {
            "run.requested" => {
                let (Some(run_id), Some(title), Some(plan_file), Some(adapter)) = (
                    string_field(&body, "run_id"),
                    string_field(&body, "title"),
                    string_field(&body, "plan_file"),
                    string_field(&body, "adapter"),
                ) else {
                    continue;
                };
                if run_id.is_empty() {
                    continue;
                }
                runs.insert(
                    run_id.clone(),
                    Run {
                        id: run_id,
                        title,
                        plan_file: nonempty(plan_file),
                        adapter: nonempty(adapter),
                        state: "requested".into(),
                        author: event.author.clone(),
                        created_at: event.ts.clone(),
                        updated_at: event.ts.clone(),
                        approval_id: None,
                        scope: None,
                        expires_at: None,
                        summary: None,
                        error: None,
                        artifacts: None,
                        checkpoints: None,
                    },
                );
            }
            "run.approved" => {
                let Some(run_id) = string_field(&body, "run_id") else {
                    continue;
                };
                let (Some(approval_id), Some(scope), Some(expires_at)) = (
                    string_field(&body, "approval_id"),
                    string_field(&body, "scope"),
                    string_field(&body, "expires_at"),
                ) else {
                    continue;
                };
                if let Some(run) = runs.get_mut(&run_id) {
                    run.state = "approved".into();
                    run.approval_id = nonempty(approval_id);
                    run.scope = nonempty(scope);
                    run.expires_at = nonempty(expires_at);
                    run.updated_at.clone_from(&event.ts);
                }
            }
            "run.denied" => {
                let (Some(run_id), Some(reason)) =
                    (string_field(&body, "run_id"), string_field(&body, "reason"))
                else {
                    continue;
                };
                if let Some(run) = runs.get_mut(&run_id) {
                    run.state = "denied".into();
                    run.error = nonempty(reason);
                    run.updated_at.clone_from(&event.ts);
                }
            }
            "run.started" => {
                let Some(run_id) = string_field(&body, "run_id") else {
                    continue;
                };
                if let Some(run) = runs.get_mut(&run_id) {
                    run.state = "started".into();
                    run.updated_at.clone_from(&event.ts);
                }
            }
            "run.finished" => {
                let (Some(run_id), Some(summary), Some(artifacts)) = (
                    string_field(&body, "run_id"),
                    string_field(&body, "summary"),
                    string_array_field(&body, "artifacts"),
                ) else {
                    continue;
                };
                if let Some(run) = runs.get_mut(&run_id) {
                    run.state = "finished".into();
                    run.summary = nonempty(summary);
                    run.artifacts = artifacts;
                    run.updated_at.clone_from(&event.ts);
                }
            }
            "run.failed" => {
                let (Some(run_id), Some(error)) =
                    (string_field(&body, "run_id"), string_field(&body, "error"))
                else {
                    continue;
                };
                if let Some(run) = runs.get_mut(&run_id) {
                    run.state = "failed".into();
                    run.error = nonempty(error);
                    run.updated_at.clone_from(&event.ts);
                }
            }
            "run.cancelled" => {
                let (Some(run_id), Some(reason)) =
                    (string_field(&body, "run_id"), string_field(&body, "reason"))
                else {
                    continue;
                };
                if let Some(run) = runs.get_mut(&run_id) {
                    run.state = "cancelled".into();
                    run.error = nonempty(reason);
                    run.updated_at.clone_from(&event.ts);
                }
            }
            _ => {}
        }
    }
    for checkpoint in project_checkpoints(events).values() {
        if let Some(run) = runs.get_mut(&checkpoint.run_id) {
            run.checkpoints
                .get_or_insert_with(Vec::new)
                .push(checkpoint.clone());
        }
    }
    runs
}

/// Go: `run.List`, including `journal.MergeAll` and the creation-time ordering.
pub fn list(room_dir: &std::path::Path, pending_only: bool) -> Result<Vec<Run>, RunQueryError> {
    let events = journal::merge_all(room_dir)?;
    let mut runs: Vec<_> = project_runs(&events)
        .into_values()
        .filter(|run| !pending_only || matches!(run.state.as_str(), "requested" | "approved"))
        .collect();
    runs.sort_by(|left, right| left.created_at.cmp(&right.created_at));
    Ok(runs)
}

/// Go: `run.Get`, including `journal.MergeAll`.
pub fn get(room_dir: &std::path::Path, run_id: &str) -> Result<Run, RunQueryError> {
    let events = journal::merge_all(room_dir)?;
    project_runs(&events)
        .remove(run_id)
        .ok_or(RunQueryError::NotFound)
}

/// Go: `run.Wait`. Read and replay the journal immediately, then poll every
/// 500ms until approval, denial/cancellation, or the timeout. Journal read
/// errors are ignored while waiting, matching Go's `Wait` loop.
pub fn wait(
    room_dir: &std::path::Path,
    run_id: &str,
    timeout: Duration,
) -> Result<Run, RunWaitError> {
    let started = Instant::now();
    loop {
        if let Ok(run) = get(room_dir, run_id) {
            match run.state.as_str() {
                "approved" => return Ok(run),
                "denied" => return Err(RunWaitError::Denied),
                "cancelled" => return Err(RunWaitError::Cancelled),
                _ => {}
            }
        }
        let elapsed = started.elapsed();
        if elapsed >= timeout {
            return Err(RunWaitError::Timeout);
        }
        thread::sleep((timeout - elapsed).min(Duration::from_millis(500)));
    }
}

/// Go: `run.Request` — append a signed `run.requested` event.
pub fn request(
    room_dir: &std::path::Path,
    title: &str,
    plan_file: &str,
    adapter: &str,
    identity: &Identity,
) -> Result<Event, RunMutationError> {
    let author = journal::author_stats(room_dir, &identity.member_id)?;
    let sequence = author.seq.saturating_add(1).max(1);
    let input = format!("{}:{title}:{sequence}", identity.member_id);
    let digest = Sha256::digest(input.as_bytes());
    let run_id = format!("run_{}", hex::encode(digest)[..16].to_owned());
    let body = go_json(&serde_json::json!({
        "adapter": adapter,
        "plan_file": plan_file,
        "run_id": run_id,
        "title": title,
    }))?;
    let event = append_run_event(
        room_dir,
        identity,
        format!("ev_{}", &run_id[4..]),
        "run.requested",
        body,
    )?;
    Ok(event)
}

/// Go: `run.Start` — append `run.started` only for a live approved run.
pub fn start(
    room_dir: &std::path::Path,
    run_id: &str,
    identity: &Identity,
) -> Result<Event, RunMutationError> {
    let run = get(room_dir, run_id)?;
    if run.state != "approved" {
        return Err(RunMutationError::InvalidTransition(format!(
            "cannot start run in state '{}' (must be 'approved')",
            run.state
        )));
    }
    if let Some(expires_at) = &run.expires_at
        && let Ok(expiration) =
            time::OffsetDateTime::parse(expires_at, &time::format_description::well_known::Rfc3339)
        && time::OffsetDateTime::now_utc() > expiration
    {
        return Err(RunMutationError::ApprovalExpired(format!(
            "approval expired at {expires_at}"
        )));
    }
    let body = go_json(&serde_json::json!({ "run_id": run_id }))?;
    let event_id = derived_event_id(run_id, "start");
    append_run_event(room_dir, identity, event_id, "run.started", body)
}

/// Go: `run.Cancel` — append `run.cancelled` unless the run is terminal.
pub fn cancel(
    room_dir: &std::path::Path,
    run_id: &str,
    reason: &str,
    identity: &Identity,
) -> Result<Event, RunMutationError> {
    let run = get(room_dir, run_id)?;
    if matches!(run.state.as_str(), "finished" | "failed" | "cancelled") {
        return Err(RunMutationError::InvalidTransition(format!(
            "cannot cancel run in terminal state '{}'",
            run.state
        )));
    }
    let body = go_json(&serde_json::json!({ "reason": reason, "run_id": run_id }))?;
    let event_id = derived_event_id(run_id, "cancel");
    append_run_event(room_dir, identity, event_id, "run.cancelled", body)
}

fn derived_event_id(run_id: &str, action: &str) -> String {
    let digest = Sha256::digest(format!("{run_id}{action}").as_bytes());
    format!("ev_{}", &hex::encode(digest)[..16])
}

fn append_run_event(
    room_dir: &std::path::Path,
    identity: &Identity,
    id: String,
    kind: &str,
    body: String,
) -> Result<Event, RunMutationError> {
    let stats = journal::read_journal_stats(room_dir)?;
    let author = journal::author_stats(room_dir, &identity.member_id)?;
    let body = RawValue::from_string(body)
        .map_err(|error| RunMutationError::Encoding(error.to_string()))?;
    let mut event = Event {
        v: event::CURRENT_VERSION,
        id,
        room: "rm_test".to_owned(),
        author: identity.member_id.clone(),
        seq: author.seq.saturating_add(1),
        prev: author.prev,
        lamport: stats.max_lamport.saturating_add(1),
        ts: event::format_timestamp(time::OffsetDateTime::now_utc()),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    event.sign(identity)?;
    journal::append_event(room_dir, &event)?;
    Ok(event)
}

fn go_json(value: &Value) -> Result<String, RunMutationError> {
    let rendered = serde_json::to_string(value)
        .map_err(|error| RunMutationError::Encoding(error.to_string()))?;
    Ok(rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029"))
}

#[derive(Debug, thiserror::Error)]
pub enum RunMutationError {
    #[error(transparent)]
    Query(#[from] RunQueryError),
    #[error(transparent)]
    Journal(#[from] journal::JournalError),
    #[error(transparent)]
    Event(#[from] event::EventError),
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error("invalid run state transition: {0}")]
    InvalidTransition(String),
    #[error("run approval has expired: {0}")]
    ApprovalExpired(String),
    #[error("canonical encoding: {0}")]
    Encoding(String),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RunWaitError {
    Timeout,
    Denied,
    Cancelled,
}

#[derive(Debug, thiserror::Error)]
pub enum RunQueryError {
    #[error(transparent)]
    Journal(#[from] journal::ReadSegmentsError),
    #[error("run not found")]
    NotFound,
}

struct BodyFieldsSeed<'a>(&'a str);

impl<'de> DeserializeSeed<'de> for BodyFieldsSeed<'_> {
    type Value = Vec<(String, Value)>;

    fn deserialize<D: Deserializer<'de>>(self, deserializer: D) -> Result<Self::Value, D::Error> {
        struct BodyVisitor<'a>(&'a str);

        impl<'de> Visitor<'de> for BodyVisitor<'_> {
            type Value = Vec<(String, Value)>;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("a run event object or null")
            }

            fn visit_unit<E: serde::de::Error>(self) -> Result<Self::Value, E> {
                Ok(Vec::new())
            }

            fn visit_map<M: MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
                let mut fields = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    if field_is_known(self.0, &key) {
                        fields.push((key, map.next_value()?));
                    } else {
                        map.next_value::<IgnoredAny>()?;
                    }
                }
                Ok(fields)
            }
        }

        deserializer.deserialize_any(BodyVisitor(self.0))
    }
}

fn body_object(raw: &str, kind: &str) -> Option<Vec<(String, Value)>> {
    let mut deserializer = serde_json::Deserializer::from_str(raw);
    let fields = BodyFieldsSeed(kind).deserialize(&mut deserializer).ok()?;
    deserializer.end().ok()?;
    Some(fields)
}

fn field_is_known(kind: &str, key: &str) -> bool {
    let names: &[&str] = match kind {
        "run.requested" => &["run_id", "title", "plan_file", "adapter"],
        "run.approved" => &["run_id", "approval_id", "scope", "expires_at"],
        "run.denied" | "run.cancelled" => &["run_id", "reason"],
        "run.started" => &["run_id"],
        "run.finished" => &["run_id", "summary", "artifacts"],
        "run.failed" => &["run_id", "error"],
        "checkpoint.requested" => &["checkpoint_id", "run_id", "question"],
        "checkpoint.resolved" => &["checkpoint_id", "answer"],
        _ => &[],
    };
    names.iter().any(|name| key.eq_ignore_ascii_case(name))
}

fn string_field(fields: &[(String, Value)], name: &str) -> Option<String> {
    let mut result = String::new();
    for (_, value) in fields
        .iter()
        .filter(|(key, _)| key.eq_ignore_ascii_case(name))
    {
        match value {
            Value::Null => {}
            Value::String(value) => result.clone_from(value),
            _ => return None,
        }
    }
    Some(result)
}

fn string_array_field(fields: &[(String, Value)], name: &str) -> Option<Option<Vec<String>>> {
    let mut result = None;
    for (_, value) in fields
        .iter()
        .filter(|(key, _)| key.eq_ignore_ascii_case(name))
    {
        match value {
            Value::Null => result = None,
            Value::Array(values) => {
                result = Some(
                    values
                        .iter()
                        .map(|value| value.as_str().map(str::to_owned))
                        .collect::<Option<Vec<_>>>()?,
                );
            }
            _ => return None,
        }
    }
    Some(result)
}

fn nonempty(value: String) -> Option<String> {
    (!value.is_empty()).then_some(value)
}
