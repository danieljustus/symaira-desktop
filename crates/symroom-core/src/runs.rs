//! Pure run-event projection from `internal/room/run.ProjectRuns`.

use std::collections::BTreeMap;
use std::fmt;

use serde::de::{MapAccess, Visitor};
use serde::{Deserialize, Deserializer, Serialize};
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
}

/// Applies only the seven `run.*` event kinds; checkpoint projection stays in
/// its separate ROOM slice, as it does not affect the records here.
pub fn project_runs(events: &[Event]) -> BTreeMap<String, Run> {
    let mut runs = BTreeMap::new();
    for event in events {
        let Some(body) = body_object(event.body.get()) else {
            continue;
        };
        match event.kind.as_str() {
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
    runs
}

struct BodyFields(Vec<(String, Value)>);

impl<'de> Deserialize<'de> for BodyFields {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct BodyVisitor;

        impl<'de> Visitor<'de> for BodyVisitor {
            type Value = BodyFields;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("a run event object or null")
            }

            fn visit_unit<E: serde::de::Error>(self) -> Result<Self::Value, E> {
                Ok(BodyFields(Vec::new()))
            }

            fn visit_map<M: MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
                let mut fields = Vec::new();
                while let Some(field) = map.next_entry()? {
                    fields.push(field);
                }
                Ok(BodyFields(fields))
            }
        }

        deserializer.deserialize_any(BodyVisitor)
    }
}

fn body_object(raw: &str) -> Option<Vec<(String, Value)>> {
    serde_json::from_str::<BodyFields>(raw)
        .ok()
        .map(|fields| fields.0)
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
