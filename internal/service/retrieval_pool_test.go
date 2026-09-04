package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func serviceRetrievalTestSetup(t *testing.T) (string, *sidecar.DB) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vaultRoot := t.TempDir()
	db, err := sidecar.Open(filepath.Join(vaultRoot, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return vaultRoot, db
}

func TestNewWithRetrievalClientCloseDoesNotCloseBorrowedClient(t *testing.T) {
	vaultRoot, db := serviceRetrievalTestSetup(t)
	client, err := retrieval.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithRetrievalClient(vaultRoot, db, client)
	if err := svc.Close(); err != nil {
		t.Fatalf("Service.Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("second Service.Close: %v", err)
	}
	if err := client.Delete("missing"); err != nil {
		t.Fatalf("borrowed client was closed by Service.Close: %v", err)
	}
	_ = client.Close()
}

func TestServiceCloseBeforeRetrievalOpenPreventsLateClient(t *testing.T) {
	vaultRoot, db := serviceRetrievalTestSetup(t)
	svc := New(vaultRoot, db)
	if err := svc.Close(); err != nil {
		t.Fatalf("Service.Close: %v", err)
	}
	client, release := svc.acquireRetrievalClient()
	release()
	if client != nil {
		_ = client.Close()
		t.Fatal("closed service opened a late retrieval client")
	}
}

func TestServiceCloseWaitsForActiveRetrievalLease(t *testing.T) {
	vaultRoot, db := serviceRetrievalTestSetup(t)
	svc := New(vaultRoot, db)
	client, release := svc.acquireRetrievalClient()
	if client == nil {
		t.Fatal("owned retrieval client was not opened")
	}
	done := make(chan error, 1)
	go func() { done <- svc.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Service.Close returned before lease release: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Service.Close: %v", err)
	}
	if err := client.Delete("missing"); err == nil {
		t.Fatal("owned client remained open after lease-aware close")
	}
}

func TestServiceCloseClosesOwnedRetrievalClient(t *testing.T) {
	vaultRoot, db := serviceRetrievalTestSetup(t)
	svc := New(vaultRoot, db)
	client, release := svc.acquireRetrievalClient()
	if client == nil {
		t.Fatal("owned retrieval client was not opened")
	}
	release()
	if err := svc.Close(); err != nil {
		t.Fatalf("Service.Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("second Service.Close: %v", err)
	}
	if err := client.Delete("missing"); err == nil {
		t.Fatal("owned retrieval client remained open after Service.Close")
	}
}

func TestServicePoolInvalidatesClientAfterSearchError(t *testing.T) {
	vaultRoot, db := serviceRetrievalTestSetup(t)
	pool := retrieval.NewClientPool()
	t.Cleanup(func() { _ = pool.Close() })
	svc := NewWithRetrievalPool(vaultRoot, db, pool)
	client, release := svc.acquireRetrievalClient()
	if client == nil {
		t.Fatal("pooled retrieval client was not opened")
	}
	release()
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	if _, err := svc.Search("invalidated-search"); err != nil {
		t.Fatalf("Search should fall back after retrieval error: %v", err)
	}
	replacement, replacementRelease := svc.acquireRetrievalClient()
	if replacement == nil {
		t.Fatal("pool did not open replacement client")
	}
	defer replacementRelease()
	if replacement == client {
		t.Fatal("pool returned invalidated client")
	}
	if err := replacement.Delete("missing"); err != nil {
		t.Fatalf("replacement retrieval client is not usable: %v", err)
	}
}
