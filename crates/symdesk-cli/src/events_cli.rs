//! `symdesk events`: Go `cmd/symdesk/events.go`. Watches the vault tree and
//! prints one NDJSON line per debounced `.md` change on stdout.
//!
//! Lifecycle (CLI-007): SIGINT/SIGTERM and stdin EOF end the command with exit
//! status 0 after the watcher and the sidecar are closed; a failed stdout
//! write ends the process by `SIGPIPE`; an externally delivered `SIGPIPE` is
//! ignored.
//!
//! Declared differences from Go:
//! * Go's debounce loop unlocks an unheld mutex on the first filesystem event
//!   (`fatal error: sync: unlock of unlocked mutex`, exit 2). That defect is
//!   not reproduced.
//! * The Go inbox and mail watchers (ingest, RUST-010) are not started here;
//!   the inbox directory is still created so the vault tree matches.
//! * A removed document is deleted from the sidecar only; Go also asks the
//!   optional retrieval index, which `events` does not open.

use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::ExitCode,
    sync::mpsc,
    time::{Duration, Instant, SystemTime},
};

use clap::Command;
use notify::{EventKind, RecursiveMode, Watcher, event::ModifyKind};
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_index::{IndexedDocument, Sidecar, open_for_vault};

use crate::lifecycle;

const DEBOUNCE_TICK: Duration = Duration::from_millis(500);
const DEBOUNCE_AGE: Duration = Duration::from_millis(500);

const CREATE: u8 = 1;
const WRITE: u8 = 2;
const REMOVE: u8 = 4;
const RENAME: u8 = 8;
const CHMOD: u8 = 16;

pub(crate) fn cli() -> Command {
    Command::new("events")
}

struct Pending {
    ops: u8,
    seen: Instant,
    wall: SystemTime,
}

/// Go `accumulateDebounce`: OR the operation bits and keep the first stamp, so
/// a create followed by writes still resolves to a create.
fn accumulate(map: &mut BTreeMap<String, Pending>, path: String, ops: u8) {
    map.entry(path)
        .and_modify(|pending| pending.ops |= ops)
        .or_insert_with(|| Pending {
            ops,
            seen: Instant::now(),
            wall: SystemTime::now(),
        });
}

/// Go `time.RFC3339` of the first event: UTC, whole seconds.
fn rfc3339_seconds(time: SystemTime) -> String {
    time::OffsetDateTime::from(time)
        .replace_nanosecond(0)
        .ok()
        .and_then(|stamp| {
            stamp
                .format(&time::format_description::well_known::Rfc3339)
                .ok()
        })
        .unwrap_or_default()
}

fn parse_for_index(path: &Path) -> Result<symdesk_vault::Document, String> {
    let bytes = symdesk_vault::go_read(path)
        .map_err(|(op, error)| symdesk_vault::go_path_error(op, path, &error))?;
    let mut document =
        symdesk_vault::parse_bytes(&path.to_string_lossy(), &bytes).map_err(|e| e.to_string())?;
    document.path = symdesk_index::document_storage_key(path)
        .map_err(|e| e.to_string())?
        .to_string_lossy()
        .into_owned();
    Ok(document)
}

/// Go `processEvent`: names the operation, performs the index side effect and
/// writes the NDJSON line for markdown files.
fn process_event(
    path: &str,
    ops: u8,
    stamp: SystemTime,
    database: &mut Sidecar,
    add_watch: &mut dyn FnMut(&Path) -> Result<(), String>,
    out: &mut dyn Write,
) {
    let mut name = "";
    if ops & CREATE != 0 {
        name = "file_added";
        if fs::metadata(path).is_ok_and(|metadata| metadata.is_dir())
            && let Err(error) = add_watch(Path::new(path))
        {
            eprintln!("failed to watch created directory {path}: {error}");
        }
    } else if ops & WRITE != 0 {
        name = "file_changed";
    } else if ops & (REMOVE | RENAME) != 0 {
        name = "file_removed";
        if let Ok(key) = symdesk_index::document_storage_key(Path::new(path)) {
            let _ = database.delete_document(&key.to_string_lossy());
        }
    }
    if name.is_empty() || Path::new(path).extension().is_none_or(|ext| ext != "md") {
        return;
    }
    if name != "file_removed"
        && let Ok(document) = parse_for_index(Path::new(path))
        && let Ok(indexed) = IndexedDocument::from_vault(&document, None)
    {
        let _ = database.index_document(&indexed);
        name = "index_updated";
    }
    let line = format!(
        "{{\"event\":{},\"path\":{},\"ts\":{}}}\n",
        serde_json::Value::String(name.to_owned()),
        serde_json::Value::String(path.to_owned()),
        serde_json::Value::String(rfc3339_seconds(stamp)),
    );
    if let Err(error) = out.write_all(line.as_bytes()).and_then(|()| out.flush()) {
        eprintln!("failed to emit filesystem event for {path}: {error}");
    }
}

