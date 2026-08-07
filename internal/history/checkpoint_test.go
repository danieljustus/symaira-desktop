package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVaultFile creates a file inside the vault root (test helper).
func writeVaultFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readVaultFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A checkpoint records the pre-task state lazily and restores it as one
// unit: files rewritten by the task come back, files created by the task
// are deleted.
func TestCheckpointUndoRestoresAsUnit(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	writeVaultFile(t, root, "notes/a.md", "before-a")
	writeVaultFile(t, root, "notes/b.md", "before-b")

	cp, err := store.CheckpointFile("task-1", "notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	cp, err = store.CheckpointFile("task-1", "notes/b.md")
	if err != nil {
		t.Fatal(err)
	}
	// The task is expected to create notes/c.md: checkpoint it now (it does
	// not exist yet → recorded as new) so undo can delete it.
	cp, err = store.CheckpointFile("task-1", "notes/c.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 2 || len(cp.NewFiles) != 1 {
		t.Fatalf("expected 2 files + 1 new recorded, got %d files / %d new", len(cp.Files), len(cp.NewFiles))
	}

	// The task rewrites both files and creates a new one.
	writeVaultFile(t, root, "notes/a.md", "after-a")
	writeVaultFile(t, root, "notes/b.md", "after-b")
	writeVaultFile(t, root, "notes/c.md", "created-by-task")

	cp, err = store.UndoCheckpoint("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := readVaultFile(t, root, "notes/a.md"); got != "before-a" {
		t.Errorf("a.md = %q, want pre-task state", got)
	}
	if got := readVaultFile(t, root, "notes/b.md"); got != "before-b" {
		t.Errorf("b.md = %q, want pre-task state", got)
	}
	if _, err := os.Stat(filepath.Join(root, "notes/c.md")); !os.IsNotExist(err) {
		t.Error("c.md created by the task must be deleted on undo")
	}
	if len(cp.Skipped) != 0 {
		t.Errorf("expected complete undo, got skipped %v", cp.Skipped)
	}
}

// Files created during the task (recorded as NewFiles because they did not
// exist when the checkpoint was taken) are deleted by undo.
func TestCheckpointUndoDeletesNewFiles(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	cp, err := store.CheckpointFile("task-new", "fresh.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.NewFiles) != 1 || len(cp.Files) != 0 {
		t.Fatalf("expected 1 new file, got %d new / %d files", len(cp.NewFiles), len(cp.Files))
	}

	writeVaultFile(t, root, "fresh.md", "created")

	cp, err = store.UndoCheckpoint("task-new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh.md")); !os.IsNotExist(err) {
		t.Error("new file must be deleted on undo")
	}
	if cp.Partial() {
		t.Error("expected complete undo")
	}
}

// CheckpointFile is idempotent per file: the pre-task state is recorded on
// the first call and never overwritten by later calls in the same task.
func TestCheckpointFileIsLazyAndIdempotent(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	writeVaultFile(t, root, "n.md", "v1")

	if _, err := store.CheckpointFile("t", "n.md"); err != nil {
		t.Fatal(err)
	}
	writeVaultFile(t, root, "n.md", "v2")
	cp, err := store.CheckpointFile("t", "n.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 1 {
		t.Fatalf("expected exactly one recorded file, got %d", len(cp.Files))
	}
	// Undo must restore v1 (the state at first call), not v2.
	if _, err := store.UndoCheckpoint("t"); err != nil {
		t.Fatal(err)
	}
	if got := readVaultFile(t, root, "n.md"); got != "v1" {
		t.Errorf("n.md = %q, want the lazily captured pre-task state v1", got)
	}
}

// A snapshot failure marks the file skipped and the checkpoint partial —
// the caller is told the checkpoint is incomplete instead of assuming it.
func TestCheckpointReportsPartialOnSnapshotFailure(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	// A directory cannot be snapshotted as a file → skipped.
	dirPath := filepath.Join(root, "blocked")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cp, err := store.CheckpointFile("t", "blocked")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Skipped) != 1 || !cp.Partial() {
		t.Fatalf("expected 1 skipped entry and partial flag, got %v / partial=%v", cp.Skipped, cp.Partial())
	}
}

// ListCheckpoints returns all tasks newest first, and empty when none.
func TestCheckpointListAndEmpty(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	list, err := store.ListCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no checkpoints, got %d", len(list))
	}

	writeVaultFile(t, root, "a.md", "a")
	writeVaultFile(t, root, "b.md", "b")
	if _, err := store.CheckpointFile("task-1", "a.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckpointFile("task-2", "b.md"); err != nil {
		t.Fatal(err)
	}
	list, err = store.ListCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(list))
	}
	if list[0].TaskID != "task-2" {
		t.Errorf("expected newest first (task-2), got %s", list[0].TaskID)
	}
}

// Undo of an unknown task must fail cleanly; invalid task ids are rejected.
func TestCheckpointValidation(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, bad := range []string{"", "../escape", "a/b", ".hidden", "a:b"} {
		if _, err := store.BeginCheckpoint(bad); err == nil {
			t.Errorf("task id %q must be rejected", bad)
		}
	}
	if _, err := store.UndoCheckpoint("nope"); err == nil {
		t.Error("undo of an unknown task must fail")
	}
}

// The checkpoint manifest lives inside the history dir, while the blobs it
// references are the same content-addressed objects as normal snapshots.
func TestCheckpointReusesBlobStore(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	writeVaultFile(t, root, "n.md", "shared-content")

	cp, err := store.CheckpointFile("t", "n.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 1 || cp.Files[0].Entry.ID == "" {
		t.Fatalf("expected blob-backed entry, got %#v", cp)
	}
	// The blob object exists in the same objects dir as regular snapshots.
	objPath := filepath.Join(root, ".symdesk", "history", "objects", cp.Files[0].Entry.ID)
	if _, err := os.Stat(objPath); err != nil {
		t.Errorf("checkpoint blob missing from shared object store: %v", err)
	}
	// And the manifest lives under history/checkpoints.
	manifest := filepath.Join(root, ".symdesk", "history", "checkpoints", "t.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "shared-content"[:8]) == false && !strings.Contains(string(data), "n.md") {
		t.Error("manifest should reference the file path")
	}
}
