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
            | "desk_asset_store"
            | "desk_undo_task"
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
            symdesk_vault::set_property(&root, file, "status", status).map_err(
                |error| match error {
                    symdesk_vault::NoteError::Mutation(symdesk_vault::MutationError::Read {
                        source,
                    }) => format!(
                        "read file: {}",
                        symdesk_vault::go_path_error("open", &absolute, &source)
                    ),
                    other => other.to_string(),
                },
            )?;
            let title = index_file(&absolute, &mut sidecar, None)?;
            let _ = append_activity(
                &root,
                "status_changed",
                file,
                &title,
                &format!("status set to {status}"),
            );
            Ok(json!({"file":file, "new_status":status, "status":"updated"}))
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
        "desk_asset_store" => {
            let (args, encoded) = string_bool_fields(
                raw,
                &["data", "preferred_name", "extension"],
                "is_base64",
                r#"struct { Data string "json:\"data\""; PreferredName string "json:\"preferred_name\""; Extension string "json:\"extension\""; IsBase64 bool "json:\"is_base64\"" }"#,
            )?;
            if args[0].is_empty() {
                return Err("data is required".into());
            }
            let bytes = if encoded {
                decode_base64(&args[0])?
            } else {
                args[0].as_bytes().to_vec()
            };
            let (root, _sidecar) = open_sidecar(config)?;
            let path = store_asset(&root, &bytes, &args[1], &args[2])?;
            let filename = path.rsplit('/').next().unwrap_or(&path);
            let _ = append_activity(&root, "asset_added", &path, &args[1], "stored asset");
            Ok(
                json!({"markdown_link":format!("![{filename}]({})",path.replace(' ',"%20")),"path":path}),
            )
        }
        "desk_undo_task" => {
            let args = go_string_fields(
                raw,
                &["task_id"],
                r#"struct { TaskID string "json:\"task_id\"" }"#,
            )?;
            if args[0].is_empty() {
                return Err("task_id is required".into());
            }
            let (root, mut sidecar) = open_sidecar(config)?;
            let checkpoint = symdesk_index::checkpoint_undo(&root, &mut sidecar, &args[0])
                .map_err(|e| match e {
                    symdesk_index::HistorySyncError::History(symdesk_vault::HistoryError::Io(
                        error,
                    )) => {
                        let relative = std::path::Path::new(".symdesk/history/checkpoints")
                            .join(format!("{}.json", args[0]));
                        symdesk_vault::go_path_error("openat", &relative, &error)
                    }
                    other => other.to_string(),
                })?;
            let manifest = root
                .join(".symdesk/history/checkpoints")
                .join(format!("{}.json", args[0]));
            let skipped_was_null = std::fs::read(manifest)
                .ok()
                .and_then(|bytes| serde_json::from_slice::<Value>(&bytes).ok())
                .is_none_or(|value| value.get("skipped").is_none_or(Value::is_null));
            Ok(
                json!({"deleted":checkpoint.new_files.len(),"partial":checkpoint.partial(),"restored":checkpoint.files.len(),"skipped":if checkpoint.skipped.is_empty() && skipped_was_null {Value::Null} else {json!(checkpoint.skipped)},"status":"undone","task_id":checkpoint.task_id}),
            )
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

fn string_bool_fields(
    raw: Option<&str>,
    names: &[&str],
    bool_name: &str,
    target: &str,
) -> Result<(Vec<String>, bool), String> {
    let raw = raw.ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    if raw == "null" {
        return Ok((vec![String::new(); names.len()], false));
    }
    if !raw.starts_with('{') {
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type {target}",
            super::go_json_type(raw)
        ));
    }
    let mut values = vec![String::new(); names.len()];
    let mut flag = false;
    for (key, value) in super::go_raw_fields(raw)?.0 {
        let text = value.get();
        if text == "null" {
            continue;
        }
        if let Some(index) = names
            .iter()
            .position(|name| symdesk_vault::dataset::go_equal_fold(&key, name))
        {
            if !text.starts_with('"') {
                return Err(super::go_string_field_type_error(
                    super::go_json_type(text),
                    names[index],
                ));
            }
            values[index] = serde_json::from_str(text).map_err(|e| e.to_string())?;
        } else if symdesk_vault::dataset::go_equal_fold(&key, bool_name) {
            if !matches!(text, "true" | "false") {
                return Err(format!(
                    "json: cannot unmarshal {} into Go struct field .{bool_name} of type bool",
                    super::go_json_type(text)
                ));
            }
            flag = text == "true";
        }
    }
    Ok((values, flag))
}

