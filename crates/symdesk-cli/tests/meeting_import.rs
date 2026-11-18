//! `symdesk meeting available|import|refresh|segments|speakers` against the real
//! binary, a temp vault and a temp Meet store built from
//! `tests/fixtures/meet-store` (provenance in its README).
//!
//! Sources of the expected behaviour (see docs/rust-port/cli-additions.md and
//! `src/meeting_import_cli.rs`): the Swift caller (`DeskCore.swift`) fixes the
//! argument shapes and JSON field names; the Go implementation before commit
//! 686570a5 (`internal/service/meetings.go`) fixes note path, frontmatter,
//! transcript markers and refresh semantics; the Meet package fixes the store
//! layout and the Markdown transcript (`meet/Tests/Fixtures/exports/kitchen-sink.md`).
#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

use serde_json::Value;
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

const KITCHEN: &str = "11111111-1111-1111-1111-111111111111";
const COMPLETE: &str = "00000000-0000-0000-0000-000000000001";
const FIXTURES: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixtures");
const START: &str = "<!-- symmeet-transcript:start -->";
const END: &str = "<!-- symmeet-transcript:end -->";

struct Run {
    code: i32,
    stdout: String,
    stderr: String,
}

impl Run {
    fn json(&self) -> Value {
        serde_json::from_str(&self.stdout)
            .unwrap_or_else(|error| panic!("stdout is not JSON ({error}): {:?}", self.stdout))
    }
}

struct Sandbox {
    root: PathBuf,
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

fn copy_tree(from: &Path, to: &Path) {
    fs::create_dir_all(to).unwrap();
    for entry in fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let target = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_tree(&entry.path(), &target);
        } else {
            fs::copy(entry.path(), target).unwrap();
        }
    }
}

/// Every file below `dir` (relative path -> bytes); symlinks are recorded by target.
fn snapshot_tree(dir: &Path) -> BTreeMap<String, Vec<u8>> {
    fn walk(base: &Path, dir: &Path, out: &mut BTreeMap<String, Vec<u8>>) {
        let Ok(entries) = fs::read_dir(dir) else {
            return;
        };
        for entry in entries {
            let entry = entry.unwrap();
            let path = entry.path();
            let key = path.strip_prefix(base).unwrap().display().to_string();
            let kind = entry.file_type().unwrap();
            if kind.is_symlink() {
                out.insert(
                    key,
                    fs::read_link(&path)
                        .unwrap()
                        .display()
                        .to_string()
                        .into_bytes(),
                );
            } else if kind.is_dir() {
                out.insert(format!("{key}/"), Vec::new());
                walk(base, &path, out);
            } else {
                out.insert(key, fs::read(&path).unwrap());
            }
        }
    }
    let mut out = BTreeMap::new();
    walk(dir, dir, &mut out);
    out
}

impl Sandbox {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!(
            "symdesk-meeting-import-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(root.join("vault")).unwrap();
        fs::create_dir_all(root.join("home")).unwrap();
        fs::create_dir_all(root.join("bin")).unwrap();
        Self { root }
    }

    fn vault(&self) -> PathBuf {
        self.root.join("vault")
    }

    /// `$XDG_DATA_HOME/symmeet` of `run`.
    fn store(&self) -> PathBuf {
        self.root.join("xdg-data/symmeet")
    }

    /// Copies fixture recordings into the store.
    fn seed(&self, ids: &[&str]) {
        for id in ids {
            copy_tree(
                &Path::new(FIXTURES).join("meet-store/meetings").join(id),
                &self.store().join("meetings").join(id),
            );
        }
    }

    fn recording(&self, id: &str) -> PathBuf {
        self.store().join("meetings").join(id)
    }

    fn note(&self, id: &str) -> PathBuf {
        self.vault().join(format!("meetings/meeting-{id}.md"))
    }

    fn command(&self) -> Command {
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .current_dir(&self.root)
            .isolated_env()
            .env("HOME", self.root.join("home"))
            .env("USERPROFILE", self.root.join("home"))
            .env("XDG_CONFIG_HOME", self.root.join("config"))
            .env("XDG_DATA_HOME", self.root.join("xdg-data"))
            .env("XDG_CACHE_HOME", self.root.join("cache"))
            .env("TZ", "UTC")
            .env("LANG", "C")
            .env("LC_ALL", "C")
            .env("PATH", self.root.join("bin"));
        command
    }

    fn run_with(&self, mut command: Command, args: &[&str]) -> Run {
        let output = command.args(args).output().expect("run symdesk");
        Run {
            code: output.status.code().unwrap_or(-1),
            stdout: String::from_utf8(output.stdout).expect("utf-8 stdout"),
            stderr: String::from_utf8(output.stderr).expect("utf-8 stderr"),
        }
    }

    /// `symdesk <args> --vault <vault>`.
    fn run(&self, args: &[&str]) -> Run {
        let vault = self.vault();
        let mut all: Vec<&str> = args.to_vec();
        all.push("--vault");
        all.push(vault.to_str().unwrap());
        self.run_with(self.command(), &all)
    }

    fn rel(&self, id: &str) -> String {
        format!("meetings/meeting-{id}.md")
    }
}

fn between_markers(note: &str) -> &str {
    let start = note.find(START).expect("start marker") + START.len();
    let end = note.find(END).expect("end marker");
    &note[start..end]
}

fn keys(value: &Value) -> Vec<String> {
    let mut keys: Vec<String> = value.as_object().unwrap().keys().cloned().collect();
    keys.sort();
    keys
}

// ------------------------------------------------------------------ available

#[test]
fn available_on_a_missing_store_is_an_empty_list() {
    let sandbox = Sandbox::new();
    let run = sandbox.run(&["meeting", "available", "--json"]);
    assert_eq!(
        (run.code, run.stdout.as_str(), run.stderr.as_str()),
        (0, "[]\n", "")
    );
}

#[test]
fn available_on_an_empty_store_is_an_empty_list() {
    let sandbox = Sandbox::new();
    fs::create_dir_all(sandbox.store().join("meetings")).unwrap();
    let run = sandbox.run(&["meeting", "available", "--json"]);
    assert_eq!((run.code, run.stdout.as_str()), (0, "[]\n"));
    let text = sandbox.run(&["meeting", "available"]);
    assert_eq!(text.code, 0);
    assert_eq!(text.stdout.trim(), "No recordings to import.");
}

