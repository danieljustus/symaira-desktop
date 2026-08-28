package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestScopesDeduplicationToDestinationVault(t *testing.T) {
	tempDir := t.TempDir()
	source := filepath.Join(tempDir, "document.txt")
	if err := os.WriteFile(source, []byte("same content in two vaults"), 0o600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tempDir, "shared", "symingest.db")
	vaultA := filepath.Join(tempDir, "vault-a")
	vaultB := filepath.Join(tempDir, "vault-b")
	ctx := context.Background()

	resA, err := Ingest(ctx, source, Options{
		Vault:   vaultA,
		Archive: filepath.Join(vaultA, "archive"),
		DBPath:  dbPath,
	})
	if err != nil {
		t.Fatalf("ingest into vault A: %v", err)
	}

	resB, err := Ingest(ctx, source, Options{
		Vault:   vaultB,
		Archive: filepath.Join(vaultB, "archive"),
		DBPath:  dbPath,
	})
	if err != nil {
		t.Fatalf("same content should ingest into vault B: %v", err)
	}
	if resA.VaultPath == resB.VaultPath {
		t.Fatalf("vault A and B received the same note path: %q", resA.VaultPath)
	}
	if _, err := os.Stat(resA.VaultPath); err != nil {
		t.Fatalf("vault A note missing: %v", err)
	}
	if _, err := os.Stat(resB.VaultPath); err != nil {
		t.Fatalf("vault B note missing: %v", err)
	}

	_, err = Ingest(ctx, source, Options{
		Vault:   vaultB,
		Archive: filepath.Join(vaultB, "archive"),
		DBPath:  dbPath,
	})
	if err == nil {
		t.Fatal("expected a duplicate on the second ingest into vault B")
	}
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), filepath.Clean(vaultB)) {
		t.Fatalf("duplicate error %q does not identify vault B %q", err, vaultB)
	}
	if strings.Contains(err.Error(), filepath.Clean(vaultA)) {
		t.Fatalf("duplicate error incorrectly identifies vault A: %q", err)
	}
}
