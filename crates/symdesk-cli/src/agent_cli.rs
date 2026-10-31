//! `symdesk ask --agent`: the bounded read-only tool loop (Go `ai.RunAgent`)
//! over the MCP read-only catalog, optionally confined to a notebook's sources
//! (issue #425).

use std::{collections::BTreeSet, path::Path};

use serde_json::{Map, Value};
use symdesk_core::cancel::CancelToken;
use symdesk_service::agent::{self, AgentRun};

use crate::{ai_cli::EventSink, mcp};

/// Runs the loop, printing every event as it happens.
///
/// # Errors
///
/// A notebook that cannot be resolved (before any provider call), or the event
/// consumer failing.
pub(crate) fn run(
    root: &Path,
    query: &str,
    notebook: &str,
    config: &symdesk_core::config::Config,
    sink: &EventSink,
) -> Result<(), String> {
    let allowed = if notebook.is_empty() {
        None
    } else {
        Some(notebook_scope(root, notebook)?)
    };
    let tools = mcp::agent_catalog();
    let runner = mcp::AgentToolRunner::new(root);
    let mut environment = symdesk_core::config::environment_snapshot()?;
    if let Ok(path) = std::env::var("PATH") {
        environment.insert("PATH".to_owned(), path);
    }
    let mut resolve_key = |reference: &str| crate::ai_secrets::resolve_key(reference);
    let cancel = CancelToken::new();
    let run = AgentRun {
        config,
        environment: &environment,
        resolve_key: &mut resolve_key,
        query,
        tools: &tools,
        results_root: mcp::results_root(root).ok(),
        cancel: &cancel,
    };
    agent::run_agent(
        run,
        &mut |call| {
            let text = runner.call(&call.name, &call.input)?;
            Ok(match &allowed {
                Some(allowed) => scope_output(&call.name, text, allowed),
                None => text,
            })
        },
        &mut |event| sink.emit(&event),
    )
}

/// Go `scopeAgentToolsToNotebook`: the notebook's present sources.
fn notebook_scope(root: &Path, reference: &str) -> Result<BTreeSet<String>, String> {
    let notebook = symdesk_vault::load_notebook(root, reference).map_err(|error| {
        format!(
            "resolve notebook {}: {error}",
            symdesk_vault::go_quote(reference)
        )
    })?;
    Ok(symdesk_vault::resolve_notebook_sources(root, &notebook)
        .into_iter()
        .filter(|source| !source.missing)
        .map(|source| source.path)
        .collect())
}

/// Go `scopedSearchHandler`/`scopedLsHandler`/`scopedBacklinksHandler`: a result
/// outside the notebook never reaches the model; other tools pass through.
fn scope_output(tool: &str, text: String, allowed: &BTreeSet<String>) -> String {
    let (key, path_of): (&str, fn(&Value) -> Option<&str>) = match tool {
        "desk_search" => ("results", |item| item.get("path").and_then(Value::as_str)),
        "desk_ls" => ("files", |item| item.get("path").and_then(Value::as_str)),
        "desk_backlinks" => ("paths", Value::as_str),
        _ => return text,
    };
    let Ok(parsed) = serde_json::from_str::<Value>(&text) else {
        return text;
    };
    let (items, hint) = match (tool, parsed) {
        ("desk_search", Value::Object(map)) => {
            let hint = map
                .get("hint")
                .and_then(Value::as_str)
                .filter(|hint| !hint.is_empty())
                .map(str::to_owned);
            match map.get("results") {
                Some(Value::Array(items)) => (items.clone(), hint),
                Some(Value::Null) | None => (Vec::new(), hint),
                Some(_) => return text,
            }
        }
        ("desk_ls" | "desk_backlinks", Value::Array(items)) => (items, None),
        ("desk_ls" | "desk_backlinks", Value::Null) => (Vec::new(), None),
        _ => return text,
    };
    let mut kept = Vec::new();
    let mut scoped_out = 0_u64;
    for item in items {
        if path_of(&item).is_some_and(|path| allowed.contains(path)) {
            kept.push(item);
        } else {
            scoped_out += 1;
        }
    }
    let mut out = Map::new();
    out.insert(
        key.to_owned(),
        if kept.is_empty() {
            Value::Null
        } else {
            Value::Array(kept)
        },
    );
    if let Some(hint) = hint {
        out.insert("hint".to_owned(), Value::String(hint));
    }
    if scoped_out > 0 {
        out.insert("scoped_out".to_owned(), Value::from(scoped_out));
    }
    crate::go_escape_json(serde_json::to_string(&Value::Object(out)).unwrap_or_default())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scoped_output_filters_and_counts_like_go() {
        let allowed: BTreeSet<String> = ["a.md".to_owned()].into();
        assert_eq!(
            scope_output(
                "desk_search",
                r#"{"results":[{"path":"a.md","score":1},{"path":"b.md","score":2}],"hint":"h"}"#
                    .to_owned(),
                &allowed
            ),
            r#"{"results":[{"path":"a.md","score":1}],"hint":"h","scoped_out":1}"#
        );
        assert_eq!(
            scope_output("desk_ls", "null".to_owned(), &allowed),
            r#"{"files":null}"#
        );
        assert_eq!(
            scope_output("desk_backlinks", r#"["b.md"]"#.to_owned(), &allowed),
            r#"{"paths":null,"scoped_out":1}"#
        );
        assert_eq!(scope_output("desk_props", "{}".to_owned(), &allowed), "{}");
    }
}
