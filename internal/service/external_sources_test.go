package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestSearchIncludesRegisteredExternalSourceInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	vaultRoot := t.TempDir()
	externalRoot := t.TempDir()
	externalFile := filepath.Join(externalRoot, "book.txt")
	const needle = "external-source-search-needle"
	if err := os.WriteFile(externalFile, []byte("A read-only external document with "+needle), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := retrieval.NewSourceRegistry(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(externalRoot); err != nil {
		t.Fatal(err)
	}

	client, err := retrieval.Open()
	if err != nil {
		t.Fatal(err)
	}
	canonicalFile := mustEvalSymlinks(t, externalFile)
	if err := client.Index(canonicalFile, ""); err != nil {
		_ = client.Close()
		t.Fatalf("index external file: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := New(vaultRoot, db)
	response, err := svc.SearchWithMeta(needle)
	if err != nil {
		t.Fatalf("SearchWithMeta: %v", err)
	}
	if len(response.Results) == 0 {
		t.Fatal("expected external document search result")
	}
	found := false
	for _, result := range response.Results {
		if result.Path == externalFile || result.Path == mustEvalSymlinks(t, externalFile) {
			found = true
			if result.SourceType != "external" || !result.ReadOnly {
				t.Fatalf("external result metadata = type %q read_only=%v", result.SourceType, result.ReadOnly)
			}
		}
	}
	if !found {
		t.Fatalf("external result not found in %+v", response.Results)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
