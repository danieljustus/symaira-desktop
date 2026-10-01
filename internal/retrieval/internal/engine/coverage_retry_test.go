package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestStatusProbeAutoDetectsEmbeddingDimension(t *testing.T) {
	var dimensionsPresent bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode embedding request: %v", err)
		}
		_, dimensionsPresent = request["dimensions"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.25, 0.75}}},
		})
	}))
	defer server.Close()

	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:        server.URL,
		Model:      "test-model",
		RetryCount: 0,
	})
	if generator.Dim() != defaultEmbeddingDim {
		t.Fatalf("initial dimension = %d, want compatibility default %d", generator.Dim(), defaultEmbeddingDim)
	}

	result, err := generator.GenerateVectorNoRetryWithModelContext(context.Background(), "dimension probe")
	if err != nil {
		t.Fatalf("status probe error = %v", err)
	}
	if result.Model != "test-model" || len(result.Vector) != 2 {
		t.Fatalf("status probe result = model %q, %d dimensions; want test-model with 2 dimensions", result.Model, len(result.Vector))
	}
	if generator.Dim() != 2 {
		t.Fatalf("detected dimension = %d, want 2", generator.Dim())
	}
	if dimensionsPresent {
		t.Fatal("auto-detect request included a dimensions constraint")
	}
}

func TestMalformedOllamaURLUsesLocalHashFallback(t *testing.T) {
	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:        "http:///api/embeddings",
		Model:      "test-model",
		Dim:        3,
		RetryCount: 0,
	})

	result := generator.GenerateVectorNoRetryWithModel("offline fallback")
	if result.Model != LocalHashModelName || len(result.Vector) != 3 {
		t.Fatalf("result = model %q, %d dimensions; want local hash with 3 dimensions", result.Model, len(result.Vector))
	}

	statusResult, err := generator.GenerateVectorNoRetryWithModelContext(context.Background(), "status fallback")
	if err != nil {
		t.Fatalf("status probe error = %v", err)
	}
	if statusResult.Model != LocalHashModelName || len(statusResult.Vector) != 3 {
		t.Fatalf("status result = model %q, %d dimensions; want local hash with 3 dimensions", statusResult.Model, len(statusResult.Vector))
	}
}

func TestIndexFileWithSourcePreservesArchivePathAndMetadata(t *testing.T) {
	store, err := db.OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		t.Fatalf("db.OpenAt: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	embedder := &fakeEmbedder{dim: 8}
	root := t.TempDir()

	archivePath := filepath.Join(root, "archive", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("original PDF bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	generatedNote := filepath.Join(root, "generated-report.md")
	if err := os.WriteFile(generatedNote, []byte("# Report Note\n\narchive-body-marker appears in extracted text.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveHash, err := IndexFileWithSource(store, embedder, archivePath, generatedNote)
	if err != nil {
		t.Fatalf("IndexFileWithSource: %v", err)
	}
	archiveDoc, err := store.GetDocument(archivePath)
	if err != nil || archiveDoc == nil {
		t.Fatalf("archive document = %v, error %v", archiveDoc, err)
	}
	if archiveDoc.Hash != archiveHash {
		t.Fatalf("archive document hash = %q, returned hash = %q", archiveDoc.Hash, archiveHash)
	}
	archiveHits, err := store.SearchBM25("archive-body-marker", 5)
	if err != nil {
		t.Fatalf("search archive body: %v", err)
	}
	if len(archiveHits) == 0 || archiveHits[0].Chunk.DocumentPath != archivePath || !strings.Contains(archiveHits[0].Chunk.Content, "archive-body-marker") {
		t.Fatalf("archive source hit = %+v, want extracted content anchored at original PDF path", archiveHits)
	}

	metadataArchivePath := filepath.Join(root, "archive", "metadata-report.pdf")
	if err := os.WriteFile(metadataArchivePath, []byte("second original PDF bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadataNote := filepath.Join(root, "generated-metadata-report.md")
	if err := os.WriteFile(metadataNote, []byte("# Metadata Note\n\nThe body contains no special catalog term.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadataHash, err := IndexFileWithSourceAndMetadata(store, embedder, metadataArchivePath, metadataNote, SearchMetadata{Fields: []SearchMetadataField{{Name: "tags", Value: "archive-catalog-marker", Weight: 2}}})
	if err != nil {
		t.Fatalf("IndexFileWithSourceAndMetadata: %v", err)
	}
	metadataDoc, err := store.GetDocument(metadataArchivePath)
	if err != nil || metadataDoc == nil || metadataDoc.Hash != metadataHash {
		var storedHash string
		if metadataDoc != nil {
			storedHash = metadataDoc.Hash
		}
		t.Fatalf("metadata archive document hash = %q, returned hash = %q, error = %v", storedHash, metadataHash, err)
	}
	metadataHits, err := store.SearchBM25("archive-catalog-marker", 5)
	if err != nil {
		t.Fatalf("search archive metadata: %v", err)
	}
	if len(metadataHits) == 0 || metadataHits[0].Chunk.DocumentPath != metadataArchivePath || !strings.Contains(metadataHits[0].Chunk.Content, "archive-catalog-marker") {
		t.Fatalf("archive metadata hit = %+v, want metadata indexed under original PDF path", metadataHits)
	}
}
