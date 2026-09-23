use std::{
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use serde_json::Value;
use symdesk_index::{HistorySyncError, IndexedDocument, Sidecar, checkpoint_undo, history_restore};
use symdesk_vault::{HistoryStore, parse_bytes, secure_path};

static COUNTER: AtomicU64 = AtomicU64::new(0);
const FIXTURE: &str = include_str!("../../../testdata/port/vault/history-service.json");

struct Sandbox {
    parent: PathBuf,
    root: PathBuf,
    sidecar: Sidecar,
}

impl Sandbox {
    fn new() -> Self {
        let parent = std::env::temp_dir().join(format!(
            "symdesk-history-service-{}-{}",
            std::process::id(),
            COUNTER.fetch_add(1, Ordering::Relaxed)
        ));
        let root = parent.join("vault");
        fs::create_dir_all(&root).unwrap();
        let sidecar = Sidecar::open(&parent.join("sidecar.db")).unwrap();
        Self {
            parent,
            root,
            sidecar,
        }
    }

    fn write_and_index(&mut self, rel: &str, content: &str) {
        let path = self.root.join(rel);
        fs::write(&path, content).unwrap();
        let canonical = secure_path(&self.root, rel).unwrap();
        let document = parse_bytes(canonical.to_str().unwrap(), content.as_bytes()).unwrap();
        self.sidecar
            .index_document(&IndexedDocument::from_vault(&document, None).unwrap())
            .unwrap();
    }

    fn hits(&self, query: &str) -> usize {
        self.sidecar.search(query).unwrap().len()
    }
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.parent);
    }
}

fn field<'a>(root: &'a Value, path: &str) -> &'a Value {
    &root[path]
}

#[test]
fn history_service_restore_and_undo_match_go_side_effects() {
    let fixture: Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(fixture["schema_version"], 1);

    let mut sandbox = Sandbox::new();
    let original = "---\ntitle: Restored\n---\noriginalneedle\n";
    sandbox.write_and_index("restored.md", original);
    let entry = HistoryStore::new(&sandbox.root)
        .snapshot("restored.md")
        .unwrap()
        .unwrap();
    sandbox.write_and_index("restored.md", "---\ntitle: Restored\n---\nchangedneedle\n");
    history_restore(&sandbox.root, &mut sandbox.sidecar, "restored.md", None).unwrap();
    let expected = field(&fixture, "restore");
    assert_eq!(entry.id, expected["snapshot_id"].as_str().unwrap());
    assert_eq!(
        fs::read_to_string(sandbox.root.join("restored.md")).unwrap(),
        expected["content"]
    );
    assert_eq!(
        sandbox.hits("originalneedle"),
        expected["original_hits"].as_u64().unwrap() as usize
    );
    assert_eq!(
        sandbox.hits("changedneedle"),
        expected["changed_hits"].as_u64().unwrap() as usize
    );
    let journal_dir = sandbox.root.join(".symdesk/journal");
    let journal = fs::read_to_string(
        fs::read_dir(&journal_dir)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path(),
    )
    .unwrap();
    let activity: Value = serde_json::from_str(journal.trim()).unwrap();
    assert_eq!(activity["event"], "file_changed");
    assert_eq!(activity["path"], "restored.md");
    assert_eq!(activity["details"], "restored snapshot ");
    assert_eq!(expected["activity"], true);

    let mut sandbox = Sandbox::new();
    let malformed = "---\ntitle: [broken\n---\nmalformedneedle\n";
    fs::write(sandbox.root.join("malformed.md"), malformed).unwrap();
    let entry = HistoryStore::new(&sandbox.root)
        .snapshot("malformed.md")
        .unwrap()
        .unwrap();
    sandbox.write_and_index("malformed.md", "---\ntitle: Valid\n---\nvalidreplacement\n");
    let error =
        history_restore(&sandbox.root, &mut sandbox.sidecar, "malformed.md", None).unwrap_err();
    let expected = field(&fixture, "malformed_restore");
    assert!(
        error
            .to_string()
            .starts_with(expected["error_prefix"].as_str().unwrap())
    );
    match error {
        HistorySyncError::Parse {
            entry: restored, ..
        } => {
            assert_eq!(restored.id, entry.id);
            assert_eq!(restored.id, expected["snapshot_id"]);
        }
        other => panic!("expected parse error with restored entry, got {other}"),
    }
    assert_eq!(
        fs::read_to_string(sandbox.root.join("malformed.md")).unwrap(),
        expected["content"]
    );
    assert_eq!(
        sandbox.hits("validreplacement"),
        expected["changed_hits"].as_u64().unwrap() as usize
    );
    assert!(!sandbox.root.join(".symdesk/journal").exists());

    let mut sandbox = Sandbox::new();
    sandbox.write_and_index(
        "checkpoint.md",
        "---\ntitle: Checkpoint\n---\ncheckpointoriginal\n",
    );
    let history = HistoryStore::new(&sandbox.root);
    history
        .checkpoint_file("task-history", "checkpoint.md")
        .unwrap();
    history.checkpoint_file("task-history", "new.md").unwrap();
    sandbox.write_and_index(
        "checkpoint.md",
        "---\ntitle: Checkpoint\n---\ncheckpointchanged\n",
    );
    sandbox.write_and_index("new.md", "---\ntitle: New\n---\nnewfiletoken\n");
    checkpoint_undo(&sandbox.root, &mut sandbox.sidecar, "task-history").unwrap();
    let expected = field(&fixture, "undo");
    assert_eq!(
        fs::read_to_string(sandbox.root.join("checkpoint.md")).unwrap(),
        expected["content"]
    );
    assert_eq!(
        sandbox.hits("checkpointoriginal"),
        expected["original_hits"].as_u64().unwrap() as usize
    );
    assert_eq!(
        sandbox.hits("checkpointchanged"),
        expected["changed_hits"].as_u64().unwrap() as usize
    );
    assert_eq!(
        !Path::new(&sandbox.root.join("new.md")).exists(),
        expected["new_file_gone"]
    );
    assert_eq!(
        sandbox.hits("newfiletoken"),
        expected["new_file_hits"].as_u64().unwrap_or(0) as usize
    );
}