#[test]
fn available_json_shape_is_meeting_id_and_source_sorted_by_id() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN, COMPLETE]);
    let run = sandbox.run(&["meeting", "available", "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    let list = run.json();
    let list = list.as_array().unwrap();
    assert_eq!(list.len(), 2);
    assert_eq!(list[0]["meeting_id"], COMPLETE);
    assert_eq!(list[1]["meeting_id"], KITCHEN);
    for entry in list {
        assert_eq!(keys(entry), ["meeting_id", "source"]);
        assert_eq!(entry["source"], "imported");
    }
}

#[test]
fn available_text_is_one_line_per_recording() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let run = sandbox.run(&["meeting", "available"]);
    assert_eq!(run.code, 0);
    assert_eq!(run.stdout, format!("{KITCHEN}  imported\n"));
}

#[test]
fn available_honours_the_xdg_data_home_override_and_the_home_default() {
    let sandbox = Sandbox::new();
    let alt = sandbox.root.join("alt-data");
    copy_tree(
        &Path::new(FIXTURES)
            .join("meet-store/meetings")
            .join(KITCHEN),
        &alt.join("symmeet/meetings").join(KITCHEN),
    );
    let mut command = sandbox.command();
    command.env("XDG_DATA_HOME", &alt);
    let vault = sandbox.vault();
    let run = sandbox.run_with(
        command,
        &[
            "meeting",
            "available",
            "--json",
            "--vault",
            vault.to_str().unwrap(),
        ],
    );
    assert_eq!(run.json()[0]["meeting_id"], KITCHEN);

    // No XDG_DATA_HOME (or an empty one, like SymMeetPaths): $HOME/.local/share/symmeet.
    let home_store = sandbox
        .root
        .join("home/.local/share/symmeet/meetings")
        .join(COMPLETE);
    copy_tree(
        &Path::new(FIXTURES)
            .join("meet-store/meetings")
            .join(COMPLETE),
        &home_store,
    );
    for empty in [false, true] {
        let mut command = sandbox.command();
        if empty {
            command.env("XDG_DATA_HOME", "");
        } else {
            command.env_remove("XDG_DATA_HOME");
        }
        let run = sandbox.run_with(
            command,
            &[
                "meeting",
                "available",
                "--json",
                "--vault",
                vault.to_str().unwrap(),
            ],
        );
        assert_eq!(run.code, 0, "{}", run.stderr);
        assert_eq!(
            run.json()[0]["meeting_id"],
            COMPLETE,
            "empty override: {empty}"
        );
    }
}

#[test]
fn the_vault_is_resolved_like_the_other_meeting_commands() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    // SYMDESK_VAULT instead of --vault, and the global flags before the command.
    let mut command = sandbox.command();
    command.env("SYMDESK_VAULT", sandbox.vault());
    let run = sandbox.run_with(command, &["--json", "meeting", "import", KITCHEN]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    assert!(sandbox.note(KITCHEN).is_file());
    // No vault at all: the message of `meeting list`.
    let run = sandbox.run_with(sandbox.command(), &["meeting", "available"]);
    let list = sandbox.run_with(sandbox.command(), &["meeting", "list"]);
    assert_eq!(run.code, 1);
    assert_eq!(run.stderr, list.stderr);
}

// --------------------------------------------------------------------- import

#[test]
fn import_creates_a_vault_note_that_the_existing_meeting_commands_accept() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let before = snapshot_tree(&sandbox.store());
    let run = sandbox.run(&["meeting", "import", KITCHEN, "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    let result = run.json();
    assert_eq!(keys(&result), ["path", "status"]);
    assert_eq!(result["path"], sandbox.rel(KITCHEN));
    assert_eq!(result["status"], "imported");
    assert_eq!(
        snapshot_tree(&sandbox.store()),
        before,
        "the Meet store is input only"
    );

    let note = fs::read_to_string(sandbox.note(KITCHEN)).unwrap();
    assert!(note.starts_with("---\n"), "{note}");
    for line in [
        "type: meeting",
        "title: Meeting 2025-01-15 10:00",
        &format!("meeting_id: {KITCHEN}"),
        "started_at: \"2025-01-15T10:00:00Z\"",
        "ended_at: \"2025-01-15T10:05:00Z\"",
        "duration_ms: 300000",
        "language: multi",
        "review_state: unreviewed",
        "artifact_schema_version: 1",
        "- label: Alice",
        "- label: Bob",
        "- label: speaker_unknown",
    ] {
        assert!(note.contains(line), "missing {line:?} in:\n{note}");
    }
    assert!(note.contains("speaker_ids:\n        - speaker_1"), "{note}");

    // `meeting list` / `show` / `review` (existing commands) accept the note.
    let list = sandbox.run(&["meeting", "list", "--json"]);
    assert_eq!(list.code, 0, "{}", list.stderr);
    let list = list.json();
    assert_eq!(list.as_array().unwrap().len(), 1);
    assert_eq!(list[0]["path"], sandbox.rel(KITCHEN));
    assert_eq!(list[0]["meeting_id"], KITCHEN);
    assert_eq!(list[0]["started_at"], "2025-01-15T10:00:00Z");
    assert_eq!(list[0]["duration_ms"], 300_000);
    assert_eq!(list[0]["language"], "multi");
    assert_eq!(list[0]["review_state"], "unreviewed");
    let show = sandbox.run(&["meeting", "show", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(show.code, 0, "{}", show.stderr);
    let review = sandbox.run(&["meeting", "review", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(review.code, 0, "{}", review.stderr);
}

#[cfg(unix)]
#[test]
fn the_imported_note_is_owner_only() {
    use std::os::unix::fs::PermissionsExt;
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 0);
    let mode = fs::metadata(sandbox.note(KITCHEN))
        .unwrap()
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o600);
}

#[test]
fn the_note_transcript_is_the_swift_markdown_golden() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 0);
    let note = fs::read_to_string(sandbox.note(KITCHEN)).unwrap();
    let golden =
        fs::read_to_string(Path::new(FIXTURES).join("meet-store/kitchen-sink.expected.md"))
            .unwrap();
    assert_eq!(
        between_markers(&note),
        format!("\n{}\n", golden.trim_end_matches('\n'))
    );
    assert!(note.ends_with(&format!("{END}\n")));
}

#[test]
fn import_text_is_one_line_and_available_drops_the_imported_recording() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN, COMPLETE]);
    let run = sandbox.run(&["meeting", "import", KITCHEN]);
    assert_eq!(run.code, 0);
    assert_eq!(
        run.stdout,
        format!("Imported {KITCHEN} as {}\n", sandbox.rel(KITCHEN))
    );
    let available = sandbox.run(&["meeting", "available", "--json"]);
    let list = available.json();
    assert_eq!(list.as_array().unwrap().len(), 1);
    assert_eq!(list[0]["meeting_id"], COMPLETE);
}

