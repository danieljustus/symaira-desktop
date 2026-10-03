package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/history"
)

func historyLifetimeHome(t *testing.T) {
	t.Helper()
	home := canonicalOracleTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func historyLifetimeNote(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("# History lifetime\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServiceCloseReleasesOwnedHistory(t *testing.T) {
	historyLifetimeHome(t)
	root := t.TempDir()
	historyLifetimeNote(t, root)
	svc := New(root, nil)
	owned := svc.History
	t.Cleanup(func() { _ = svc.Close(); _ = owned.Close() })
	if _, err := owned.Snapshot("note.md"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := svc.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owned.Snapshot("note.md"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("owned history remained usable after Service.Close: %v", err)
	}
	// Windows refuses to remove a vault while its confined root handle remains open.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("Service.Close retained the owned vault handle: %v", err)
	}
}

func TestServiceCloseBeforeHistoryUsePreventsLateRoot(t *testing.T) {
	historyLifetimeHome(t)
	root := filepath.Join(t.TempDir(), "later-vault")
	svc := New(root, nil)
	t.Cleanup(func() { _ = svc.Close(); _ = svc.History.Close() })
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	historyLifetimeNote(t, root)
	if _, err := svc.History.Snapshot("note.md"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed service opened a late history root: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "note.md" {
		t.Fatalf("closed history mutated the later vault: entries=%v error=%v", entries, err)
	}
}

func TestServiceClosePreservesBorrowedHistory(t *testing.T) {
	for _, mode := range []string{"injected", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			historyLifetimeHome(t)
			root := t.TempDir()
			historyLifetimeNote(t, root)
			borrowed := history.NewStore(root)
			t.Cleanup(func() { _ = borrowed.Close() })
			svc := &Service{VaultRoot: root, History: borrowed}
			if mode == "replaced" {
				ownedRoot := t.TempDir()
				historyLifetimeNote(t, ownedRoot)
				svc = New(ownedRoot, nil)
				owned := svc.History
				t.Cleanup(func() { _ = owned.Close() })
				if _, err := owned.Snapshot("note.md"); err != nil {
					t.Fatal(err)
				}
				svc.History = borrowed
				t.Cleanup(func() { _ = svc.Close() })
				if err := svc.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := owned.Snapshot("note.md"); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("replaced owned history was not released: %v", err)
				}
			}
			if err := svc.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := borrowed.Snapshot("note.md"); err != nil {
				t.Fatalf("Service.Close closed caller-owned history: %v", err)
			}
		})
	}
}
