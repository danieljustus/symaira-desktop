package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestIndexFileWithMetadata_SearchesDistinctiveTagAndReportsMetadataMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "fixture.md")
	if err := os.WriteFile(path, []byte("---\ntitle: Fixture\ntags: [distinctive-665]\n---\n\nThe body has no tag term.\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := IndexFile(store, &fakeEmbedder{dim: 32}, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	results, err := SearchHybrid(store, store, &fakeEmbedder{dim: 32}, "distinctive-665", 5)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected distinctive tag to be retrievable")
	}
	if results[0].Chunk.DocumentPath != path {
		t.Fatalf("path = %q, want %q", results[0].Chunk.DocumentPath, path)
	}
	if !containsString(results[0].MetadataMatches, "tags") {
		t.Fatalf("metadata matches = %v, want tags", results[0].MetadataMatches)
	}
}

func TestMetadataTitleAndTagRankAboveIncidentalBodyMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	embedder := &zeroEmbedder{dim: 32}
	bodyOnly := filepath.Join(t.TempDir(), "body.md")
	metadataHit := filepath.Join(t.TempDir(), "metadata.md")
	for path, content := range map[string]string{
		bodyOnly:    "---\ntitle: Body\n---\n\nAn incidental priority-665 mention.\n",
		metadataHit: "---\ntitle: priority-665 record\ntags: [archive]\n---\n\nNo incidental terms here.\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if _, err := IndexFileWithMetadata(store, embedder, bodyOnly, SearchMetadata{Fields: []SearchMetadataField{{Name: "title", Value: "Body"}}}); err != nil {
		t.Fatalf("index body-only: %v", err)
	}
	if _, err := IndexFileWithMetadata(store, embedder, metadataHit, SearchMetadata{Fields: []SearchMetadataField{{Name: "title", Value: "priority-665 record", Weight: 4}, {Name: "tags", Value: "archive", Weight: 4}}}); err != nil {
		t.Fatalf("index metadata hit: %v", err)
	}

	results, err := SearchHybrid(store, store, embedder, "priority-665", 5)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("results = %d, want both metadata and body hits", len(results))
	}
	if results[0].Chunk.DocumentPath != metadataHit {
		t.Fatalf("top result = %q, want metadata hit %q", results[0].Chunk.DocumentPath, metadataHit)
	}
}

func TestMetadataOnlyChangeReplacesIndexedRepresentation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	path := filepath.Join(t.TempDir(), "metadata-only.md")
	if err := os.WriteFile(path, []byte("---\ntitle: Stable\ntags: [old-665]\n---\n\nBody stays byte-for-byte stable.\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	embedder := &zeroEmbedder{dim: 32}
	index := func(tag string) error {
		_, err := IndexFileWithMetadata(store, embedder, path, SearchMetadata{Fields: []SearchMetadataField{{Name: "title", Value: "Stable", Weight: 4}, {Name: "tags", Value: tag, Weight: 4}}})
		return err
	}
	if err := index("old-665"); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if err := os.WriteFile(path, []byte("---\ntitle: Stable\ntags: [new-665]\n---\n\nBody stays byte-for-byte stable.\n"), 0600); err != nil {
		t.Fatalf("update fixture metadata: %v", err)
	}
	if err := index("new-665"); err != nil {
		t.Fatalf("second index: %v", err)
	}

	oldResults, err := store.SearchBM25("old-665", 5)
	if err != nil {
		t.Fatalf("old search: %v", err)
	}
	if len(oldResults) != 0 {
		t.Fatalf("old metadata still searchable: %#v", oldResults)
	}
	newResults, err := store.SearchBM25("new-665", 5)
	if err != nil {
		t.Fatalf("new search: %v", err)
	}
	if len(newResults) == 0 || newResults[0].Chunk.DocumentPath != path {
		t.Fatalf("new metadata results = %#v", newResults)
	}
}

func TestSearchMetadataRepresentationIsCanonical(t *testing.T) {
	got := formatSearchMetadata(SearchMetadata{Fields: []SearchMetadataField{
		{Name: "tags", Value: "beta", Weight: 99},
		{Name: "title", Value: "alpha", Weight: 2},
	}})
	want := searchMetadataStart + "\ntitle: alpha\ntitle: alpha\ntags: beta\ntags: beta\ntags: beta\ntags: beta\n" + searchMetadataEnd
	if got != want {
		t.Fatalf("metadata representation = %q, want %q", got, want)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type zeroEmbedder struct{ dim int }

func (z *zeroEmbedder) GenerateVector(string) []float32 { return make([]float32, z.dim) }
func (z *zeroEmbedder) GenerateVectors(texts []string) [][]float32 {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = make([]float32, z.dim)
	}
	return vectors
}
func (z *zeroEmbedder) GenerateVectorsWithModel(texts []string) []EmbeddingResult {
	results := make([]EmbeddingResult, len(texts))
	for i := range results {
		results[i] = EmbeddingResult{Vector: make([]float32, z.dim), Model: "metadata-test"}
	}
	return results
}
func (z *zeroEmbedder) GenerateVectorNoRetry(string) []float32 { return make([]float32, z.dim) }
func (z *zeroEmbedder) GenerateVectorNoRetryWithModel(string) EmbeddingResult {
	return EmbeddingResult{Vector: make([]float32, z.dim), Model: "metadata-test"}
}
func (z *zeroEmbedder) Dim() int          { return z.dim }
func (z *zeroEmbedder) ModelName() string { return "metadata-test" }
