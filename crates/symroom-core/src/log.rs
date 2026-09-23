//! Go `Journal.QueryLog` and human journal rendering.

use std::path::Path;

use serde_json::Value;

use crate::{event::Event, journal};

#[derive(Default, serde::Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct LogFilter {
    pub since: String,
    pub until: String,
    pub kind: String,
    pub author: String,
    pub run: String,
    pub limit: i64,
}

pub struct LogResult {
    pub events: Vec<Event>,
    pub invalid_count: usize,
}

#[derive(Debug, thiserror::Error)]
pub enum LogError {
    #[error("verify error: read all segments: {0}")]
    Verify(journal::ReadSegmentsError),
    #[error("merge all: {0}")]
    Merge(journal::ReadSegmentsError),
}

/// Verify, omit signature/chain-invalid event IDs, then filter the merged log.
pub fn query(room_dir: &Path, filter: &LogFilter) -> Result<LogResult, LogError> {
    let report = journal::verify(room_dir).map_err(LogError::Verify)?;
    let invalid_ids = report
        .findings
        .iter()
        .filter(|finding| {
            matches!(finding.code.as_str(), "signature_invalid" | "chain_broken")
                && !finding.event_id.is_empty()
        })
        .map(|finding| finding.event_id.as_str())
        .collect::<std::collections::BTreeSet<_>>();
    let merged = journal::merge_all(room_dir).map_err(LogError::Merge)?;
    let mut events = Vec::new();
    for event in merged {
        if invalid_ids.contains(event.id.as_str())
            || (!filter.since.is_empty() && event.ts < filter.since)
            || (!filter.until.is_empty() && event.ts > filter.until)
            || (!filter.kind.is_empty() && event.kind != filter.kind)
            || (!filter.author.is_empty() && !event.author.eq_ignore_ascii_case(&filter.author))
        {
            continue;
        }
        if !filter.run.is_empty() {
            let Ok(Value::Object(body)) = serde_json::from_str::<Value>(event.body.get()) else {
                continue;
            };
            let run = body
                .get("run_id")
                .and_then(Value::as_str)
                .filter(|value| !value.is_empty())
                .or_else(|| body.get("run").and_then(Value::as_str));
            if run != Some(filter.run.as_str()) {
                continue;
            }
        }
        events.push(event);
        if filter.limit > 0 && events.len() == filter.limit as usize {
            break;
        }
    }
    Ok(LogResult {
        events,
        invalid_count: invalid_ids.len(),
    })
}

pub fn format_event_human(event: &Event) -> String {
    let summary = match serde_json::from_str::<Value>(event.body.get()) {
        Ok(Value::Object(body)) => body
            .get("text")
            .and_then(Value::as_str)
            .or_else(|| body.get("name").and_then(Value::as_str))
            .unwrap_or(event.body.get())
            .to_owned(),
        _ => event.body.get().to_owned(),
    };
    format!(
        "[{}] {} ({}): {summary}",
        event.ts, event.author, event.kind
    )
}