/// Go `flushDebounce`: process every event older than the debounce window.
fn flush_debounce(
    map: &mut BTreeMap<String, Pending>,
    database: &mut Sidecar,
    add_watch: &mut dyn FnMut(&Path) -> Result<(), String>,
    out: &mut dyn Write,
) {
    let matured: Vec<String> = map
        .iter()
        .filter(|(_, pending)| pending.seen.elapsed() >= DEBOUNCE_AGE)
        .map(|(path, _)| path.clone())
        .collect();
    for path in matured {
        if let Some(pending) = map.remove(&path) {
            process_event(&path, pending.ops, pending.wall, database, add_watch, out);
        }
    }
}

fn operations(kind: &EventKind) -> u8 {
    match kind {
        EventKind::Create(_) => CREATE,
        EventKind::Remove(_) => REMOVE,
        EventKind::Modify(ModifyKind::Name(_)) => RENAME,
        EventKind::Modify(ModifyKind::Metadata(_)) => CHMOD,
        EventKind::Modify(_) => WRITE,
        _ => 0,
    }
}

fn watch_tree(
    watcher: &mut notify::RecommendedWatcher,
    directory: &Path,
    root: &Path,
) -> Result<(), String> {
    let mut stack = vec![directory.to_path_buf()];
    while let Some(current) = stack.pop() {
        let hidden = current
            .file_name()
            .is_some_and(|name| name.to_string_lossy().starts_with('.'));
        if hidden && current != root {
            continue;
        }
        watcher
            .watch(&current, RecursiveMode::NonRecursive)
            .map_err(|error| error.to_string())?;
        let entries = fs::read_dir(&current).map_err(|error| error.to_string())?;
        for entry in entries.flatten() {
            if entry.file_type().is_ok_and(|kind| kind.is_dir()) {
                stack.push(entry.path());
            }
        }
    }
    Ok(())
}

fn inbox_directory(root: &Path) -> PathBuf {
    let configured = symdesk_core::config::environment_snapshot()
        .ok()
        .and_then(|environment| {
            let path = symdesk_core::config::global_path(&environment);
            let text = fs::read_to_string(path).ok();
            symdesk_core::config::load(text.as_deref(), &environment).ok()
        })
        .map(|config| config.inbox)
        .unwrap_or_default();
    if configured.is_empty() {
        root.join("inbox_watch")
    } else {
        PathBuf::from(configured)
    }
}

pub(crate) fn run(vault: Option<&str>) -> ExitCode {
    let root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return crate::emit_error(error, false),
    };
    let mut database = match open_for_vault(&root) {
        Ok(database) => database,
        Err(error) => return crate::emit_error(error.to_string(), false),
    };
    let result = watch(&root, &mut database);
    if let Err(error) = database.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    match result {
        Ok(()) => crate::process_exit(CoreExitCode::Ok),
        Err(error) => crate::emit_error(error, false),
    }
}