#[test]
fn importing_twice_is_a_no_op_that_keeps_reviewed_state_and_manual_edits() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 0);
    assert_eq!(
        sandbox
            .run(&["meeting", "review", &sandbox.rel(KITCHEN)])
            .code,
        0
    );
    let mut note = fs::read_to_string(sandbox.note(KITCHEN)).unwrap();
    note.push_str("\n## My notes\n\nkeep me\n");
    fs::write(sandbox.note(KITCHEN), &note).unwrap();

    let again = sandbox.run(&["meeting", "import", KITCHEN, "--json"]);
    assert_eq!(again.code, 0, "{}", again.stderr);
    assert_eq!(again.json()["status"], "already_imported");
    assert_eq!(again.json()["path"], sandbox.rel(KITCHEN));
    assert_eq!(fs::read_to_string(sandbox.note(KITCHEN)).unwrap(), note);
    let notes: Vec<_> = fs::read_dir(sandbox.vault().join("meetings"))
        .unwrap()
        .collect();
    assert_eq!(notes.len(), 1, "no duplicate note");

    // Even when the user moved the note, the recording counts as imported.
    fs::create_dir_all(sandbox.vault().join("archive")).unwrap();
    fs::rename(
        sandbox.note(KITCHEN),
        sandbox.vault().join("archive/kept.md"),
    )
    .unwrap();
    let moved = sandbox.run(&["meeting", "import", KITCHEN, "--json"]);
    assert_eq!(moved.json()["status"], "already_imported");
    assert_eq!(moved.json()["path"], "archive/kept.md");
    assert!(!sandbox.note(KITCHEN).exists());
    assert_eq!(
        sandbox.run(&["meeting", "available", "--json"]).stdout,
        "[]\n"
    );
}

#[test]
fn import_refuses_to_overwrite_an_unrelated_file_at_the_note_path() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    fs::create_dir_all(sandbox.vault().join("meetings")).unwrap();
    fs::write(sandbox.note(KITCHEN), "mine\n").unwrap();
    let run = sandbox.run(&["meeting", "import", KITCHEN]);
    assert_eq!(run.code, 1);
    assert_eq!(run.stderr.lines().count(), 1, "{}", run.stderr);
    assert_eq!(fs::read_to_string(sandbox.note(KITCHEN)).unwrap(), "mine\n");
}

#[test]
fn a_missing_recording_is_a_one_line_error() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let run = sandbox.run(&["meeting", "import", COMPLETE]);
    assert_eq!(run.code, 1);
    assert_eq!(run.stdout, "");
    assert_eq!(run.stderr.lines().count(), 1, "{}", run.stderr);
    assert!(run.stderr.contains(COMPLETE), "{}", run.stderr);
    let json = sandbox.run(&["meeting", "import", COMPLETE, "--json"]);
    assert_eq!(json.code, 1);
    assert!(json.json()["error"].as_str().unwrap().contains(COMPLETE));
    assert!(
        !sandbox.vault().join("meetings").exists(),
        "nothing written"
    );

    let none = sandbox.run(&["meeting", "import"]);
    assert_eq!(none.code, 1);
    assert_eq!(none.stderr.lines().count(), 1, "{}", none.stderr);
}

#[test]
fn an_uppercase_id_is_normalised_like_the_store_does() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let upper = KITCHEN.to_uppercase();
    let run = sandbox.run(&["meeting", "import", &upper, "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    assert_eq!(run.json()["path"], sandbox.rel(KITCHEN));
}

// ------------------------------------------------------- malformed and hostile

fn rewrite_manifest(sandbox: &Sandbox, id: &str, edit: impl FnOnce(&mut Value)) {
    let path = sandbox.recording(id).join("manifest.json");
    let mut manifest: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    edit(&mut manifest);
    fs::write(path, serde_json::to_vec_pretty(&manifest).unwrap()).unwrap();
}

#[test]
fn a_malformed_manifest_is_skipped_by_available_and_rejected_by_import() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN, COMPLETE]);
    fs::write(
        sandbox.recording(COMPLETE).join("manifest.json"),
        "{ not json",
    )
    .unwrap();
    let available = sandbox.run(&["meeting", "available", "--json"]);
    assert_eq!(available.code, 0);
    let list = available.json();
    assert_eq!(list.as_array().unwrap().len(), 1);
    assert_eq!(list[0]["meeting_id"], KITCHEN);
    let text = sandbox.run(&["meeting", "available"]);
    assert!(
        text.stderr.contains(COMPLETE),
        "skipped recordings are named on stderr: {}",
        text.stderr
    );

    let run = sandbox.run(&["meeting", "import", COMPLETE]);
    assert_eq!(run.code, 1);
    assert_eq!(run.stderr.lines().count(), 1, "{}", run.stderr);
    assert!(!sandbox.vault().join("meetings").exists());
}

