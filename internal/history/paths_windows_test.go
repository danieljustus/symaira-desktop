package history

import (
	"os"
	"testing"
)

func TestWindowsPurgeRejectsRootAndVolumeCoordinates(t *testing.T) {
	root, store := newVault(t)
	for _, path := range []string{`/absolute.md`, `\absolute.md`, `C:\outside.md`, `C:outside.md`, `\\server\share\outside.md`} {
		if err := store.PurgePaths(path); err == nil {
			t.Errorf("purge accepted %q", path)
		}
		if removed, err := store.PurgeTrashPaths(path); err == nil || removed != 0 {
			t.Errorf("trash purge accepted %q: removed=%d err=%v", path, removed, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected paths mutated the vault: entries=%v err=%v", entries, err)
	}
}
