//! Additional read-only handlers, backed by the shared vault and index.
use std::collections::BTreeMap;

use serde_json::Value;
use symdesk_index::docs::DocsFilter;

use super::{ServerConfig, encoded};

pub(super) fn supports(name: &str) -> bool {
    matches!(
        name,
        "desk_docs" | "list_documents" | "docs_review" | "docs_similar"
    )
}

// Anonymous Go structs retain field declaration order in decoder diagnostics.
// Keep tokens raw: 90.0 is not a Go int, and null leaves scalar fields unchanged.
fn arguments(
    raw: Option<&str>,
    fields: &[(&str, &str, &str)],
) -> Result<BTreeMap<String, Value>, String> {
    let raw = raw.ok_or("unexpected end of JSON input")?;
    let mut result = BTreeMap::new();
    if raw == "null" {
        return Ok(result);
    }
    if !raw.starts_with('{') {
        let declaration = fields
            .iter()
            .map(|(key, name, kind)| format!("{name} {kind} \"json:\\\"{key}\\\"\""))
            .collect::<Vec<_>>()
            .join("; ");
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type struct {{ {declaration} }}",
            super::super::go_json_type(raw)
        ));
    }
    for (key, token) in super::super::go_raw_fields(raw)?.0 {
        let Some(&(field, _, kind)) = fields
            .iter()
            .find(|(field, _, _)| symdesk_vault::dataset::go_equal_fold(field, &key))
        else {
            continue;
        };
        let token = token.get();
        if token == "null" {
            if kind.starts_with('*') {
                result.remove(field);
            }
            continue;
        }
        let value: Value = serde_json::from_str(token).map_err(|e| e.to_string())?;
        let valid = if kind == "string" {
            value.is_string()
        } else {
            token.parse::<i64>().is_ok()
        };
        if !valid {
            let mut got = super::super::go_json_type(token).to_owned();
            if got == "number" {
                got.push(' ');
                got.push_str(token);
            }
            return Err(format!(
                "json: cannot unmarshal {got} into Go struct field .{field} of type {}",
                kind.trim_start_matches('*')
            ));
        }
        result.insert(field.to_owned(), value);
    }
    Ok(result)
}

pub(super) fn call(name: &str, raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    let fields: &[(&str, &str, &str)] = match name {
        "desk_docs" | "list_documents" => &[
            ("type", "Type", "string"),
            ("status", "Status", "string"),
            ("person", "Person", "string"),
            ("correspondent", "Correspondent", "string"),
            ("year", "Year", "string"),
            ("due_before", "DueBefore", "string"),
            ("min_confidence", "MinConfidence", "*int"),
            ("max_confidence", "MaxConfidence", "*int"),
        ],
        "docs_review" => &[("threshold", "Threshold", "int")],
        "docs_similar" => &[
            ("file", "File", "string"),
            ("threshold", "Threshold", "int"),
        ],
        _ => return Err(format!("Unknown tool: {name}")),
    };
    let args = arguments(raw, fields)?;
    let text = |key: &str| {
        args.get(key)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    let number = |key: &str| args.get(key).and_then(Value::as_i64);
    if name == "docs_similar" && text("file").is_empty() {
        return Err("file is required".into());
    }
    let (root, sidecar) = super::super::open_sidecar(config)?;
    match name {
        "desk_docs" | "list_documents" => {
            let filter = DocsFilter {
                document_type: text("type"),
                status: text("status"),
                person: text("person"),
                correspondent: text("correspondent"),
                year: text("year"),
                due_before: text("due_before"),
                min_confidence: number("min_confidence"),
                max_confidence: number("max_confidence"),
                ..DocsFilter::default()
            };
            let mut rows = sidecar.docs_list(&filter).map_err(|e| e.to_string())?;
            for row in &mut rows {
                row.path = super::backlink_relative_path(&root, std::path::Path::new(&row.path));
            }
            encoded(&rows)
        }
        "docs_review" => {
            let threshold = number("threshold")
                .filter(|v| *v > 0)
                .unwrap_or(crate::config_paths::load_root_config()?.0.review_threshold);
            let rows = sidecar.docs_review(threshold).map_err(|e| e.to_string())?;
            if rows.is_empty() {
                encoded(&Value::Null)
            } else {
                encoded(&rows)
            }
        }
        "docs_similar" => {
            let hash = crate::duplicates_cli::reference_simhash(&root, &text("file"))?;
            let threshold = number("threshold").filter(|v| *v > 0).unwrap_or(85);
            let rows = sidecar
                .similar_docs(&hash, threshold)
                .map_err(|e| e.to_string())?;
            if rows.is_empty() {
                encoded(&Value::Null)
            } else {
                encoded(&rows)
            }
        }
        _ => Err(format!("Unknown tool: {name}")),
    }
}
