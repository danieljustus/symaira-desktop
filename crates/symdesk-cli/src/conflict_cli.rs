//! Explicit sync-conflict resolution, retaining Go's explicit-path contract.
use crate::{emit_error, resolve_vault, write_go_json, write_stdout};
use clap::{Arg, ArgMatches, Command};
use serde_json::json;
use std::{fs, io::Write, path::Path, process::ExitCode};
use symdesk_index::{IndexedDocument, open_for_vault};
use symdesk_vault::{derive_original_path, go_path_error, parse_bytes};

pub fn cli() -> Command {
    Command::new("conflict").subcommand(
        Command::new("resolve")
            .arg(Arg::new("args").num_args(0..))
            .arg(
                Arg::new("action")
                    .long("action")
                    .num_args(1)
                    .overrides_with("action"),
            ),
    )
}
fn delete_retrieval(root: &Path, path: &Path) -> Result<(), String> {
    let environment = symdesk_core::config::environment_snapshot()?;
    let cwd = std::env::current_dir().map_err(|e| e.to_string())?;
    let temp = std::env::temp_dir();
    let source = symdesk_index::store_retrieval_path_for_vault("", &environment, &cwd, &temp)
        .map_err(|e| e.to_string())?;
    let target = symdesk_index::store_retrieval_path_for_vault(
        &root.to_string_lossy(),
        &environment,
        &cwd,
        &temp,
    )
    .map_err(|e| e.to_string())?;
    let migrated = source != target && source.is_file() && !target.exists();
    let retrieval =
        symdesk_index::open_retrieval_for_vault(&root.to_string_lossy(), &environment, &cwd, &temp)
            .map_err(|e| e.to_string())?;
    if migrated {
        let now = time::OffsetDateTime::now_utc();
        let now = now.to_offset(crate::local_offset_at(now));
        let format = time::format_description::parse_borrowed::<2>(
            "[year]-[month]-[day]T[hour]:[minute]:[second].[subsecond digits:3]",
        )
        .expect("static slog timestamp");
        let stamp = now.format(&format).unwrap_or_default();
        let zone = if now.offset() == time::UtcOffset::UTC {
            "Z".to_owned()
        } else {
            now.format(
                &time::format_description::parse_borrowed::<2>(
                    "[offset_hour sign:mandatory]:[offset_minute]",
                )
                .expect("static offset format"),
            )
            .unwrap_or_default()
        };
        let log_path = |p: &Path| {
            let p = p.to_string_lossy();
            if p.chars().any(char::is_whitespace) {
                symdesk_vault::go_quote(&p)
            } else {
                p.into_owned()
            }
        };
        eprintln!(
            "time={stamp}{zone} level=INFO msg=\"seeded per-vault retrieval index from standalone store\" standalone_path={} new_path={}",
            log_path(&source),
            log_path(&target)
        );
    }
    // Retrieval indexes canonical local sources, unlike sidecar lexical keys.
    // Resolve the surviving parent after removal (e.g. macOS /var -> /private/var).
    retrieval
        .delete_document(&retrieval_document_key(path))
        .map_err(|e| e.to_string())
}

fn retrieval_document_key(path: &Path) -> String {
    crate::index_cli::canonical_local_source(&path.to_string_lossy())
}

#[test]
#[cfg(unix)]
fn removed_conflict_retrieval_key_resolves_parent_alias() {
    let base = std::env::temp_dir().join(format!(
        "symdesk-conflict-key-{}-{}",
        std::process::id(),
        time::OffsetDateTime::now_utc().unix_timestamp_nanos()
    ));
    fs::create_dir_all(base.join("real")).unwrap();
    std::os::unix::fs::symlink(base.join("real"), base.join("alias")).unwrap();
    let path = base.join("alias/note conflicted copy.md");
    fs::write(&path, "conflict").unwrap();
    let indexed = fs::canonicalize(&path)
        .unwrap()
        .to_string_lossy()
        .into_owned();
    fs::remove_file(&path).unwrap();
    assert_ne!(path.to_string_lossy(), indexed);
    assert_eq!(retrieval_document_key(&path), indexed);
    fs::remove_dir_all(base).unwrap();
}

