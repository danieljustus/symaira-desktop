package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newVault(t *testing.T) (string, *Store) {
	t.Helper()
	root := t.TempDir()
	return root, NewStore(root)
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSnapshotAndRestore(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "notes/a.md", "v1")

	if _, err := s.Snapshot("notes/a.md"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "notes/a.md", "v2")
	if _, err := s.Snapshot("notes/a.md"); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List("notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(entries))
	}

	// Restore oldest (v1).
	if _, err := s.Restore("notes/a.md", entries[1].ID); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "notes/a.md"); got != "v1" {
		t.Fatalf("restored content = %q, want v1", got)
	}

	// Pre-restore state (v2) must itself have been snapshotted.
	entries, _ = s.List("notes/a.md")
	if len(entries) != 2 {
		t.Fatalf("expected 2 snapshots after restore (v2 deduped as newest), got %d", len(entries))
	}
}

func TestSnapshotDedupAndMissingFile(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "same")

	e1, err := s.Snapshot("a.md")
	if err != nil || e1 == nil {
		t.Fatalf("first snapshot: %v %v", e1, err)
	}
	e2, err := s.Snapshot("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if e2.ID != e1.ID {
		t.Fatal("expected dedup of identical content")
	}
	entries, _ := s.List("a.md")
	if len(entries) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(entries))
	}

	e3, err := s.Snapshot("missing.md")
	if err != nil || e3 != nil {
		t.Fatalf("missing file should be a no-op, got %v %v", e3, err)
	}
}

func TestRestoreByPrefixAndLatest(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "v1")
	s.Snapshot("a.md")
	write(t, root, "a.md", "v2")
	s.Snapshot("a.md")
	write(t, root, "a.md", "working copy")

	// Empty id restores the latest snapshot (v2).
	if _, err := s.Restore("a.md", ""); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "a.md"); got != "v2" {
		t.Fatalf("got %q, want v2", got)
	}

	entries, _ := s.List("a.md")
	var v1 Entry
	for _, e := range entries {
		if e.Size == 2 {
			v1 = e
		}
	}
	if _, err := s.Restore("a.md", v1.ID[:8]); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "a.md"); got != "v1" {
		t.Fatalf("got %q, want v1", got)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	_, s := newVault(t)
	for _, p := range []string{"../evil.md", "/abs/evil.md", ".."} {
		if _, err := s.Snapshot(p); err == nil {
			t.Fatalf("expected error for %q", p)
		}
	}
}

func TestPruneMaxPerFileAndGC(t *testing.T) {
	root, s := newVault(t)
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		write(t, root, "a.md", v)
		if _, err := s.Snapshot("a.md"); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.Prune(RetentionPolicy{MaxPerFile: 2})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	entries, _ := s.List("a.md")
	if len(entries) != 2 {
		t.Fatalf("expected 2 kept snapshots, got %d", len(entries))
	}

	objs, err := os.ReadDir(filepath.Join(root, ".symdesk", "history", "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects after GC, got %d", len(objs))
	}
}

func TestPruneMaxAgeKeepsNewest(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "old")
	s.Snapshot("a.md")

	// Backdate the manifest entry.
	entries, _ := s.List("a.md")
	entries[0].Timestamp = time.Now().UTC().Add(-48 * time.Hour)
	if err := s.writeManifest("a.md", entries); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Prune(RetentionPolicy{MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatal("newest snapshot must survive MaxAge pruning")
	}

	// Add a second, current snapshot; now the old one is prunable.
	write(t, root, "a.md", "new")
	s.Snapshot("a.md")
	removed, err = s.Prune(RetentionPolicy{MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
}

func TestTrashRoundtrip(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "notes/doc.md", "content")

	entry, err := s.Trash("notes/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes/doc.md")); !os.IsNotExist(err) {
		t.Fatal("file should be gone from vault")
	}

	list, err := s.TrashList()
	if err != nil || len(list) != 1 {
		t.Fatalf("trash list: %v (%d entries)", err, len(list))
	}
	if list[0].OriginalPath != "notes/doc.md" {
		t.Fatalf("original path = %q", list[0].OriginalPath)
	}

	restored, err := s.TrashRestore(entry.Name)
	if err != nil {
		t.Fatal(err)
	}
	if restored.OriginalPath != "notes/doc.md" {
		t.Fatalf("restored path = %q", restored.OriginalPath)
	}
	if got := read(t, root, "notes/doc.md"); got != "content" {
		t.Fatalf("restored content = %q", got)
	}
	list, _ = s.TrashList()
	if len(list) != 0 {
		t.Fatal("trash should be empty after restore")
	}
}

func TestTrashRestoreConflict(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "v1")
	entry, err := s.Trash("a.md")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.md", "occupied")
	if _, err := s.TrashRestore(entry.Name); err == nil {
		t.Fatal("expected conflict error")
	}
	// Trash item must still be there.
	list, _ := s.TrashList()
	if len(list) != 1 {
		t.Fatal("trash item must survive a failed restore")
	}
}

func TestTrashNameCollision(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "first")
	e1, err := s.Trash("a.md")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.md", "second")
	e2, err := s.Trash("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if e1.Name == e2.Name {
		t.Fatal("trash names must be unique")
	}
	list, _ := s.TrashList()
	if len(list) != 2 {
		t.Fatalf("expected 2 trash items, got %d", len(list))
	}
}

