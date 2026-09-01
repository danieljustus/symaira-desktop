package retrieval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestPublicFacadeSwallowsOptionalIndexErrors(t *testing.T) {
	prevIndex, prevDelete, prevSearch := IndexFunc, DeleteFunc, SearchFunc
	t.Cleanup(func() {
		IndexFunc, DeleteFunc, SearchFunc = prevIndex, prevDelete, prevSearch
	})
	IndexFunc = func(string, string) error { return errors.New("index unavailable") }
	DeleteFunc = func(string) error { return errors.New("delete unavailable") }
	SearchFunc = func(string, int) ([]Result, error) { return nil, errors.New("search unavailable") }

	Index("note.md", "body")
	Delete("note.md")
	if results := Search("needle"); results != nil {
		t.Fatalf("Search on unavailable index = %#v, want nil fallback", results)
	}
}

func TestPackageFacadeRoundTripUsesConfiguredIndex(t *testing.T) {
	home := t.TempDir()
	indexPath := filepath.Join(t.TempDir(), "retrieval.db")
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "index_path = \"" + indexPath + "\"\nollama_url = \"http://127.0.0.1:1/api/embeddings\"\nembedding_dim = 8\ntimeout_seconds = 1\nretry_count = 0\nmodel = \"test-model\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	sourceDir := t.TempDir()
	directoryPath := filepath.Join(sourceDir, "directory.md")
	metadataPath := filepath.Join(sourceDir, "metadata.md")
	if err := os.WriteFile(directoryPath, []byte("# Directory\n\nunique-directory-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte("# Metadata\n\nordinary body"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := IndexDirectory(sourceDir); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if err := IndexWithMetadata(metadataPath, "", SearchMetadata{Fields: []SearchMetadataField{{Name: "title", Value: "metadataonlymarker", Weight: 4}}}); err != nil {
		t.Fatalf("IndexWithMetadata: %v", err)
	}
	results, err := SearchInPaths("metadataonlymarker", []string{sourceDir}, 5)
	if err != nil {
		t.Fatalf("SearchInPaths: %v", err)
	}
	if len(results) == 0 || results[0].Path != metadataPath {
		t.Fatalf("SearchInPaths results = %#v, want metadata document ranked first", results)
	}
	if len(results[0].MetadataMatches) != 1 || results[0].MetadataMatches[0] != "title" {
		t.Fatalf("metadata matches = %#v, want title match", results[0].MetadataMatches)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WatchDirectory(ctx, sourceDir); err != nil {
		t.Fatalf("WatchDirectory cancelled: %v", err)
	}
	removed, err := RemoveDirectory(sourceDir)
	if err != nil {
		t.Fatalf("RemoveDirectory: %v", err)
	}
	if removed != 2 {
		t.Fatalf("RemoveDirectory removed %d documents, want 2", removed)
	}
	if _, err := os.Stat(directoryPath); err != nil {
		t.Fatalf("directory source disappeared: %v", err)
	}

	Index("stdin-source", "package-wrapper-marker")
	if results := Search("package-wrapper-marker"); len(results) != 1 || results[0].Path != "stdin-source" {
		t.Fatalf("package Search results = %#v, want stdin source", results)
	}
	Delete("stdin-source")
	if results := Search("package-wrapper-marker"); len(results) != 0 {
		t.Fatalf("package Search after Delete = %#v, want empty", results)
	}

	if pending, err := CountPendingChunks(); err != nil || pending != 0 {
		t.Fatalf("CountPendingChunks = %d, %v, want empty index", pending, err)
	}
	if reembedded, err := ReembedPending(); err != nil || reembedded != 0 {
		t.Fatalf("ReembedPending = %d, %v, want no documents", reembedded, err)
	}
	status, err := CurrentStatus()
	if err != nil {
		t.Fatalf("CurrentStatus: %v", err)
	}
	if status.IndexScope != "shared" || status.DocumentCount != 0 {
		t.Fatalf("CurrentStatus = %+v, want empty shared index", status)
	}
}

func TestClientIndexWithMetadataUsesBodyFallbackForNonLocalSource(t *testing.T) {
	dbClient, err := db.OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbClient.Close() }()
	client := &Client{db: dbClient, embedder: &fakeEmbedder{dim: 8, model: "test-model"}}
	if err := client.IndexWithMetadata("stdin:metadata", strings.Repeat("body-marker ", 10), SearchMetadata{Fields: []SearchMetadataField{{Name: "title", Value: "ignored-metadata"}}}); err != nil {
		t.Fatalf("IndexWithMetadata body fallback: %v", err)
	}
	doc, err := dbClient.GetDocument("stdin:metadata")
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil {
		t.Fatal("body fallback did not create a document")
	}
}