// Go 1.26 encoding/base64.decodeQuantum: retain raw input offsets across
// CR/LF, distinguish missing padding from trailing garbage, allow pad bits.
fn decode_base64(data: &str) -> Result<Vec<u8>, String> {
    let src = data.as_bytes();
    let mut si = 0;
    let mut output = Vec::new();
    let corrupt = |index| format!("decode base64 data: illegal base64 data at input byte {index}");
    loop {
        let mut digits = [0u8; 4];
        let mut j = 0;
        let mut length = 4;
        while j < 4 {
            if si == src.len() {
                if j == 0 {
                    return Ok(output);
                }
                return Err(corrupt(si - j));
            }
            let byte = src[si];
            si += 1;
            let value = match byte {
                b'A'..=b'Z' => Some(byte - b'A'),
                b'a'..=b'z' => Some(byte - b'a' + 26),
                b'0'..=b'9' => Some(byte - b'0' + 52),
                b'+' => Some(62),
                b'/' => Some(63),
                _ => None,
            };
            if let Some(value) = value {
                digits[j] = value;
                j += 1;
                continue;
            }
            if matches!(byte, b'\r' | b'\n') {
                continue;
            }
            if byte != b'=' || j < 2 {
                return Err(corrupt(si - 1));
            }
            if j == 2 {
                while si < src.len() && matches!(src[si], b'\r' | b'\n') {
                    si += 1;
                }
                if si == src.len() {
                    return Err(corrupt(si));
                }
                if src[si] != b'=' {
                    return Err(corrupt(si - 1));
                }
                si += 1;
            }
            while si < src.len() && matches!(src[si], b'\r' | b'\n') {
                si += 1;
            }
            if si < src.len() {
                return Err(corrupt(si));
            }
            length = j;
            break;
        }
        let value = (u32::from(digits[0]) << 18)
            | (u32::from(digits[1]) << 12)
            | (u32::from(digits[2]) << 6)
            | u32::from(digits[3]);
        let bytes = value.to_be_bytes();
        output.extend_from_slice(&bytes[1..length]);
    }
}

fn store_asset(
    root: &std::path::Path,
    bytes: &[u8],
    preferred: &str,
    extension: &str,
) -> Result<String, String> {
    use std::io::Write as _;
    let dir = secure_path(root, "assets").map_err(|e| format!("resolve assets folder: {e}"))?;
    std::fs::create_dir_all(&dir).map_err(|e| format!("create assets directory: {e}"))?;
    let mut extension = extension.strip_prefix('.').unwrap_or(extension).to_owned();
    let base = if preferred.is_empty() {
        let instant = OffsetDateTime::now_utc();
        let now = instant.to_offset(crate::local_offset_at(instant));
        format!(
            "pasted-{}-{:02}{:02}{:02}",
            now.date(),
            now.hour(),
            now.minute(),
            now.second()
        )
    } else {
        let last = preferred.rsplit('/').next().unwrap_or(preferred);
        let ext = last.rfind('.').map_or("", |i| &last[i..]);
        if extension.is_empty() {
            extension = ext.strip_prefix('.').unwrap_or(ext).to_owned();
        }
        preferred.strip_suffix(ext).unwrap_or(preferred).to_owned()
    };
    let base = base
        .chars()
        .map(|c| {
            if matches!(c, '/' | '\\' | ':') || c.is_control() {
                '-'
            } else {
                c
            }
        })
        .collect::<String>();
    let base = if base.trim().is_empty() {
        "pasted-image"
    } else {
        base.trim()
    };
    let mut counter = 1;
    let (relative, path) = loop {
        let stem = if counter == 1 {
            base.to_owned()
        } else {
            format!("{base}-{counter}")
        };
        let name = if extension.is_empty() {
            stem
        } else {
            format!("{stem}.{extension}")
        };
        let relative = format!("assets/{name}");
        let path = secure_path(root, &relative).map_err(|e| e.to_string())?;
        if !path.exists() {
            break (relative, path);
        }
        counter += 1;
    };
    static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let temporary = dir.join(format!(
        ".symdesk-frontmatter-{}-{}.tmp",
        std::process::id(),
        NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    let mut created = false;
    let result = (|| {
        let mut options = std::fs::OpenOptions::new();
        options.create_new(true).write(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let mut file = options
            .open(&temporary)
            .map_err(|e| format!("create temp file: {e}"))?;
        created = true;
        file.write_all(bytes)
            .map_err(|e| format!("write temp file: {e}"))?;
        file.sync_all()
            .map_err(|e| format!("sync temp file: {e}"))?;
        drop(file);
        std::fs::rename(&temporary, &path).map_err(|e| format!("rename temp file: {e}"))
    })();
    if let Err(error) = result {
        if created {
            let _ = std::fs::remove_file(temporary);
        }
        return Err(format!("write asset file: {error}"));
    }
    Ok(relative)
}