func TestTrashPurge(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "x")
	s.Trash("a.md")

	// Fresh item survives an age-based purge.
	purged, err := s.TrashPurge(24 * time.Hour)
	if err != nil || purged != 0 {
		t.Fatalf("purge: %d %v", purged, err)
	}

	// maxAge <= 0 purges everything.
	purged, err = s.TrashPurge(0)
	if err != nil || purged != 1 {
		t.Fatalf("purge all: %d %v", purged, err)
	}
	list, _ := s.TrashList()
	if len(list) != 0 {
		t.Fatal("trash should be empty")
	}

	// Deletion content is still recoverable via history.
	entries, _ := s.List("a.md")
	if len(entries) != 1 {
		t.Fatal("trashed file content must be preserved as a snapshot")
	}
}

// Regression test for issue #417: prune GC must never delete a blob that a
// task checkpoint still references, even when the per-file manifest has aged
// it out. Blobs are content-addressed and deduplicated, so a checkpoint
// taken on an old, unchanged file references the same blob ID as an
// aged-out manifest entry.
func TestPruneKeepsCheckpointReferencedBlob(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "notes/a.md", "v1")

	if _, err := s.Snapshot("notes/a.md"); err != nil {
		t.Fatal(err)
	}
	// The checkpoint references the same blob A as the manifest entry.
	if _, err := s.CheckpointFile("task-a", "notes/a.md"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "notes/a.md", "v2")
	if _, err := s.Snapshot("notes/a.md"); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List("notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(entries))
	}
	blobA, blobB := entries[1].ID, entries[0].ID

	removed, err := s.Prune(RetentionPolicy{MaxPerFile: 1})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	// Blob A is still needed by the checkpoint and must survive the GC.
	if _, err := os.Stat(filepath.Join(s.objectsDir(), blobA)); err != nil {
		t.Fatalf("checkpoint-referenced blob %s was garbage-collected: %v", blobA, err)
	}
	if _, err := os.Stat(filepath.Join(s.objectsDir(), blobB)); err != nil {
		t.Fatalf("kept blob %s missing: %v", blobB, err)
	}
	got, err := s.Content(blobA)
	if err != nil || string(got) != "v1" {
		t.Fatalf("Content(blobA) = %q, %v; want v1 (undo must still work)", got, err)
	}
}

// backdateCheckpoint rewrites the checkpoint manifest on disk with the given
// age, simulating a checkpoint created that long ago.
func backdateCheckpoint(t *testing.T, s *Store, taskID string, age time.Duration) {
	t.Helper()
	cp, err := s.loadCheckpoint(taskID)
	if err != nil {
		t.Fatal(err)
	}
	cp.Timestamp = time.Now().UTC().Add(-age)
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.checkpointPath(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// Aged-out checkpoints are pruned by MaxCheckpointAge while fresh ones
// survive (issue #417 checkpoint retention path).
func TestPruneRemovesOldCheckpoints(t *testing.T) {
	root, s := newVault(t)
	write(t, root, "a.md", "v1")
	if _, err := s.CheckpointFile("task-a", "a.md"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "b.md", "v2")
	if _, err := s.CheckpointFile("task-b", "b.md"); err != nil {
		t.Fatal(err)
	}

	backdateCheckpoint(t, s, "task-a", 48*time.Hour)
	backdateCheckpoint(t, s, "task-b", time.Hour)

	removed, err := s.Prune(RetentionPolicy{MaxCheckpointAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only the aged-out checkpoint)", removed)
	}

	pa, _ := s.checkpointPath("task-a")
	if _, err := os.Stat(pa); !os.IsNotExist(err) {
		t.Fatalf("task-a manifest should be pruned, stat err = %v", err)
	}
	pb, _ := s.checkpointPath("task-b")
	if _, err := os.Stat(pb); err != nil {
		t.Fatalf("task-b manifest should survive, stat err = %v", err)
	}
}

// After an aged-out checkpoint is pruned its blob loses protection and is
// garbage-collected once no manifest references it, while the surviving
// checkpoint's blob stays protected.
func TestPruneCheckpointBlobProtected(t *testing.T) {
	root, s := newVault(t)

	write(t, root, "a.md", "a-v1")
	if _, err := s.Snapshot("a.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckpointFile("task-a", "a.md"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.md", "a-v2")
	if _, err := s.Snapshot("a.md"); err != nil {
		t.Fatal(err)
	}

	write(t, root, "b.md", "b-v1")
	if _, err := s.Snapshot("b.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckpointFile("task-b", "b.md"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "b.md", "b-v2")
	if _, err := s.Snapshot("b.md"); err != nil {
		t.Fatal(err)
	}

	entriesA, _ := s.List("a.md")
	blobA := entriesA[1].ID // referenced only by the soon-to-be-pruned checkpoint
	entriesB, _ := s.List("b.md")
	blobB := entriesB[1].ID // referenced only by the surviving checkpoint

	backdateCheckpoint(t, s, "task-a", 48*time.Hour)
	backdateCheckpoint(t, s, "task-b", time.Hour)

	if _, err := s.Prune(RetentionPolicy{MaxPerFile: 1, MaxCheckpointAge: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}

	// task-a's checkpoint is gone, so its blob is now unreferenced and GC'd.
	if _, err := os.Stat(filepath.Join(s.objectsDir(), blobA)); !os.IsNotExist(err) {
		t.Fatalf("blob of pruned checkpoint should be garbage-collected, stat err = %v", err)
	}
	// task-b's checkpoint survives and still protects its blob.
	if _, err := os.Stat(filepath.Join(s.objectsDir(), blobB)); err != nil {
		t.Fatalf("blob of surviving checkpoint must not be garbage-collected: %v", err)
	}
}
