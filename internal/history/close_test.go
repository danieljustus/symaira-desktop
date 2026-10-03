package history

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCloseReleasesVaultForRemoval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, root, "note.md", "before removal")
	store := NewStore(root)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Snapshot("note.md"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if _, err := store.Snapshot("note.md"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed store accepted a new operation: %v", err)
	}
	// Native Windows refuses this removal if Store still owns an open root.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("closed history retains vault handle: %v", err)
	}
}

func TestCloseUnusedStoreDoesNotOpenOrCreateVault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	store := NewStore(root)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, root, "note.md", "later vault")
	if _, err := store.Snapshot("note.md"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("unused closed store reopened a later vault: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".symdesk")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed store wrote history metadata: %v", err)
	}
}
