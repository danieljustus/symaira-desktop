package retrieval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/engine"
)

func TestStatusReportsStoredRetrievalDegradation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = dbClient.Close() }()

	indexDocument := func(name, model string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		body := strings.Repeat(name+" content ", 40)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := engine.IndexStdin(dbClient, &fakeEmbedder{dim: 8, model: model}, strings.NewReader(body), path); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
	}

	indexDocument("semantic-a.md", "model-a")
	indexDocument("semantic-b.md", "model-b")
	indexDocument("pending.md", engine.LocalHashModelName)

	client := &Client{db: dbClient, embedder: &fakeEmbedder{dim: 8, model: "model-a"}}
	status, err := client.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.PendingChunkCount == 0 {
		t.Fatal("PendingChunkCount = 0, want stored pending chunks")
	}
	if !status.MixedEmbeddingSpaces {
		t.Fatal("MixedEmbeddingSpaces = false, want true for model-a + model-b")
	}
	if status.IndexScope != "shared" {
		t.Fatalf("IndexScope = %q, want shared", status.IndexScope)
	}
	if status.LastIndexedAt == "" {
		t.Fatal("LastIndexedAt is empty after indexing documents")
	}
}

func TestStatusDoesNotConfuseBackendOutageWithStoredDegradation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = dbClient.Close() }()

	client := &Client{db: dbClient, embedder: &fakeEmbedder{dim: 8, model: engine.LocalHashModelName}}
	status, err := client.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.BackendAvailable {
		t.Fatal("BackendAvailable = true, want false for local fallback")
	}
	if status.PendingChunkCount != 0 || status.MixedEmbeddingSpaces {
		t.Fatalf("stored degradation = pending %d, mixed %v; want clean empty index", status.PendingChunkCount, status.MixedEmbeddingSpaces)
	}
}