fn remove(path: &Path) -> std::io::Result<()> {
    if fs::symlink_metadata(path)?.is_dir() {
        fs::remove_dir(path)
    } else {
        fs::remove_file(path)
    }
}
pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let Some(("resolve", command)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let args: Vec<_> = command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .collect();
    if args.len() != 1 {
        return emit_error(
            format!("accepts 1 arg(s), received {}", args.len()),
            output_json,
        );
    }
    let Some(action) = command.get_one::<String>("action") else {
        return emit_error(
            "required flag(s) \"action\" not set".to_owned(),
            output_json,
        );
    };
    if !matches!(action.as_str(), "keep-mine" | "keep-theirs") {
        return emit_error(
            "invalid action: must be keep-mine or keep-theirs".to_owned(),
            output_json,
        );
    }
    let result = (|| -> Result<ExitCode, String> {
        let root = resolve_vault(vault).map_err(|e| {
            if e.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                crate::index_cli::index_vault_error(vault, &e)
            }
        })?;
        let mut sidecar = open_for_vault(&root).map_err(|e| e.to_string())?;
        let result = (|| -> Result<ExitCode, String> {
            let argument = Path::new(args[0]);
            let path = if argument.is_absolute() {
                argument.to_path_buf()
            } else {
                crate::lexical_clean(&root.join(argument))
            };
            fs::metadata(&path).map_err(|e| {
                format!(
                    "conflict file not found: {}",
                    go_path_error("stat", &path, &e)
                )
            })?;
            let original = derive_original_path(&path.to_string_lossy());
            let original = Path::new(&original);
            if action == "keep-theirs" {
                let content = fs::read(&path).map_err(|e| {
                    format!(
                        "failed to read conflict file: {}",
                        go_path_error("open", &path, &e)
                    )
                })?;
                let mut options = fs::OpenOptions::new();
                options.write(true).create(true).truncate(true);
                #[cfg(unix)]
                {
                    use std::os::unix::fs::OpenOptionsExt;
                    options.mode(0o600);
                }
                let mut file = options.open(original).map_err(|e| {
                    format!(
                        "failed to write original file: {}",
                        go_path_error("open", original, &e)
                    )
                })?;
                file.write_all(&content).map_err(|e| {
                    format!(
                        "failed to write original file: {}",
                        go_path_error("write", original, &e)
                    )
                })?;
            }
            remove(&path).map_err(|e| {
                format!(
                    "failed to remove conflict file: {}",
                    go_path_error("remove", &path, &e)
                )
            })?;
            if sidecar.delete_document(&path.to_string_lossy()).is_ok() {
                let _ = delete_retrieval(&root, &path);
            }
            if action == "keep-theirs" {
                // Re-index is best-effort in the Go command, including malformed
                // replacement content and explicitly selected external paths.
                if let Ok(bytes) = fs::read(original)
                    && let Ok(doc) = parse_bytes(&original.to_string_lossy(), &bytes)
                    && let Ok(indexed) = IndexedDocument::from_vault(
                        &doc,
                        fs::metadata(original)
                            .and_then(|m| m.modified())
                            .ok()
                            .map(time::OffsetDateTime::from)
                            .and_then(|t| i64::try_from(t.unix_timestamp_nanos()).ok()),
                    )
                {
                    let _ = sidecar.index_document(&indexed);
                }
            }
            Ok(if output_json {
                write_go_json(
                    &json!({"action":action,"conflict_path":path,"original_path":original,"status":"resolved"}),
                )
            } else {
                write_stdout(format!(
                    "map[action:{action} conflict_path:{} original_path:{} status:resolved]\n",
                    path.display(),
                    original.display()
                ))
            })
        })();
        if let Err(e) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {e}");
        }
        result
    })();
    match result {
        Ok(exit) => exit,
        Err(e) => emit_error(e, output_json),
    }
}