#[test]
fn manifests_the_swift_decoder_rejects_are_rejected_here() {
    type ManifestEdit = (&'static str, Box<dyn Fn(&mut Value)>);
    let cases: Vec<ManifestEdit> = vec![
        (
            "schema 2",
            Box::new(|m| m["schema_version"] = Value::from(2)),
        ),
        (
            "id mismatch",
            Box::new(|m| m["meeting_id"] = Value::from(COMPLETE)),
        ),
        (
            "bad source",
            Box::new(|m| m["source"] = Value::from("cloud")),
        ),
        (
            "fractional date",
            Box::new(|m| m["created_at"] = Value::from("2025-01-15T10:00:00.5Z")),
        ),
        (
            "missing consent",
            Box::new(|m| {
                m.as_object_mut().unwrap().remove("consent");
            }),
        ),
        (
            "bad retention",
            Box::new(|m| m["retention"]["policy"] = Value::from("forever")),
        ),
    ];
    for (name, edit) in cases {
        let sandbox = Sandbox::new();
        sandbox.seed(&[KITCHEN]);
        rewrite_manifest(&sandbox, KITCHEN, edit);
        let run = sandbox.run(&["meeting", "import", KITCHEN]);
        assert_eq!(run.code, 1, "{name}: {}", run.stderr);
        assert_eq!(run.stderr.lines().count(), 1, "{name}: {}", run.stderr);
        assert_eq!(
            sandbox.run(&["meeting", "available", "--json"]).stdout,
            "[]\n",
            "{name}"
        );
    }
    // Unknown fields stay tolerated, like `additionalFields` in the Swift decoder.
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    rewrite_manifest(&sandbox, KITCHEN, |m| {
        m["future_field"] = serde_json::json!({"a": 1})
    });
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 0);
}

#[test]
fn an_oversized_manifest_is_rejected_before_it_is_parsed() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let path = sandbox.recording(KITCHEN).join("manifest.json");
    let mut bytes = fs::read(&path).unwrap();
    bytes.extend(std::iter::repeat_n(b' ', 2 * 1024 * 1024));
    fs::write(&path, bytes).unwrap();
    let run = sandbox.run(&["meeting", "import", KITCHEN]);
    assert_eq!(run.code, 1);
    assert!(run.stderr.contains("limit"), "{}", run.stderr);
}

#[test]
fn ids_that_are_not_uuids_never_reach_the_filesystem() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let outside = sandbox.root.join("xdg-data/outside");
    copy_tree(
        &Path::new(FIXTURES)
            .join("meet-store/meetings")
            .join(KITCHEN),
        &outside,
    );
    let hostile = [
        "..".to_owned(),
        "../outside".to_owned(),
        "../../outside".to_owned(),
        format!("{KITCHEN}/../../outside"),
        format!("./{KITCHEN}"),
        format!("{KITCHEN}/"),
        "/etc/passwd".to_owned(),
        "outside".to_owned(),
        String::new(),
        "..%2foutside".to_owned(),
    ];
    for id in &hostile {
        let before = (snapshot_tree(&sandbox.store()), snapshot_tree(&outside));
        let run = sandbox.run(&["meeting", "import", id]);
        assert_eq!(run.code, 1, "{id:?}: {}", run.stdout);
        assert_eq!(run.stderr.lines().count(), 1, "{id:?}: {}", run.stderr);
        assert!(
            run.stderr.contains("invalid meeting id"),
            "{id:?}: {}",
            run.stderr
        );
        assert!(!sandbox.vault().join("meetings").exists(), "{id:?}");
        assert_eq!(
            (snapshot_tree(&sandbox.store()), snapshot_tree(&outside)),
            before,
            "{id:?}"
        );
    }
    // Same for the id of a note-bound command: `segments` takes a vault path, not an id.
    let run = sandbox.run(&["meeting", "segments", "../../etc/passwd"]);
    assert_eq!(run.code, 1);
}

#[test]
fn file_names_in_a_manifest_must_stay_inside_the_recording() {
    for bad in [
        "../../outside.m4a",
        "/etc/passwd",
        "~/x",
        "a//b",
        "a/../b",
        "",
    ] {
        let sandbox = Sandbox::new();
        sandbox.seed(&[KITCHEN]);
        rewrite_manifest(&sandbox, KITCHEN, |m| {
            m["audio_tracks"][0]["relative_path"] = Value::from(bad);
        });
        let run = sandbox.run(&["meeting", "import", KITCHEN]);
        assert_eq!(run.code, 1, "{bad:?}");
        assert!(
            run.stderr.contains("relative path"),
            "{bad:?}: {}",
            run.stderr
        );
        assert_eq!(
            sandbox.run(&["meeting", "available", "--json"]).stdout,
            "[]\n"
        );
    }
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    rewrite_manifest(&sandbox, KITCHEN, |m| {
        m["original_asset"] = Value::from("../evil")
    });
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 1);
}

#[cfg(unix)]
#[test]
fn a_symlinked_recording_directory_is_never_followed() {
    let sandbox = Sandbox::new();
    let outside = sandbox.root.join("outside/real");
    copy_tree(
        &Path::new(FIXTURES)
            .join("meet-store/meetings")
            .join(KITCHEN),
        &outside,
    );
    fs::create_dir_all(sandbox.store().join("meetings")).unwrap();
    std::os::unix::fs::symlink(&outside, sandbox.recording(KITCHEN)).unwrap();
    assert_eq!(
        sandbox.run(&["meeting", "available", "--json"]).stdout,
        "[]\n"
    );
    let run = sandbox.run(&["meeting", "import", KITCHEN]);
    assert_eq!(run.code, 1);
    assert!(run.stderr.contains("symbolic link"), "{}", run.stderr);
    assert!(!sandbox.vault().join("meetings").exists());
}

#[cfg(unix)]
#[test]
fn symlinked_artifact_files_are_never_followed() {
    for file in ["manifest.json", "segments.raw.jsonl"] {
        let sandbox = Sandbox::new();
        sandbox.seed(&[KITCHEN]);
        let secret = sandbox.root.join("secret.txt");
        fs::write(&secret, "TOP SECRET\n").unwrap();
        let target = sandbox.recording(KITCHEN).join(file);
        let original = fs::read(&target).unwrap();
        fs::remove_file(&target).unwrap();
        // A symlink to a real, readable file outside the recording.
        let real = sandbox.root.join("outside-copy");
        fs::write(&real, original).unwrap();
        std::os::unix::fs::symlink(&real, &target).unwrap();
        let run = sandbox.run(&["meeting", "import", KITCHEN]);
        assert_eq!(run.code, 1, "{file}");
        assert!(
            run.stderr.contains("symbolic link"),
            "{file}: {}",
            run.stderr
        );
        assert!(!sandbox.vault().join("meetings").exists(), "{file}");
    }
    // The optional speaker files too: a link is an error, not an empty file.
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let target = sandbox.recording(KITCHEN).join("speaker_edits.jsonl");
    fs::remove_file(&target).unwrap();
    std::os::unix::fs::symlink("/etc/passwd", &target).unwrap();
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 1);
}

