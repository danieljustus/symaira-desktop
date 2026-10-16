package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNoteCaseServiceClosesOwnedResources(t *testing.T) {
	root, db := newIsolatedVault(t)
	var svc *Service
	// Registered before the fixture owner, so assertions run after its Close
	// but before the sidecar and strict temporary-directory cleanup.
	t.Cleanup(func() {
		defer func() {
			if err := svc.Close(); err != nil {
				t.Errorf("fallback Service.Close: %v", err)
			}
		}()
		if _, err := svc.History.Snapshot("note.md"); !errors.Is(err, os.ErrClosed) {
			t.Errorf("fixture helper left owned history open: %v", err)
		}
		if svc.retrievalClient != nil {
			if err := svc.retrievalClient.Delete("missing"); err == nil {
				t.Error("fixture helper left owned retrieval client open")
			}
		}
	})
	svc = newNoteCaseService(t, root, db)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("fixture body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.History.Snapshot("note.md"); err != nil {
		t.Fatal(err)
	}
	client, release := svc.acquireRetrievalClient()
	release()
	if client == nil {
		t.Fatal("fixture did not open an owned retrieval client")
	}
	if err := client.Delete("missing"); err != nil {
		t.Fatalf("owned client is not usable before cleanup: %v", err)
	}
}
