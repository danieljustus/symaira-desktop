package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPurgePathsRemovesTargetAndCollectsUnreferencedObjects(t *testing.T) {
	root, store := newVault(t)

	write(t, root, "target.md", "target-only")
	targetEntry, err := store.Snapshot("target.md")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "keep.md", "shared")
	keepEntry, err := store.Snapshot("keep.md")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "shared.md", "shared")
	sharedEntry, err := store.Snapshot("shared.md")
	if err != nil {
		t.Fatal(err)
	}
	if targetEntry.ID == keepEntry.ID || keepEntry.ID != sharedEntry.ID {
		t.Fatalf("unexpected content-addressed blobs: target=%s keep=%s shared=%s", targetEntry.ID, keepEntry.ID, sharedEntry.ID)
	}
	write(t, root, "unrelated.md", "unrelated")
	unrelatedEntry, err := store.Snapshot("unrelated.md")
	if err != nil {
		t.Fatal(err)
	}

	orphanID := strings.Repeat("f", 64)
	if err := os.WriteFile(filepath.Join(store.objectsDir(), orphanID), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := store.PurgePaths("target.md"); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.List("target.md"); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("target history still exists: %#v", entries)
	}
	if entries, err := store.List("keep.md"); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 || entries[0].ID != keepEntry.ID {
		t.Fatalf("unrelated keep history changed: %#v", entries)
	}
	if entries, err := store.List("unrelated.md"); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 || entries[0].ID != unrelatedEntry.ID {
		t.Fatalf("ordinary unrelated history changed: %#v", entries)
	}
	if _, err := os.Stat(filepath.Join(store.objectsDir(), targetEntry.ID)); !os.IsNotExist(err) {
		t.Fatalf("target-only blob survived purge, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.objectsDir(), keepEntry.ID)); err != nil {
		t.Fatalf("shared blob was collected despite keep.md/shared.md references: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.objectsDir(), unrelatedEntry.ID)); err != nil {
		t.Fatalf("unrelated referenced blob was collected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.objectsDir(), orphanID)); !os.IsNotExist(err) {
		t.Fatalf("unreferenced object survived purge, stat err = %v", err)
	}
}

func TestPurgePathsFiltersMixedCheckpointAndDeletesEmptyCheckpoint(t *testing.T) {
	root, store := newVault(t)
	write(t, root, "target.md", "target")
	if _, err := store.Snapshot("target.md"); err != nil {
		t.Fatal(err)
	}
	write(t, root, "keep.md", "keep")
	if _, err := store.Snapshot("keep.md"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CheckpointFile("mixed", "target.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckpointFile("mixed", "keep.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckpointFile("mixed", "target-new.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckpointFile("mixed", "keep-new.md"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"target-skip", "keep-skip"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0750); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CheckpointFile("mixed", path); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := store.CheckpointFile("empty", "target-only-new.md"); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgePaths("target.md", "target-new.md", "target-skip", "target-only-new.md"); err != nil {
		t.Fatal(err)
	}

	checkpoints, err := store.ListCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 || checkpoints[0].TaskID != "mixed" {
		t.Fatalf("expected only filtered mixed checkpoint, got %#v", checkpoints)
	}
	cp := checkpoints[0]
	if len(cp.Files) != 1 || cp.Files[0].RelPath != "keep.md" {
		t.Fatalf("target file reference was not removed: %#v", cp)
	}
	if len(cp.NewFiles) != 1 || cp.NewFiles[0] != "keep-new.md" {
		t.Fatalf("new-file filtering changed unrelated reference: %#v", cp)
	}
	if len(cp.Skipped) != 1 || cp.Skipped[0] != "keep-skip" {
		t.Fatalf("skipped filtering changed unrelated reference: %#v", cp)
	}
	if _, err := store.checkpointPath("empty"); err != nil {
		t.Fatal(err)
	} else if _, err := os.Stat(filepath.Join(root, ".symdesk", "history", "checkpoints", "empty.json")); !os.IsNotExist(err) {
		t.Fatalf("empty checkpoint survived purge, stat err = %v", err)
	}
	if entries, err := store.List("keep.md"); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Fatalf("unrelated history was removed: %#v", entries)
	}
}

func TestPurgePathsRejectsCorruptHistoryAndCheckpoint(t *testing.T) {
	t.Run("history", func(t *testing.T) {
		root, store := newVault(t)
		write(t, root, filepath.Join(".symdesk", "history", "manifest", "unrelated.md.json"), "not json")
		if err := store.PurgePaths("target.md"); err == nil {
			t.Fatal("expected corrupt history manifest to fail closed")
		}
		if got := read(t, root, filepath.Join(".symdesk", "history", "manifest", "unrelated.md.json")); got != "not json" {
			t.Fatalf("corrupt history manifest changed to %q", got)
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		root, store := newVault(t)
		write(t, root, filepath.Join(".symdesk", "history", "checkpoints", "broken.json"), "not json")
		if err := store.PurgePaths("target.md"); err == nil {
			t.Fatal("expected corrupt checkpoint manifest to fail closed")
		}
		if got := read(t, root, filepath.Join(".symdesk", "history", "checkpoints", "broken.json")); got != "not json" {
			t.Fatalf("corrupt checkpoint manifest changed to %q", got)
		}
	})
}

func TestPurgePathsRejectsInvalidAndTraversalPaths(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, path := range []string{"", ".", "..", "../outside.md", "/absolute.md"} {
		if err := store.PurgePaths(path); err == nil {
			t.Errorf("expected PurgePaths(%q) to reject invalid path", path)
		}
	}
}

func TestPurgeTrashPathsSelectsExactTargets(t *testing.T) {
	root, store := newVault(t)
	for _, path := range []string{"target.md", "other.md", "dir/target.md"} {
		write(t, root, path, path)
		if _, err := store.Trash(path); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := store.TrashList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected three trash entries before purge, got %d", len(entries))
	}
	var targetName string
	for _, entry := range entries {
		if entry.OriginalPath == "target.md" {
			targetName = entry.Name
		}
	}
	if targetName == "" {
		t.Fatal("target trash entry was not created")
	}

	removed, err := store.PurgeTrashPaths("target.md")
	if err != nil || removed != 1 {
		t.Fatalf("target trash purge: removed=%d err=%v", removed, err)
	}
	entries, err = store.TrashList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected unrelated trash entries to survive, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.OriginalPath == "target.md" {
			t.Fatal("target trash entry survived purge")
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".symdesk", "trash", targetName)); !os.IsNotExist(err) {
		t.Fatalf("target trash payload survived purge, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".symdesk", "trash", targetName+trashMetaSuffix)); !os.IsNotExist(err) {
		t.Fatalf("target trash metadata survived purge, stat err = %v", err)
	}

	removed, err = store.PurgeTrashPaths("dir/target.md")
	if err != nil || removed != 1 {
		t.Fatalf("nested target trash purge: removed=%d err=%v", removed, err)
	}
	entries, err = store.TrashList()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].OriginalPath != "other.md" {
		t.Fatalf("ordinary unrelated trash changed: %#v", entries)
	}
}

func TestPurgeTrashPathsRejectsInvalidAndTraversalPaths(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, path := range []string{"", ".", "..", "../outside.md", "/absolute.md"} {
		if removed, err := store.PurgeTrashPaths(path); err == nil || removed != 0 {
			t.Errorf("expected PurgeTrashPaths(%q) to reject invalid path, removed=%d err=%v", path, removed, err)
		}
	}
}
