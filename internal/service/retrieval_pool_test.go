package service

import (
	"path/filepath"
	"testing"

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
	if client := svc.getRetrievalClient(); client != nil {
		_ = client.Close()
		t.Fatal("closed service opened a late retrieval client")
	}
}

func TestServiceCloseClosesOwnedRetrievalClient(t *testing.T) {
	vaultRoot, db := serviceRetrievalTestSetup(t)
	svc := New(vaultRoot, db)
	client := svc.getRetrievalClient()
	if client == nil {
		t.Fatal("owned retrieval client was not opened")
	}
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
	client := svc.getRetrievalClient()
	if client == nil {
		t.Fatal("pooled retrieval client was not opened")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	if _, err := svc.Search("invalidated-search"); err != nil {
		t.Fatalf("Search should fall back after retrieval error: %v", err)
	}
	replacement := svc.getRetrievalClient()
	if replacement == nil {
		t.Fatal("pool did not open replacement client")
	}
	if replacement == client {
		t.Fatal("pool returned invalidated client")
	}
	if err := replacement.Delete("missing"); err != nil {
		t.Fatalf("replacement retrieval client is not usable: %v", err)
	}
}
