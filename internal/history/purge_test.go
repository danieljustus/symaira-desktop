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

func TestPurgePathsPreflightLeavesAllRecoveryStateUntouched(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(root string, targetID, keepID string)
	}{
		{
			name: "null target manifest",
			mutate: func(root, _, _ string) {
				write(t, root, filepath.Join(".symdesk", "history", "manifest", "target.md.json"), "null")
			},
		},
		{
			name: "null survivor manifest",
			mutate: func(root, _, _ string) {
				write(t, root, filepath.Join(".symdesk", "history", "manifest", "keep.md.json"), "null")
			},
		},
		{
			name: "missing referenced object",
			mutate: func(root, _, keepID string) {
				if err := os.Remove(filepath.Join(root, ".symdesk", "history", "objects", keepID)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt checkpoint",
			mutate: func(root, _, _ string) {
				write(t, root, filepath.Join(".symdesk", "history", "checkpoints", "task.json"), "not json")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, store := newVault(t)
			write(t, root, "target.md", "target")
			target, err := store.Snapshot("target.md")
			if err != nil {
				t.Fatal(err)
			}
			write(t, root, "keep.md", "keep")
			keep, err := store.Snapshot("keep.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CheckpointFile("task", "target.md"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CheckpointFile("task", "keep.md"); err != nil {
				t.Fatal(err)
			}

			manifestTarget := filepath.Join(root, ".symdesk", "history", "manifest", "target.md.json")
			manifestKeep := filepath.Join(root, ".symdesk", "history", "manifest", "keep.md.json")
			checkpoint := filepath.Join(root, ".symdesk", "history", "checkpoints", "task.json")
			tracked := []string{manifestTarget, manifestKeep, checkpoint}
			tt.mutate(root, target.ID, keep.ID)
			before := make(map[string][]byte, len(tracked))
			for _, path := range tracked {
				data, err := os.ReadFile(path) //nolint:gosec // path is constructed under this test vault
				if err != nil {
					t.Fatal(err)
				}
				before[path] = data
			}
			targetObject := filepath.Join(root, ".symdesk", "history", "objects", target.ID)
			keepObject := filepath.Join(root, ".symdesk", "history", "objects", keep.ID)
			_, targetBeforeErr := os.Stat(targetObject)
			_, keepBeforeErr := os.Stat(keepObject)

			if err := store.PurgePaths("target.md"); err == nil {
				t.Fatal("expected recovery metadata preflight to fail closed")
			}
			for _, path := range tracked {
				after, err := os.ReadFile(path) //nolint:gosec // path is constructed under this test vault
				if err != nil {
					t.Fatalf("preflight removed %s: %v", path, err)
				}
				if string(after) != string(before[path]) {
					t.Fatalf("preflight mutated %s", path)
				}
			}
			if _, err := os.Stat(targetObject); (err == nil) != (targetBeforeErr == nil) {
				t.Fatalf("target object existence changed: before=%v after=%v", targetBeforeErr, err)
			}
			if _, err := os.Stat(keepObject); (err == nil) != (keepBeforeErr == nil) {
				t.Fatalf("keep object existence changed: before=%v after=%v", keepBeforeErr, err)
			}
		})
	}
}

func TestTrashListStrictRejectsInvalidInventory(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(root string, entryName string)
	}{
		{
			name: "orphan payload",
			mutate: func(root, _ string) {
				write(t, root, filepath.Join(".symdesk", "trash", "orphan.md"), "orphan")
			},
		},
		{
			name: "orphan metadata",
			mutate: func(root, _ string) {
				write(t, root, filepath.Join(".symdesk", "trash", "orphan.md"+trashMetaSuffix), "{}")
			},
		},
		{
			name: "malformed metadata",
			mutate: func(root, entryName string) {
				write(t, root, filepath.Join(".symdesk", "trash", entryName+trashMetaSuffix), "not json")
			},
		},
		{
			name: "metadata name mismatch",
			mutate: func(root, entryName string) {
				path := filepath.Join(root, ".symdesk", "trash", entryName+trashMetaSuffix)
				data, err := os.ReadFile(path) //nolint:gosec // path is constructed under this test vault
				if err != nil {
					t.Fatal(err)
				}
				updated := strings.Replace(string(data), `"name": "`+entryName+`"`, `"name": "other.md"`, 1)
				write(t, root, filepath.Join(".symdesk", "trash", entryName+trashMetaSuffix), updated)
			},
		},
		{
			name: "metadata path mismatch",
			mutate: func(root, entryName string) {
				path := filepath.Join(root, ".symdesk", "trash", entryName+trashMetaSuffix)
				data, err := os.ReadFile(path) //nolint:gosec // path is constructed under this test vault
				if err != nil {
					t.Fatal(err)
				}
				updated := strings.Replace(string(data), `"original_path": "doc.md"`, `"original_path": "../outside.md"`, 1)
				write(t, root, filepath.Join(".symdesk", "trash", entryName+trashMetaSuffix), updated)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, store := newVault(t)
			write(t, root, "doc.md", "content")
			entry, err := store.Trash("doc.md")
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(root, entry.Name)
			if _, err := store.TrashListStrict(); err == nil {
				t.Fatal("expected strict trash inventory to fail closed")
			}
			if _, err := os.Stat(filepath.Join(root, ".symdesk", "trash", entry.Name)); err != nil {
				t.Fatalf("strict inventory must not mutate payload: %v", err)
			}
		})
	}
}
