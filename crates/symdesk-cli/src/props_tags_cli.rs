//! `symdesk props get|edit` and `symdesk tags rename|merge|delete`
//! (Go `cmd/symdesk/props.go`, `tags.go`, `internal/service/tags.go`).
use std::{path::Path, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use symdesk_index::Sidecar;
use symdesk_vault::{
    rewrite_document_tags_and_body, rewrite_inline_tags, secure_path, set_frontmatter_value,
    walk_markdown,
};

use crate::{
    mutate_cli::{parse, reindex, snapshot},
    service_cli::{go_string_map, with_deps},
    write_go_json, write_stdout,
};

fn positions() -> Arg {
    Arg::new("args").num_args(0..)
}

pub fn props_cli() -> Command {
    Command::new("props")
        .subcommand(Command::new("get").arg(positions()))
        .subcommand(Command::new("edit").arg(positions()))
}

pub fn tags_cli() -> Command {
    Command::new("tags")
        .subcommand(Command::new("rename").arg(positions()))
        .subcommand(Command::new("merge").arg(positions()))
        .subcommand(Command::new("delete").arg(positions()))
}

fn values(command: &ArgMatches) -> Vec<String> {
    command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .cloned()
        .collect()
}

pub fn run_props(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let Some((operation, command)) = command.subcommand() else {
        return ExitCode::SUCCESS;
    };
    let args = values(command);
    with_deps(vault, output_json, |root, sidecar| match operation {
        "get" => props_get(root, sidecar, &args[0], output_json),
        _ => props_edit(root, sidecar, &args, output_json),
    })
}

fn props_get(
    root: &Path,
    sidecar: &mut Sidecar,
    file: &str,
    output_json: bool,
) -> Result<ExitCode, String> {
    let path = secure_path(root, file).map_err(|error| error.to_string())?;
    let key = symdesk_index::document_storage_key(&path).map_err(|error| error.to_string())?;
    let props = sidecar
        .get_properties(&key.to_string_lossy())
        .map_err(|error| error.to_string())?;
    Ok(if output_json {
        write_go_json(&props)
    } else {
        write_stdout(format!("{}\n", go_string_map(props.iter())))
    })
}

fn props_edit(
    root: &Path,
    sidecar: &mut Sidecar,
    args: &[String],
    output_json: bool,
) -> Result<ExitCode, String> {
    let (file, key, value) = (&args[0], &args[1], &args[2]);
    if key == "asn" {
        return Err("use \"symdesk doc asn <file> <next|N>\" to assign an ASN safely".to_owned());
    }
    let path = secure_path(root, file).map_err(|error| error.to_string())?;
    snapshot(root, &path);
    set_frontmatter_value(&path, key, &noyalib::Value::from(value.clone()))
        .map_err(|error| error.to_string())?;
    reindex(sidecar, &path)?;
    let status = std::collections::BTreeMap::from([("status".to_owned(), "updated".to_owned())]);
    Ok(if output_json {
        write_go_json(&status)
    } else {
        write_stdout(format!("{}\n", go_string_map(status.iter())))
    })
}

#[derive(Serialize)]
struct TagResult {
    file: String,
    status: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

pub fn run_tags(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let Some((operation, command)) = command.subcommand() else {
        return ExitCode::SUCCESS;
    };
    let args = values(command);
    with_deps(vault, output_json, |root, sidecar| {
        let results = match operation {
            "rename" => tags_rewrite(root, sidecar, &args[0], Some(&args[1]), false)?,
            "merge" => tags_rewrite(root, sidecar, &args[0], Some(&args[1]), true)?,
            _ => tags_rewrite(root, sidecar, &args[0], None, false)?,
        };
        Ok(render(&results, output_json))
    })
}

fn render(results: &[TagResult], output_json: bool) -> ExitCode {
    if output_json {
        return if results.is_empty() {
            write_stdout("null\n".to_owned())
        } else {
            write_go_json(&results)
        };
    }
    let (mut updated, mut skipped) = (0, 0);
    let mut text = String::new();
    for result in results {
        match result.status {
            "updated" => updated += 1,
            "skipped" => skipped += 1,
            "error" => text.push_str(&format!("error: {}: {}\n", result.file, result.error)),
            _ => {}
        }
    }
    text.push_str(&format!(
        "Tag update complete. {updated} file(s) updated, {skipped} skipped.\n"
    ));
    write_stdout(text)
}

fn same_tag(candidate: &str, wanted: &str) -> bool {
    // Go `strings.EqualFold` on the trimmed candidate.
    candidate.trim().to_lowercase() == wanted.to_lowercase()
}

fn tags_rewrite(
    root: &Path,
    sidecar: &mut Sidecar,
    from: &str,
    to: Option<&str>,
    merge: bool,
) -> Result<Vec<TagResult>, String> {
    let from = from.trim();
    let to = to.map(str::trim);
    if from.is_empty() {
        return Err(if to.is_some() {
            "source tag must not be empty"
        } else {
            "tag must not be empty"
        }
        .to_owned());
    }
    if let Some(to) = to {
        if to.is_empty() {
            return Err("target tag must not be empty".to_owned());
        }
        if from == to {
            return Err("source and target tag are identical".to_owned());
        }
    }

    let mut results = Vec::new();
    let files = walk_markdown(root).map_err(|error| error.to_string())?;
    let canonical = std::fs::canonicalize(root).unwrap_or_else(|_| root.to_path_buf());
    for relative_path in files {
        let relative = relative_path.to_string_lossy().replace('\\', "/");
        let path = canonical.join(&relative_path);
        let failed = |error: String| TagResult {
            file: relative.clone(),
            status: "error",
            error,
        };
        let document = match parse(&path) {
            Ok(document) => document,
            Err(error) => {
                results.push(failed(error));
                continue;
            }
        };
        let mut mutate_frontmatter = |tags: &[String]| -> Option<Vec<String>> {
            match (to, merge) {
                (Some(to), false) => {
                    // rename: in-place replacement, then de-duplicate.
                    let mut changed = false;
                    let renamed: Vec<String> = tags
                        .iter()
                        .map(|tag| {
                            if same_tag(tag, from) {
                                changed = true;
                                to.to_owned()
                            } else {
                                tag.clone()
                            }
                        })
                        .collect();
                    changed.then(|| {
                        let mut unique: Vec<String> = Vec::new();
                        for tag in renamed {
                            if !unique.iter().any(|seen| same_tag(seen, &tag)) {
                                unique.push(tag);
                            }
                        }
                        unique
                    })
                }
                (Some(to), true) => {
                    let kept: Vec<String> = tags
                        .iter()
                        .filter(|tag| !same_tag(tag, from))
                        .cloned()
                        .collect();
                    if kept.len() == tags.len() {
                        return None;
                    }
                    let mut merged = kept;
                    if !merged.iter().any(|tag| same_tag(tag, to)) {
                        merged.push(to.to_owned());
                    }
                    Some(merged)
                }
                (None, _) => {
                    let kept: Vec<String> = tags
                        .iter()
                        .filter(|tag| !same_tag(tag, from))
                        .cloned()
                        .collect();
                    (kept.len() != tags.len()).then_some(kept)
                }
            }
        };
        let mut mutate_body = |body: &str| -> Option<String> {
            let (rewritten, changed) = rewrite_inline_tags(body, &mut |tag: &str| {
                if same_tag(tag, from) {
                    match to {
                        Some(to) => (to.to_owned(), true),
                        None => (String::new(), false),
                    }
                } else {
                    (tag.to_owned(), true)
                }
            });
            changed.then_some(rewritten)
        };
        match rewrite_document_tags_and_body(
            &path,
            &document,
            &mut mutate_frontmatter,
            &mut mutate_body,
        ) {
            Err(error) => results.push(failed(error)),
            Ok(false) => results.push(TagResult {
                file: relative.clone(),
                status: "skipped",
                error: String::new(),
            }),
            Ok(true) => match reindex(sidecar, &path) {
                Ok(_) => results.push(TagResult {
                    file: relative.clone(),
                    status: "updated",
                    error: String::new(),
                }),
                Err(error) => results.push(failed(error)),
            },
        }
    }
    Ok(results)
}
