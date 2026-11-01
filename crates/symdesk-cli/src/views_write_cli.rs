//! `symdesk views save|delete` and `symdesk views base save|delete|new`
//! (Go `cmd/symdesk/views.go`, `internal/service/views.go`): the write half of
//! the saved-view manager; storage stays in symdesk-vault, the sidecar is
//! re-indexed afterwards like Go's `reindexBase`/`reindexAllBases`.
use std::{collections::BTreeMap, path::Path, process::ExitCode};

use symdesk_index::Sidecar;
use symdesk_vault::{
    BASES_DIR, Base, View, delete_view_with_snapshot, get_base, list_bases, migrate_legacy_views,
    save_base_with_snapshot, save_view_with_snapshot, secure_path, slugify,
};

use crate::{
    mutate_cli::{delete_note, parse, reindex, snapshot},
    service_cli::{go_string_map, with_deps},
    views_cli::base_text,
    write_go_json, write_stdout,
};

/// Go `json.Unmarshal` into a view/base: its syntax errors, serde's wording
/// for type errors (declared limit, as for `dataset`).
fn decode<T: serde::de::DeserializeOwned>(raw: &str, go_type: &str) -> Result<T, String> {
    symdesk_vault::go_json_validate_syntax(raw.as_bytes())?;
    let value: serde_json::Value = serde_json::from_str(raw).map_err(|error| error.to_string())?;
    let kind = match &value {
        // `null` leaves the zero value, like Go.
        serde_json::Value::Null => {
            return serde_json::from_value(serde_json::json!({})).map_err(|error| error.to_string());
        }
        serde_json::Value::Object(_) => {
            return serde_json::from_value(value).map_err(|error| error.to_string());
        }
        serde_json::Value::Array(_) => "array",
        serde_json::Value::String(_) => "string",
        serde_json::Value::Number(_) => "number",
        serde_json::Value::Bool(_) => "bool",
    };
    Err(format!(
        "json: cannot unmarshal {kind} into Go value of type dbviews.{go_type}"
    ))
}

fn reindex_base(root: &Path, db: &mut Sidecar, base: &Base) -> Result<(), String> {
    let path = secure_path(root, &base.path).map_err(|e| e.to_string())?;
    parse(&path).map_err(|e| format!("wrote base but failed to parse for indexing: {e}"))?;
    reindex(db, &path)
        .map(|_| ())
        .map_err(|e| format!("wrote base but failed to index: {e}"))
}

fn reindex_all_bases(root: &Path, db: &mut Sidecar) -> Result<(), String> {
    for base in list_bases(root).map_err(|e| e.to_string())? {
        reindex_base(root, db, &base)?;
    }
    Ok(())
}

fn status(entries: &[(&str, &str)], json: bool) -> ExitCode {
    let map: BTreeMap<String, String> = entries
        .iter()
        .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
        .collect();
    if json {
        write_go_json(&map)
    } else {
        write_stdout(format!("{}\n", go_string_map(map.iter())))
    }
}

pub fn run(base: bool, name: &str, args: &[String], vault: Option<&str>, json: bool) -> ExitCode {
    with_deps(vault, json, |root, db| {
        migrate_legacy_views(root);
        let take_snapshot = |path: &Path| snapshot(root, path);
        match (base, name) {
            (false, "save") => {
                let view: View = decode(&args[0], "View")?;
                save_view_with_snapshot(root, view, take_snapshot).map_err(|e| e.to_string())?;
                reindex_all_bases(root, db)?;
                Ok(status(&[("status", "saved")], json))
            }
            (false, _) => {
                delete_view_with_snapshot(root, &args[0], take_snapshot)
                    .map_err(|e| e.to_string())?;
                reindex_all_bases(root, db)?;
                Ok(status(&[("status", "deleted")], json))
            }
            (true, "save") => {
                let mut value: Base =
                    decode(&args[0], "Base").map_err(|e| format!("unmarshal base: {e}"))?;
                save_base_with_snapshot(root, &mut value, take_snapshot)
                    .map_err(|e| e.to_string())?;
                reindex_base(root, db, &value)?;
                Ok(status(&[("path", &value.path), ("status", "saved")], json))
            }
            (true, "delete") => {
                let value = get_base(root, &args[0]).map_err(|e| e.to_string())?;
                delete_note(root, db, &value.path)?;
                Ok(status(&[("status", "deleted")], json))
            }
            _ => {
                let title = &args[0];
                let slug = slugify(title);
                let created = time::OffsetDateTime::now_utc()
                    .replace_nanosecond(0)
                    .expect("zero nanoseconds")
                    .format(&time::format_description::well_known::Rfc3339)
                    .unwrap_or_default();
                let mut value = Base {
                    id: slug.clone(),
                    path: format!("{BASES_DIR}{}{slug}.md", std::path::MAIN_SEPARATOR),
                    title: title.clone(),
                    description: args.get(1).cloned().unwrap_or_default(),
                    created,
                    tags: vec!["base".to_owned()],
                    properties: BTreeMap::new(),
                    views: Vec::new(),
                    extras: BTreeMap::new(),
                };
                save_base_with_snapshot(root, &mut value, take_snapshot)
                    .map_err(|e| e.to_string())?;
                reindex_base(root, db, &value)?;
                Ok(if json {
                    write_go_json(&value)
                } else {
                    write_stdout(format!("&{}\n", base_text(&value)))
                })
            }
        }
    })
}
