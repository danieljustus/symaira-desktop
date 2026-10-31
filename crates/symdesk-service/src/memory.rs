//! Go `internal/compose` symmemory adapters: the runtime contract through
//! which SymDesk reads and writes the sibling Memory tool. The binary is
//! found through an injected resolver (Go's single `ResolveFunc` seam), so
//! nothing here depends on another Symaira tool at build time.

use std::{process::Command, time::Duration};

use serde::Serialize;
use serde_json::Value;

use crate::{
    gofmt::status_text,
    gojson::{as_string, as_strings, fields, syntax_error, type_error},
    process::run_bounded,
};

const SYMMEMORY: &str = "symmemory";

/// Resolves a sibling-tool name to an executable path.
pub type Resolve<'a> = &'a dyn Fn(&str) -> Result<String, String>;

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct MemoryEntity {
    pub id: String,
    pub name: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub aliases: Option<Vec<String>>,
    pub description: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct MemoryRelation {
    pub from_entity_id: String,
    pub to_entity_id: String,
    pub relation_type: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct MemoryNeighbors {
    pub nodes: Option<Vec<MemoryEntity>>,
    pub edges: Option<Vec<MemoryRelation>>,
}

/// One deterministic candidate for a participant label; never fuzzy.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct CandidateMatch {
    pub entity: MemoryEntity,
    /// `exact_name` or `alias`.
    pub match_reason: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct MemoryRecord {
    pub id: String,
    pub content: String,
    pub scope: String,
    pub entities: Option<Vec<String>>,
}

/// Go `runTool`: the audited exec site. `Err` carries the Go `err` text and
/// the raw stderr (`(stderr: …)` is formatted by the caller).
fn run_tool(
    resolve: Resolve<'_>,
    args: &[&str],
    timeout: Duration,
) -> Result<Result<Vec<u8>, (String, String)>, String> {
    let binary = resolve(SYMMEMORY).map_err(|error| format!("symmemory not found: {error}"))?;
    let mut command = Command::new(binary);
    command.args(args);
    let finished = run_bounded(command, timeout)?;
    if finished.status.success() && !finished.timed_out {
        return Ok(Ok(finished.stdout));
    }
    Ok(Err((
        status_text(finished.status),
        String::from_utf8_lossy(&finished.stderr).into_owned(),
    )))
}

fn entity_from(value: &Value, path: &str) -> Result<MemoryEntity, String> {
    let f = fields(
        value,
        &["id", "name", "type", "aliases", "description"],
        "compose.MemoryEntity",
    )?;
    Ok(MemoryEntity {
        id: as_string(f.values[0], &format!("{path}.id"))?,
        name: as_string(f.values[1], &format!("{path}.name"))?,
        kind: as_string(f.values[2], &format!("{path}.type"))?,
        aliases: as_strings(f.values[3], &format!("{path}.aliases"))?,
        description: as_string(f.values[4], &format!("{path}.description"))?,
    })
}

fn entities_from(value: &Value, path: &str) -> Result<Option<Vec<MemoryEntity>>, String> {
    match value {
        Value::Null => Ok(None),
        Value::Array(items) => items
            .iter()
            .map(|item| entity_from(item, path))
            .collect::<Result<Vec<_>, _>>()
            .map(Some),
        other => Err(type_error(other, path, "[]compose.MemoryEntity")),
    }
}

/// Go `ListEntities` (5s deadline).
///
/// # Errors
///
/// Go's wording for resolution, exit, decode failures.
pub fn list_entities(resolve: Resolve<'_>) -> Result<Option<Vec<MemoryEntity>>, String> {
    let output = run_tool(
        resolve,
        &["entity", "list", "--output", "json"],
        Duration::from_secs(5),
    )?
    .map_err(|(error, stderr)| {
        format!("symmemory entity list failed: {error} (stderr: {stderr})")
    })?;
    let value: Value = serde_json::from_slice(&output).map_err(|error| {
        format!(
            "failed to unmarshal symmemory entities: {}",
            syntax_error(&error)
        )
    })?;
    entities_from(&value, "[]").map_err(|e| format!("failed to unmarshal symmemory entities: {e}"))
}

/// Go `GetNeighbors` (5s deadline).
///
/// # Errors
///
/// Go's wording for resolution, exit, decode failures.
pub fn get_neighbors(resolve: Resolve<'_>, name: &str) -> Result<MemoryNeighbors, String> {
    let output = run_tool(
        resolve,
        &["entity", "neighbors", name, "--output", "json"],
        Duration::from_secs(5),
    )?
    .map_err(|(error, stderr)| format!("symmemory neighbors failed: {error} (stderr: {stderr})"))?;
    let unmarshal = |error: String| format!("failed to unmarshal symmemory neighbors: {error}");
    let value: Value =
        serde_json::from_slice(&output).map_err(|error| unmarshal(syntax_error(&error)))?;
    let f = fields(&value, &["nodes", "edges"], "compose.MemoryNeighbors").map_err(unmarshal)?;
    let nodes = match f.values[0] {
        None => None,
        Some(value) => entities_from(value, "MemoryNeighbors.nodes").map_err(unmarshal)?,
    };
    let edges = match f.values[1] {
        None | Some(Value::Null) => None,
        Some(Value::Array(items)) => Some(
            items
                .iter()
                .map(|item| {
                    let f = fields(
                        item,
                        &["from_entity_id", "to_entity_id", "relation_type"],
                        "compose.MemoryRelation",
                    )?;
                    Ok(MemoryRelation {
                        from_entity_id: as_string(
                            f.values[0],
                            "MemoryNeighbors.edges.from_entity_id",
                        )?,
                        to_entity_id: as_string(f.values[1], "MemoryNeighbors.edges.to_entity_id")?,
                        relation_type: as_string(
                            f.values[2],
                            "MemoryNeighbors.edges.relation_type",
                        )?,
                    })
                })
                .collect::<Result<Vec<_>, String>>()
                .map_err(unmarshal)?,
        ),
        Some(other) => {
            return Err(unmarshal(type_error(
                other,
                "MemoryNeighbors.edges",
                "[]compose.MemoryRelation",
            )));
        }
    };
    Ok(MemoryNeighbors { nodes, edges })
}

/// Go `ResolveCandidates`: exact (case-insensitive) names first, then
/// aliases. An empty label never reaches the tool.
///
/// # Errors
///
/// The `list_entities` failure.
pub fn resolve_candidates(
    resolve: Resolve<'_>,
    label: &str,
) -> Result<Option<Vec<CandidateMatch>>, String> {
    let label = label.trim();
    if label.is_empty() {
        return Ok(None);
    }
    let entities = list_entities(resolve)?.unwrap_or_default();
    let lower = label.to_lowercase();
    let (mut exact, mut aliases) = (Vec::new(), Vec::new());
    for entity in entities {
        if entity.name.to_lowercase() == lower {
            exact.push(CandidateMatch {
                entity,
                match_reason: "exact_name".to_owned(),
            });
            continue;
        }
        if entity
            .aliases
            .as_deref()
            .unwrap_or_default()
            .iter()
            .any(|alias| alias.to_lowercase() == lower)
        {
            aliases.push(CandidateMatch {
                entity,
                match_reason: "alias".to_owned(),
            });
        }
    }
    exact.extend(aliases);
    Ok((!exact.is_empty()).then_some(exact))
}

/// Go `runSymmemory`: errors name the full command line; output is trimmed.
fn run_symmemory(
    resolve: Resolve<'_>,
    args: &[&str],
    timeout: Duration,
) -> Result<Vec<u8>, String> {
    let binary = resolve(SYMMEMORY).map_err(|error| format!("symmemory not found: {error}"))?;
    let mut command = Command::new(binary);
    command.args(args);
    let finished = run_bounded(command, timeout)?;
    let line = args.join(" ");
    if finished.timed_out {
        return Err(format!(
            "symmemory {line} timed out: context deadline exceeded"
        ));
    }
    if !finished.status.success() {
        return Err(format!(
            "symmemory {line} failed: {} (stderr: {})",
            status_text(finished.status),
            String::from_utf8_lossy(&finished.stderr).trim()
        ));
    }
    Ok(finished.stdout)
}

/// Go `ShowEntity`: decodes only the first JSON value; `entity show` prints
/// human-readable summaries after it even with `--output json`.
///
/// # Errors
///
/// Go's wording for exit and decode failures.
pub fn show_entity(resolve: Resolve<'_>, name: &str) -> Result<MemoryEntity, String> {
    let data = run_symmemory(
        resolve,
        &["entity", "show", name, "--output", "json"],
        Duration::from_secs(5),
    )?;
    let unmarshal =
        |error: String| format!("failed to unmarshal symmemory entity show output: {error}");
    let first = serde_json::Deserializer::from_slice(&data)
        .into_iter::<Value>()
        .next()
        .ok_or_else(|| unmarshal("EOF".to_owned()))?
        .map_err(|error| unmarshal(syntax_error(&error)))?;
    entity_from(&first, "MemoryEntity").map_err(unmarshal)
}

/// Go `EnsureEntity`: lookup first (`entity add` is not idempotent); a
/// concurrent creator between the lookup and the add wins quietly.
///
/// # Errors
///
/// The add failure when the entity still does not exist.
pub fn ensure_entity(
    resolve: Resolve<'_>,
    name: &str,
    entity_type: &str,
) -> Result<MemoryEntity, String> {
    if let Ok(existing) = show_entity(resolve, name) {
        return Ok(existing);
    }
    if let Err(error) = run_symmemory(
        resolve,
        &["entity", "add", name, "--type", entity_type],
        Duration::from_secs(10),
    ) {
        return show_entity(resolve, name).map_err(|_| error);
    }
    show_entity(resolve, name)
}

/// Go `RelateEntities` (idempotent on the tool side).
///
/// # Errors
///
/// Go's wording for resolution and exit failures.
pub fn relate_entities(
    resolve: Resolve<'_>,
    from: &str,
    relation: &str,
    to: &str,
) -> Result<(), String> {
    run_symmemory(
        resolve,
        &["entity", "relate", from, relation, to],
        Duration::from_secs(10),
    )
    .map(|_| ())
}

/// Go `SetMemory`: not idempotent; callers track what they already published.
///
/// # Errors
///
/// Go's wording for exit and decode failures.
pub fn set_memory(
    resolve: Resolve<'_>,
    value: &str,
    scope: &str,
    entities: &[String],
) -> Result<MemoryRecord, String> {
    let joined = entities.join(",");
    let mut args = vec![
        "set", "--value", value, "--scope", scope, "--output", "json",
    ];
    if !entities.is_empty() {
        args.extend(["--entities", &joined]);
    }
    let data = run_symmemory(resolve, &args, Duration::from_secs(15))?;
    let unmarshal = |error: String| format!("failed to unmarshal symmemory set output: {error}");
    let value: Value =
        serde_json::from_slice(&data).map_err(|error| unmarshal(syntax_error(&error)))?;
    let f = fields(
        &value,
        &["id", "content", "scope", "entities"],
        "compose.MemoryRecord",
    )
    .map_err(unmarshal)?;
    let record = (|| {
        Ok::<_, String>(MemoryRecord {
            id: as_string(f.values[0], "MemoryRecord.id")?,
            content: as_string(f.values[1], "MemoryRecord.content")?,
            scope: as_string(f.values[2], "MemoryRecord.scope")?,
            entities: as_strings(f.values[3], "MemoryRecord.entities")?,
        })
    })();
    record.map_err(unmarshal)
}
