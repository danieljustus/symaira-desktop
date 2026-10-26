//! Native allow-write MCP handlers. Markdown remains authoritative.
use serde_json::{Value, json};
use symdesk_index::IndexedDocument;
use symdesk_vault::{HistoryStore, activity_journal::append_activity, secure_path};
use time::OffsetDateTime;

use super::{ServerConfig, go_string_fields, open_sidecar};

pub(super) fn supports(name: &str) -> bool {
    matches!(
        name,
        "desk_note_new"
            | "doc_set_status"
            | "notebook_create"
            | "notebook_add_source"
            | "notebook_remove_source"
    )
}

pub(super) fn call(name: &str, raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    match name {
        "desk_note_new" => {
            let args = go_string_fields(
                raw,
                &["title", "content", "template"],
                r#"struct { Title string "json:\"title\""; Content string "json:\"content\""; Template string "json:\"template\"" }"#,
            )?;
            let [title, content, template] = args.as_slice() else {
                unreachable!()
            };
            if title.is_empty() {
                return Err("title is required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            let relative = symdesk_vault::note_file_name(title);
            let absolute = secure_path(&root, &relative).map_err(|e| e.to_string())?;
            let now = OffsetDateTime::now_utc();
            let stamp = symdesk_vault::notes::format_rfc3339(now);
            let template_text = if template.is_empty() {
                String::new()
            } else {
                secure_path(&root, &format!("templates/{template}.md"))
                    .ok()
                    .and_then(|path| std::fs::read_to_string(path).ok())
                    .unwrap_or_default()
            };
            let from_frontmatter = template_text.starts_with("---\n");
            let body = if template_text.is_empty() {
                symdesk_vault::note_document(title, &stamp, content)
            } else {
                let text = template_text
                    .replace("{{title}}", title)
                    .replace("{{date}}", &now.date().to_string())
                    .replace(
                        "{{time}}",
                        &format!("{:02}:{:02}", now.hour(), now.minute()),
                    );
                let mut body = if from_frontmatter {
                    text
                } else {
                    symdesk_vault::note_document(title, &stamp, &text)
                };
                if !content.is_empty() {
                    body.push('\n');
                    body.push_str(content);
                }
                body
            };
            let _ = HistoryStore::new(&root).snapshot(&relative);
            let mut options = std::fs::OpenOptions::new();
            options.write(true).create(true).truncate(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                options.mode(0o644);
            }
            use std::io::Write as _;
            options
                .open(&absolute)
                .and_then(|mut file| file.write_all(body.as_bytes()))
                .map_err(|e| {
                    format!("failed to write file: {}", io_error("open", &absolute, &e))
                })?;
            if from_frontmatter {
                let _ = symdesk_vault::set_property(&root, &relative, "title", title);
                let _ = symdesk_vault::set_property(&root, &relative, "created", &stamp);
            }
            index_file(
                &absolute,
                &mut sidecar,
                Some(&symdesk_vault::sha256_hex(body.as_bytes())),
            )
            .map_err(|e| format!("failed to index new file: {e}"))?;
            let _ = append_activity(&root, "file_added", &relative, title, "created note");
            Ok(json!({"path": relative}))
        }
        "doc_set_status" => {
            let args = go_string_fields(
                raw,
                &["file", "status"],
                r#"struct { File string "json:\"file\""; Status string "json:\"status\"" }"#,
            )?;
            let [file, status] = args.as_slice() else {
                unreachable!()
            };
            if file.is_empty() || status.is_empty() {
                return Err("file and status are required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            if !matches!(
                status.as_str(),
                "open" | "paid" | "submitted" | "done" | "needs_review" | "waiting_for_reply"
            ) {
                return Err(format!(
                    "invalid status {} (valid: open, paid, submitted, done, needs_review, waiting_for_reply)",
                    symdesk_vault::go_quote(status)
                ));
            }
            let absolute = secure_path(&root, file).map_err(|e| e.to_string())?;
            let _ = HistoryStore::new(&root).snapshot(file);
            symdesk_vault::set_property(&root, file, "status", status)
                .map_err(|e| e.to_string())?;
            let title = index_file(&absolute, &mut sidecar, None)?;
            let _ = append_activity(
                &root,
                "status_changed",
                file,
                &title,
                &format!("status set to {status}"),
            );
            Ok(json!({"status":"updated", "file":file, "new_status":status}))
        }
        "notebook_create" => {
            let args = go_string_fields(
                raw,
                &["title", "description"],
                r#"struct { Title string "json:\"title\""; Description string "json:\"description\"" }"#,
            )?;
            if args[0].is_empty() {
                return Err("title is required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            let notebook = symdesk_vault::new_notebook(&root, &args[0], &args[1])
                .map_err(|e| e.to_string())?;
            let absolute = secure_path(&root, &notebook.path).map_err(|e| e.to_string())?;
            index_file(&absolute, &mut sidecar, None)?;
            encoded_notebook(&notebook)
        }
        "notebook_add_source" | "notebook_remove_source" => {
            let args = go_string_fields(
                raw,
                &["notebook", "path"],
                r#"struct { Notebook string "json:\"notebook\""; Path string "json:\"path\"" }"#,
            )?;
            if args[0].is_empty() || args[1].is_empty() {
                return Err("notebook and path are required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            let notebook = symdesk_vault::load_notebook(&root, &args[0])?;
            let _ = HistoryStore::new(&root).snapshot(&notebook.path);
            let notebook = if name == "notebook_add_source" {
                symdesk_vault::add_notebook_source(&root, &notebook.path, &args[1])
            } else {
                symdesk_vault::remove_notebook_source(&root, &notebook.path, &args[1])
            }
            .map_err(|e| e.to_string())?;
            let absolute = secure_path(&root, &notebook.path).map_err(|e| e.to_string())?;
            index_file(&absolute, &mut sidecar, None)?;
            encoded_notebook(&notebook)
        }
        _ => Err(format!("Unknown tool: {name}")),
    }
}

fn encoded_notebook(notebook: &symdesk_vault::Notebook) -> Result<Value, String> {
    serde_json::to_string(notebook)
        .map(|text| Value::String(crate::go_escape_json(text)))
        .map_err(|e| e.to_string())
}

fn index_file(
    path: &std::path::Path,
    sidecar: &mut symdesk_index::Sidecar,
    hash: Option<&str>,
) -> Result<String, String> {
    let bytes = std::fs::read(path).map_err(|e| io_error("open", path, &e))?;
    let mut document =
        symdesk_vault::parse_bytes(&path.to_string_lossy(), &bytes).map_err(|e| e.to_string())?;
    if let Some(hash) = hash {
        document.sha256 = hash.to_owned();
    }
    let modified = std::fs::metadata(path)
        .and_then(|m| m.modified())
        .map_err(|e| e.to_string())?;
    let nanos = modified
        .duration_since(std::time::UNIX_EPOCH)
        .map_err(|e| e.to_string())?
        .as_nanos();
    let indexed = IndexedDocument::from_vault(
        &document,
        Some(i64::try_from(nanos).map_err(|e| e.to_string())?),
    )
    .map_err(|e| e.to_string())?;
    sidecar
        .index_document(&indexed)
        .map_err(|e| e.to_string())?;
    Ok(document.title)
}

fn io_error(operation: &str, path: &std::path::Path, error: &std::io::Error) -> String {
    let text = if error.kind() == std::io::ErrorKind::NotFound {
        "no such file or directory".to_owned()
    } else {
        error.to_string()
    };
    format!("{operation} {}: {text}", path.display())
}