#[cfg(unix)]
#[test]
fn a_symlinked_store_root_is_rejected_like_the_swift_store_does() {
    let sandbox = Sandbox::new();
    let real = sandbox.root.join("real-store");
    copy_tree(
        &Path::new(FIXTURES)
            .join("meet-store/meetings")
            .join(KITCHEN),
        &real.join("meetings").join(KITCHEN),
    );
    fs::create_dir_all(sandbox.root.join("xdg-data")).unwrap();
    std::os::unix::fs::symlink(&real, sandbox.store()).unwrap();
    let run = sandbox.run(&["meeting", "available", "--json"]);
    assert_eq!(run.code, 1);
    assert!(
        run.json()["error"]
            .as_str()
            .unwrap()
            .contains("symbolic link")
    );
}

#[cfg(unix)]
#[test]
fn import_never_writes_through_a_symlinked_vault_directory() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN]);
    let outside = sandbox.root.join("outside-vault");
    fs::create_dir_all(&outside).unwrap();
    std::os::unix::fs::symlink(&outside, sandbox.vault().join("meetings")).unwrap();
    let run = sandbox.run(&["meeting", "import", KITCHEN]);
    assert_eq!(run.code, 1);
    assert_eq!(
        fs::read_dir(&outside).unwrap().count(),
        0,
        "nothing written outside the vault"
    );
}

// -------------------------------------------------------------------- refresh

fn imported_and_edited(sandbox: &Sandbox) -> String {
    sandbox.seed(&[KITCHEN]);
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 0);
    assert_eq!(
        sandbox
            .run(&["meeting", "review", &sandbox.rel(KITCHEN)])
            .code,
        0
    );
    let mut note = fs::read_to_string(sandbox.note(KITCHEN)).unwrap();
    note.push_str("\n## My notes\n\nkeep me\n");
    fs::write(sandbox.note(KITCHEN), &note).unwrap();
    note
}

fn change_transcript(sandbox: &Sandbox) {
    let path = sandbox.recording(KITCHEN).join("segments.raw.jsonl");
    let text = fs::read_to_string(&path).unwrap();
    assert!(text.contains("Hello, world!"));
    fs::write(path, text.replace("Hello, world!", "Hello, there!")).unwrap();
}

