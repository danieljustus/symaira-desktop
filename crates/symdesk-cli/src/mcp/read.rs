//! Read-only notebook/property tools; storage semantics live in the libraries.
use serde::Serialize;
use serde_json::{Value, json};
use symdesk_vault::{
    NotebookSource, list_notebooks, load_notebook, resolve_notebook_sources, secure_path,
};

use super::{ServerConfig, go_string_fields, open_sidecar};

pub(super) fn supports(name: &str) -> bool {
    matches!(
        name,
        "desk_props" | "notebook_list" | "notebook_get" | "notebook_ask"
    )
}

fn encoded<T: Serialize>(value: &T) -> Result<Value, String> {
    let text = serde_json::to_string(value).map_err(|error| error.to_string())?;
    Ok(Value::String(crate::go_escape_json(text)))
}

pub(super) fn call(name: &str, raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    match name {
        "desk_props" => {
            let args =
                go_string_fields(raw, &["file"], r#"struct { File string "json:\"file\"" }"#)?;
            if args[0].is_empty() {
                return Err("file is required".to_owned());
            }
            let (root, sidecar) = open_sidecar(config)?;
            let absolute = secure_path(&root, &args[0]).map_err(|error| error.to_string())?;
            encoded(
                &sidecar
                    .get_properties(&absolute.to_string_lossy())
                    .map_err(|error| error.to_string())?,
            )
        }
        "notebook_list" => {
            // Go ignores arguments for this tool, including malformed types.
            let (root, _sidecar) = open_sidecar(config)?;
            encoded(&list_notebooks(&root)?)
        }
        "notebook_get" => {
            let args = go_string_fields(
                raw,
                &["notebook"],
                r#"struct { Notebook string "json:\"notebook\"" }"#,
            )?;
            if args[0].is_empty() {
                return Err("notebook is required".to_owned());
            }
            let (root, _sidecar) = open_sidecar(config)?;
            let notebook = load_notebook(&root, &args[0])?;
            // Go returns a sorted map containing typed SourceRef structs.
            // Keep map order and each source struct's order, including omissions.
            #[derive(Serialize)]
            struct NotebookResponse {
                created: String,
                description: String,
                id: String,
                path: String,
                sources: Vec<NotebookSource>,
                title: String,
            }
            let sources = resolve_notebook_sources(&root, &notebook);
            encoded(&NotebookResponse {
                created: notebook.created,
                description: notebook.description,
                id: notebook.id,
                path: notebook.path,
                sources,
                title: notebook.title,
            })
        }
        "notebook_ask" => {
            let args = go_string_fields(
                raw,
                &["notebook", "query"],
                r#"struct { Notebook string "json:\"notebook\""; Query string "json:\"query\"" }"#,
            )?;
            if args[0].is_empty() {
                return Err("notebook is required".to_owned());
            }
            if args[1].is_empty() {
                return Err("query is required".to_owned());
            }
            let arguments = serde_json::to_string(&json!({"notebook": args[0], "query": args[1]}))
                .map_err(|error| error.to_string())?;
            super::call_tool("desk_ask", Some(&arguments), config)
        }
        _ => Err(format!("Unknown tool: {name}")),
    }
}
