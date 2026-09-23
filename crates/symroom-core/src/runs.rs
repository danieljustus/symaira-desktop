//! Pure run-event projection from `internal/room/run.ProjectRuns`.

use std::collections::BTreeMap;
use std::fmt;

use serde::de::{DeserializeSeed, IgnoredAny, MapAccess, Visitor};
use serde::{Deserializer, Serialize};
use serde_json::Value;

use crate::event::Event;

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