#[test]
fn refresh_without_a_change_reports_changed_false() {
    let sandbox = Sandbox::new();
    let note = imported_and_edited(&sandbox);
    let run = sandbox.run(&["meeting", "refresh", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    let result = run.json();
    assert_eq!(result["path"], sandbox.rel(KITCHEN));
    assert_eq!(result["changed"], false);
    assert_eq!(result["applied"], false);
    assert!(
        result.get("diff_lines").is_none(),
        "diff_lines is omitted when empty"
    );
    assert_eq!(fs::read_to_string(sandbox.note(KITCHEN)).unwrap(), note);
    let text = sandbox.run(&["meeting", "refresh", &sandbox.rel(KITCHEN)]);
    assert_eq!(text.stdout, "No changes.\n");
}

#[test]
fn refresh_previews_by_default_and_applies_only_the_transcript_block() {
    let sandbox = Sandbox::new();
    let note = imported_and_edited(&sandbox);
    change_transcript(&sandbox);
    let store_before = snapshot_tree(&sandbox.store());

    let preview = sandbox.run(&["meeting", "refresh", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(preview.code, 0, "{}", preview.stderr);
    let result = preview.json();
    assert_eq!(keys(&result), ["applied", "changed", "diff_lines", "path"]);
    assert_eq!(
        (&result["changed"], &result["applied"]),
        (&Value::Bool(true), &Value::Bool(false))
    );
    let diff: Vec<&str> = result["diff_lines"]
        .as_array()
        .unwrap()
        .iter()
        .map(|line| line.as_str().unwrap())
        .collect();
    assert!(
        diff.contains(&"- Hello, world! Café — naïve résumé."),
        "{diff:?}"
    );
    assert!(
        diff.contains(&"+ Hello, there! Café — naïve résumé."),
        "{diff:?}"
    );
    assert!(
        diff.iter()
            .all(|line| line.starts_with("  ") || line.starts_with("- ") || line.starts_with("+ "))
    );
    assert_eq!(
        fs::read_to_string(sandbox.note(KITCHEN)).unwrap(),
        note,
        "preview writes nothing"
    );

    let text = sandbox.run(&["meeting", "refresh", &sandbox.rel(KITCHEN)]);
    assert!(
        text.stdout.contains("+ Hello, there!") && text.stdout.contains("- Hello, world!"),
        "{}",
        text.stdout
    );
    assert!(!text.stdout.contains("Applied."));

    let applied = sandbox.run(&[
        "meeting",
        "refresh",
        &sandbox.rel(KITCHEN),
        "--apply",
        "--json",
    ]);
    assert_eq!(applied.code, 0, "{}", applied.stderr);
    assert_eq!(applied.json()["applied"], true);
    let after = fs::read_to_string(sandbox.note(KITCHEN)).unwrap();
    assert_eq!(
        after,
        note.replace("Hello, world!", "Hello, there!"),
        "only the transcript changed"
    );
    assert!(after.contains("review_state: reviewed"));
    assert!(after.ends_with("\n## My notes\n\nkeep me\n"));
    assert_eq!(
        snapshot_tree(&sandbox.store()),
        store_before,
        "the Meet store is input only"
    );

    // The index and `meeting list` still accept the note, and a second refresh is a no-op.
    assert_eq!(
        sandbox.run(&["meeting", "list", "--json"]).json()[0]["review_state"],
        "reviewed"
    );
    let again = sandbox.run(&[
        "meeting",
        "refresh",
        &sandbox.rel(KITCHEN),
        "--apply",
        "--json",
    ]);
    assert_eq!(
        (
            again.json()["changed"].as_bool(),
            again.json()["applied"].as_bool()
        ),
        (Some(false), Some(false))
    );
    // The previous note stays recoverable in the vault history.
    let history = sandbox.vault().join(".symdesk");
    assert!(history.exists(), "history snapshot directory");
}

#[test]
fn refresh_follows_edited_segments_when_they_exist() {
    let sandbox = Sandbox::new();
    imported_and_edited(&sandbox);
    let edited = sandbox.recording(KITCHEN).join("segments.edited.jsonl");
    let raw = fs::read_to_string(sandbox.recording(KITCHEN).join("segments.raw.jsonl")).unwrap();
    let first = raw
        .lines()
        .find(|line| line.contains("Hello, world!"))
        .unwrap();
    let mut segment: Value = serde_json::from_str(first).unwrap();
    segment["edited_text"] = Value::from("Hello, edited world.");
    segment["revision"] = Value::from("user_corrected");
    fs::write(&edited, format!("{}\n", segment)).unwrap();
    let run = sandbox.run(&[
        "meeting",
        "refresh",
        &sandbox.rel(KITCHEN),
        "--apply",
        "--json",
    ]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    let note = fs::read_to_string(sandbox.note(KITCHEN)).unwrap();
    // Swift `symmeet export` prefers a non-empty edited file over the raw one.
    assert!(note.contains("Hello, edited world."), "{note}");
    assert!(
        !note.contains("Continuing without an identified speaker"),
        "edited file replaces raw"
    );
}

#[test]
fn refresh_errors_are_one_line_and_leave_the_note_alone() {
    let sandbox = Sandbox::new();
    let note = imported_and_edited(&sandbox);
    // Markers removed by hand: refuse instead of guessing.
    let broken = note.replace(START, "");
    fs::write(sandbox.note(KITCHEN), &broken).unwrap();
    let run = sandbox.run(&["meeting", "refresh", &sandbox.rel(KITCHEN), "--apply"]);
    assert_eq!(run.code, 1);
    assert_eq!(run.stderr.lines().count(), 1, "{}", run.stderr);
    assert!(run.stderr.contains(START), "{}", run.stderr);
    assert_eq!(fs::read_to_string(sandbox.note(KITCHEN)).unwrap(), broken);
    fs::write(sandbox.note(KITCHEN), &note).unwrap();

    // Recording gone.
    fs::remove_dir_all(sandbox.recording(KITCHEN)).unwrap();
    let gone = sandbox.run(&["meeting", "refresh", &sandbox.rel(KITCHEN)]);
    assert_eq!(gone.code, 1);
    assert_eq!(gone.stderr.lines().count(), 1, "{}", gone.stderr);
    assert!(gone.stderr.contains(KITCHEN), "{}", gone.stderr);

    // Not a meeting note, outside the vault, no argument.
    fs::write(sandbox.vault().join("plain.md"), "# plain\n").unwrap();
    assert_eq!(sandbox.run(&["meeting", "refresh", "plain.md"]).code, 1);
    assert_eq!(
        sandbox
            .run(&["meeting", "refresh", "../../etc/passwd"])
            .code,
        1
    );
    assert_eq!(sandbox.run(&["meeting", "refresh"]).code, 1);
}

// ------------------------------------------------------ segments and speakers

fn imported(sandbox: &Sandbox) {
    sandbox.seed(&[KITCHEN]);
    assert_eq!(sandbox.run(&["meeting", "import", KITCHEN]).code, 0);
}

#[test]
fn segments_json_matches_the_swift_meeting_segment_shape() {
    let sandbox = Sandbox::new();
    imported(&sandbox);
    let run = sandbox.run(&["meeting", "segments", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    let list = run.json();
    let list = list.as_array().unwrap();
    assert_eq!(list.len(), 4);
    let starts: Vec<i64> = list
        .iter()
        .map(|s| s["start_ms"].as_i64().unwrap())
        .collect();
    assert_eq!(
        starts,
        [0, 2000, 4000, 3_661_000],
        "chronological, like the transcript"
    );
    for segment in list {
        let mut expected = vec![
            "end_ms",
            "engine_text",
            "revision",
            "segment_id",
            "speaker_id",
            "start_ms",
        ];
        if segment.get("edited_text").is_some() {
            expected.insert(0, "edited_text");
        }
        assert_eq!(keys(segment), expected);
    }
    assert_eq!(
        list[0]["segment_id"],
        "AAAAAAAA-1111-1111-1111-111111111111"
    );
    assert_eq!(list[0]["speaker_id"], "speaker_1");
    assert_eq!(list[0]["engine_text"], "Hello, world! Café — naïve résumé.");
    assert_eq!(list[0]["revision"], "engine");
    assert!(list[0].get("edited_text").is_none(), "omitted, not null");
    assert_eq!(list[2]["revision"], "user_corrected");
    assert_eq!(
        list[2]["edited_text"],
        "Continuing without an identified speaker, edited for clarity."
    );
    assert!(
        sandbox
            .run(&["meeting", "segments", &sandbox.rel(KITCHEN)])
            .stdout
            .contains("00:00:00.000")
    );
}

#[test]
fn speakers_json_lists_labels_from_the_edit_log_and_falls_back_to_the_id() {
    let sandbox = Sandbox::new();
    imported(&sandbox);
    let run = sandbox.run(&["meeting", "speakers", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    assert_eq!(
        run.json(),
        serde_json::json!([
            {"speaker_id": "speaker_1", "label": "Alice"},
            {"speaker_id": "speaker_2", "label": "Bob"},
            {"speaker_id": "speaker_unknown", "label": "speaker_unknown"},
        ])
    );
    let text = sandbox.run(&["meeting", "speakers", &sandbox.rel(KITCHEN)]);
    assert_eq!(
        text.stdout,
        "speaker_1\tAlice\nspeaker_2\tBob\nspeaker_unknown\tspeaker_unknown\n"
    );
}

#[test]
fn speaker_edits_replay_like_the_swift_speaker_editor() {
    let sandbox = Sandbox::new();
    imported(&sandbox);
    let edits = sandbox.recording(KITCHEN).join("speaker_edits.jsonl");
    let event = |seq: u32, kind: &str, speaker: &str, extra: Value| {
        let mut value = serde_json::json!({
            "schema_version": 1,
            "event_id": format!("00000000-0000-0000-0000-0000000003{seq:02}"),
            "meeting_id": KITCHEN, "kind": kind, "speaker_id": speaker,
            "sequence_number": seq, "occurred_at": "2025-01-15T10:07:00Z",
        });
        for (k, v) in extra.as_object().unwrap() {
            value[k] = v.clone();
        }
        value.to_string()
    };
    // label, merge speaker_2 into speaker_1 (the label moves with it), relabel, reset-free.
    let lines = [
        event(1, "label", "speaker_2", serde_json::json!({"label": "Bob"})),
        event(
            2,
            "merge",
            "speaker_2",
            serde_json::json!({"target_id": "speaker_1"}),
        ),
        event(
            3,
            "label",
            "speaker_unknown",
            serde_json::json!({"label": "Guest"}),
        ),
    ];
    fs::write(&edits, lines.join("\n") + "\n").unwrap();
    let run = sandbox.run(&["meeting", "speakers", &sandbox.rel(KITCHEN), "--json"]);
    assert_eq!(run.code, 0, "{}", run.stderr);
    assert_eq!(
        run.json(),
        serde_json::json!([
            {"speaker_id": "speaker_1", "label": "Bob"},
            {"speaker_id": "speaker_2", "label": "speaker_2"},
            {"speaker_id": "speaker_unknown", "label": "Guest"},
        ])
    );
    // A label for a speaker nobody spoke as is a corrupt log, as in the Swift editor.
    fs::write(
        &edits,
        event(1, "label", "speaker_9", serde_json::json!({"label": "X"})) + "\n",
    )
    .unwrap();
    let run = sandbox.run(&["meeting", "speakers", &sandbox.rel(KITCHEN)]);
    assert_eq!(run.code, 1);
    assert_eq!(run.stderr.lines().count(), 1, "{}", run.stderr);
}

#[test]
fn segments_and_speakers_fail_cleanly_without_the_recording() {
    let sandbox = Sandbox::new();
    imported(&sandbox);
    fs::remove_dir_all(sandbox.store()).unwrap();
    for sub in ["segments", "speakers"] {
        let run = sandbox.run(&["meeting", sub, &sandbox.rel(KITCHEN), "--json"]);
        assert_eq!(run.code, 1, "{sub}");
        assert!(
            run.json()["error"].as_str().unwrap().contains(KITCHEN),
            "{sub}"
        );
        assert_eq!(
            fs::read_to_string(sandbox.note(KITCHEN))
                .unwrap()
                .matches(START)
                .count(),
            1
        );
    }
}

// ----------------------------------------------- help, routing and frozen paths

const ADDED: [(&str, &str); 5] = [
    (
        "available",
        "List Meet recordings that are not yet imported into the vault",
    ),
    (
        "import",
        "Import a Meet recording into the vault as a meeting note",
    ),
    (
        "refresh",
        "Preview (or apply) a re-export of a meeting note's transcript",
    ),
    (
        "segments",
        "List the time-coded transcript segments of a meeting note's recording",
    ),
    (
        "speakers",
        "List the speakers of a meeting note's recording with their labels",
    ),
];

#[test]
fn meeting_help_lists_the_five_commands_in_cobra_order() {
    let sandbox = Sandbox::new();
    for args in [vec!["meeting", "--help"], vec!["help", "meeting"]] {
        let run = sandbox.run_with(sandbox.command(), &args);
        assert_eq!(run.code, 0);
        let listed: Vec<&str> = run
            .stdout
            .lines()
            .skip_while(|line| *line != "Available Commands:")
            .skip(1)
            .take_while(|line| !line.is_empty())
            .map(|line| line.split_whitespace().next().unwrap())
            .collect();
        assert_eq!(
            listed,
            [
                "available",
                "import",
                "list",
                "participant",
                "publish",
                "refresh",
                "review",
                "segments",
                "show",
                "speakers"
            ],
            "{args:?}"
        );
        for (name, short) in ADDED {
            assert!(
                run.stdout.contains(&format!("  {name:<11} {short}\n")),
                "{name}: {}",
                run.stdout
            );
        }
    }
}

#[test]
fn every_added_command_answers_help_in_both_spellings() {
    let sandbox = Sandbox::new();
    for (name, short) in ADDED {
        let by_help = sandbox.run_with(sandbox.command(), &["help", "meeting", name]);
        let by_flag = sandbox.run_with(sandbox.command(), &["meeting", name, "--help"]);
        let by_short = sandbox.run_with(sandbox.command(), &["meeting", name, "-h"]);
        assert_eq!(by_help.code, 0, "{name}");
        assert_eq!(by_help.stderr, "");
        assert!(
            by_help.stdout.starts_with(&format!("{short}\n\n")),
            "{name}: {}",
            by_help.stdout
        );
        assert!(
            by_help
                .stdout
                .contains(&format!("\nUsage:\n  symdesk meeting {name}")),
            "{name}: {}",
            by_help.stdout
        );
        assert!(by_help.stdout.contains("Global Flags:"));
        assert_eq!(
            (by_flag.code, &by_flag.stdout),
            (0, &by_help.stdout),
            "{name}"
        );
        assert_eq!(
            (by_short.code, &by_short.stdout),
            (0, &by_help.stdout),
            "{name}"
        );
    }
    let refresh = sandbox.run_with(sandbox.command(), &["meeting", "refresh", "--help"]);
    assert!(refresh.stdout.contains("--apply"), "{}", refresh.stdout);
}

/// Open point (not changed here): the recorded Go tree prints the `meeting` help with
/// exit 0 for an unknown subcommand. The issue asks for a non-zero exit; that is a
/// deliberate change of frozen behaviour and needs its own reviewed decision.
#[test]
fn unknown_meeting_subcommand_keeps_the_frozen_help_with_exit_zero() {
    let sandbox = Sandbox::new();
    let unknown = sandbox.run_with(sandbox.command(), &["meeting", "frobnicate", "--json"]);
    let help = sandbox.run_with(sandbox.command(), &["meeting", "--help"]);
    assert_eq!(
        unknown.code, 0,
        "frozen Go behaviour; see the open point in docs/rust-port/cli-additions.md"
    );
    assert_eq!(unknown.stdout, help.stdout);
    assert_eq!(unknown.stderr, "");
}

// ------------------------------------------- JSON samples pinned for the Swift side

/// The bytes the binary prints for each command, committed under
/// `tests/fixtures/meeting-json/`. `Tests/SymDeskCoreTests/MeetingImportJSONTests.swift`
/// decodes the same files into the `DeskCore` types, so both sides are pinned to
/// one file. `SYMDESK_UPDATE_FIXTURES=1` rewrites them.
#[test]
fn json_output_matches_the_files_the_swift_decoder_test_reads() {
    let sandbox = Sandbox::new();
    sandbox.seed(&[KITCHEN, COMPLETE]);
    let mut samples: Vec<(&str, String)> = Vec::new();
    samples.push((
        "available",
        sandbox.run(&["meeting", "available", "--json"]).stdout,
    ));
    samples.push((
        "import",
        sandbox
            .run(&["meeting", "import", KITCHEN, "--json"])
            .stdout,
    ));
    let note = sandbox.rel(KITCHEN);
    samples.push((
        "segments",
        sandbox
            .run(&["meeting", "segments", &note, "--json"])
            .stdout,
    ));
    samples.push((
        "speakers",
        sandbox
            .run(&["meeting", "speakers", &note, "--json"])
            .stdout,
    ));
    change_transcript(&sandbox);
    samples.push((
        "refresh",
        sandbox.run(&["meeting", "refresh", &note, "--json"]).stdout,
    ));
    samples.push((
        "refresh-applied",
        sandbox
            .run(&["meeting", "refresh", &note, "--apply", "--json"])
            .stdout,
    ));
    let dir = Path::new(FIXTURES).join("meeting-json");
    let update = std::env::var_os("SYMDESK_UPDATE_FIXTURES").is_some();
    if update {
        fs::create_dir_all(&dir).unwrap();
    }
    for (name, stdout) in samples {
        let path = dir.join(format!("{name}.json"));
        if update {
            fs::write(&path, &stdout).unwrap();
        }
        let expected =
            fs::read_to_string(&path).unwrap_or_else(|_| panic!("missing {}", path.display()));
        assert_eq!(stdout, expected, "{name}");
        assert!(
            stdout.ends_with('\n') && stdout.matches('\n').count() == 1,
            "{name} is one JSON line"
        );
    }
}

// ------------------------------------------------- refresh --apply data safety

/// Names of everything directly inside the vault's `meetings/` directory.
fn meetings_entries(sandbox: &Sandbox) -> Vec<String> {
    let mut names: Vec<String> = fs::read_dir(sandbox.vault().join("meetings"))
        .unwrap()
        .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    names.sort();
    names
}

#[cfg(unix)]
fn mode_of(path: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    fs::metadata(path).unwrap().permissions().mode() & 0o777
}

#[cfg(unix)]
#[test]
fn refresh_apply_stops_before_touching_the_note_when_the_history_cannot_be_written() {
    let sandbox = Sandbox::new();
    let note = imported_and_edited(&sandbox);
    change_transcript(&sandbox);
    // The history store needs `.symdesk/history` to be a directory.
    let history = sandbox.vault().join(".symdesk/history");
    let _ = fs::remove_dir_all(&history);
    fs::create_dir_all(sandbox.vault().join(".symdesk")).unwrap();
    fs::write(&history, "not a directory").unwrap();
    let before = fs::read(sandbox.note(KITCHEN)).unwrap();

    for json in [false, true] {
        let mut args = vec!["meeting", "refresh"];
        let rel = sandbox.rel(KITCHEN);
        args.push(&rel);
        args.push("--apply");
        if json {
            args.push("--json");
        }
        let run = sandbox.run(&args);
        assert_ne!(run.code, 0, "{}", run.stdout);
        // `--json` errors are reported on the other stream, as everywhere.
        let message = if json { &run.stdout } else { &run.stderr };
        assert_eq!(message.lines().count(), 1, "{message}");
        assert!(message.contains("history"), "{message}");
        assert_eq!(
            fs::read(sandbox.note(KITCHEN)).unwrap(),
            before,
            "the note is byte-identical"
        );
    }
    assert_eq!(fs::read_to_string(sandbox.note(KITCHEN)).unwrap(), note);
    assert_eq!(
        meetings_entries(&sandbox),
        [format!("meeting-{KITCHEN}.md")]
    );
}

#[cfg(unix)]
#[test]
fn refresh_apply_replaces_the_note_atomically_and_never_widens_its_mode() {
    use std::os::unix::fs::PermissionsExt;
    // (mode before, mode after): stricter or equal is kept, wider is narrowed to 0600.
    for (before, after) in [(0o600, 0o600), (0o400, 0o400), (0o644, 0o600)] {
        let sandbox = Sandbox::new();
        let note = imported_and_edited(&sandbox);
        change_transcript(&sandbox);
        fs::set_permissions(sandbox.note(KITCHEN), fs::Permissions::from_mode(before)).unwrap();
        let run = sandbox.run(&[
            "meeting",
            "refresh",
            &sandbox.rel(KITCHEN),
            "--apply",
            "--json",
        ]);
        assert_eq!(run.code, 0, "mode {before:o}: {}", run.stderr);
        assert_eq!(run.json()["applied"], true, "mode {before:o}");
        assert_eq!(
            fs::read_to_string(sandbox.note(KITCHEN)).unwrap(),
            note.replace("Hello, world!", "Hello, there!"),
            "mode {before:o}"
        );
        assert_eq!(mode_of(&sandbox.note(KITCHEN)), after, "mode {before:o}");
        assert_eq!(
            meetings_entries(&sandbox),
            [format!("meeting-{KITCHEN}.md")],
            "no temp file is left behind (mode {before:o})"
        );
    }
}