fn watch(root: &Path, database: &mut Sidecar) -> Result<(), String> {
    let (sender, receiver) = mpsc::channel();
    let mut watcher = notify::recommended_watcher(move |event| {
        let _ = sender.send(event);
    })
    .map_err(|error| error.to_string())?;

    // Go creates the inbox directory before it walks the vault.
    let inbox = inbox_directory(root);
    if let Err(error) = fs::create_dir_all(&inbox) {
        eprintln!("failed to start inbox watcher: {error}");
    }
    watch_tree(&mut watcher, root, root)?;

    lifecycle::install_shutdown_handlers().map_err(|error| error.to_string())?;
    let stdin_closed = lifecycle::watch_stdin_closed();
    let mut stdout = lifecycle::SigpipeWriter(std::io::stdout());
    let mut pending: BTreeMap<String, Pending> = BTreeMap::new();
    let mut next_tick = Instant::now() + DEBOUNCE_TICK;
    loop {
        if lifecycle::shutdown_requested() || stdin_closed.try_recv().is_ok() {
            return Ok(());
        }
        let wait = next_tick
            .saturating_duration_since(Instant::now())
            .min(Duration::from_millis(50));
        match receiver.recv_timeout(wait) {
            Ok(Ok(event)) => {
                let ops = operations(&event.kind);
                if ops != 0 {
                    for path in event.paths {
                        accumulate(&mut pending, path.to_string_lossy().into_owned(), ops);
                    }
                }
            }
            Ok(Err(error)) => eprintln!("watcher error: {error}"),
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => return Ok(()),
        }
        if Instant::now() >= next_tick {
            next_tick = Instant::now() + DEBOUNCE_TICK;
            flush_debounce(
                &mut pending,
                database,
                &mut |directory| {
                    watcher
                        .watch(directory, RecursiveMode::NonRecursive)
                        .map_err(|error| error.to_string())
                },
                &mut stdout,
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use serde::Deserialize;

    use super::*;

    #[derive(Deserialize)]
    struct EventCase {
        name: String,
        #[serde(default)]
        files: BTreeMap<String, String>,
        #[serde(default)]
        remove: String,
        #[serde(default)]
        mkdir: String,
        ops: Vec<String>,
        path: String,
        output: String,
        listed: Vec<String>,
    }

    #[derive(Deserialize)]
    struct Fixture {
        event_cases: Vec<EventCase>,
    }

    /// The Go-recorded outcome of `processEvent` for every operation kind.
    #[test]
    fn process_event_matches_the_go_recording() {
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../testdata/port/cli/signals-process.json"
        ))
        .expect("decode Go signals fixture");
        assert!(!fixture.event_cases.is_empty());
        for case in fixture.event_cases {
            let scratch = std::env::temp_dir().join(format!(
                "symdesk-events-{}-{}",
                std::process::id(),
                case.name
            ));
            let _ = fs::remove_dir_all(&scratch);
            let vault = scratch.join("vault");
            fs::create_dir_all(&vault).unwrap();
            let vault = fs::canonicalize(&vault).unwrap();
            let mut database = open_for_vault(&vault).unwrap();
            for (name, content) in &case.files {
                fs::write(vault.join(name), content).unwrap();
            }
            if !case.mkdir.is_empty() {
                fs::create_dir(vault.join(&case.mkdir)).unwrap();
            }
            if !case.remove.is_empty() {
                fs::remove_file(vault.join(&case.remove)).unwrap();
            }
            let ops = case.ops.iter().fold(0, |ops, name| {
                ops | match name.as_str() {
                    "create" => CREATE,
                    "write" => WRITE,
                    "remove" => REMOVE,
                    "rename" => RENAME,
                    "chmod" => CHMOD,
                    other => panic!("unknown op {other}"),
                }
            });
            let stamp = SystemTime::UNIX_EPOCH + Duration::from_secs(1_767_323_045);
            let mut out = Vec::new();
            let mut added = Vec::new();
            process_event(
                &vault.join(&case.path).to_string_lossy(),
                ops,
                stamp,
                &mut database,
                &mut |directory| {
                    added.push(directory.to_path_buf());
                    Ok(())
                },
                &mut out,
            );
            let output = String::from_utf8(out)
                .unwrap()
                .replace(&vault.to_string_lossy().into_owned(), "<VAULT>");
            assert_eq!(output, case.output, "{}", case.name);
            let mut listed: Vec<String> = database
                .list_files(&vault, "")
                .unwrap()
                .into_iter()
                .map(|document| {
                    format!(
                        "{} | {}",
                        document
                            .path
                            .replace(&vault.to_string_lossy().into_owned(), "<VAULT>"),
                        document.title
                    )
                })
                .collect();
            listed.sort();
            assert_eq!(listed, case.listed, "{}", case.name);
            if case.mkdir.is_empty() {
                assert!(added.is_empty(), "{}", case.name);
            } else {
                assert_eq!(added, [vault.join(&case.mkdir)], "{}", case.name);
            }
            let _ = fs::remove_dir_all(&scratch);
        }
    }

    #[test]
    fn debounce_keeps_the_first_stamp_and_ors_the_operations() {
        let mut map = BTreeMap::new();
        accumulate(&mut map, "a.md".into(), CREATE);
        let first = map["a.md"].seen;
        accumulate(&mut map, "a.md".into(), WRITE);
        assert_eq!(map["a.md"].ops, CREATE | WRITE);
        assert_eq!(map["a.md"].seen, first);
    }
}
