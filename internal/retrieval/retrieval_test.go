package retrieval

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/engine"
)

// Index/Delete/Search swallow failures on purpose. CurrentStatus must not:
// a caller asking whether retrieval is healthy has to hear "unknown" rather
// than a confident zero, which would read as "the index is empty".
func TestCurrentStatusPropagatesTheFailure(t *testing.T) {
	prev := StatusFunc
	t.Cleanup(func() { StatusFunc = prev })
	wantErr := errors.New("index unavailable")
	StatusFunc = func() (*Status, error) { return nil, wantErr }

	status, err := CurrentStatus()

	if !errors.Is(err, wantErr) {
		t.Fatalf("CurrentStatus() error = %v, want %v", err, wantErr)
	}
	if status != nil {
		t.Errorf("CurrentStatus() = %+v, want nil on failure", status)
	}
}

func TestCurrentStatusReportsADegradedBackend(t *testing.T) {
	prev := StatusFunc
	t.Cleanup(func() { StatusFunc = prev })
	StatusFunc = func() (*Status, error) {
		return &Status{DocumentCount: 12, EmbeddingModel: "local-hash", BackendAvailable: false}, nil
	}

	status, err := CurrentStatus()
	if err != nil {
		t.Fatalf("CurrentStatus() error = %v", err)
	}
	if status.BackendAvailable {
		t.Error("BackendAvailable = true, want false for the fallback model")
	}
	if status.DocumentCount != 12 {
		t.Errorf("DocumentCount = %d, want 12", status.DocumentCount)
	}
}

// BackendAvailable is the difference between "search works" and "search
// silently ranks worse", so the fallback model name must never be reported as
// an available backend.
func TestLocalHashModelIsNotAnAvailableBackend(t *testing.T) {
	if engine.LocalHashModelName == "" {
		t.Fatal("LocalHashModelName is empty; the availability check would treat every model as a fallback")
	}
	if engine.LocalHashModelName == "qwen3-embedding:0.6b" {
		t.Fatal("the fallback model name collides with a real embedding model")
	}
}

func TestSeamOverrides(t *testing.T) {
	prevIndex := IndexFunc
	prevDelete := DeleteFunc
	prevSearch := SearchFunc
	prevStatus := StatusFunc
	t.Cleanup(func() {
		IndexFunc = prevIndex
		DeleteFunc = prevDelete
		SearchFunc = prevSearch
		StatusFunc = prevStatus
	})

	var indexedDoc, indexedBody string
	IndexFunc = func(doc, body string) error {
		indexedDoc = doc
		indexedBody = body
		return nil
	}
	Index("doc1.md", "hello world")
	if indexedDoc != "doc1.md" || indexedBody != "hello world" {
		t.Errorf("Index did not invoke IndexFunc properly")
	}

	var deletedDoc string
	DeleteFunc = func(doc string) error {
		deletedDoc = doc
		return nil
	}
	Delete("doc1.md")
	if deletedDoc != "doc1.md" {
		t.Errorf("Delete did not invoke DeleteFunc properly")
	}

	SearchFunc = func(query string, limit int) ([]Result, error) {
		return []Result{{Path: "doc1.md", Score: 0.9, Snippet: "hello"}}, nil
	}
	results := Search("hello")
	if len(results) != 1 || results[0].Path != "doc1.md" {
		t.Errorf("Search did not invoke SearchFunc properly")
	}

	StatusFunc = func() (*Status, error) {
		return &Status{DocumentCount: 5, BackendAvailable: true}, nil
	}
	st, err := CurrentStatus()
	if err != nil || st.DocumentCount != 5 {
		t.Errorf("CurrentStatus did not invoke StatusFunc properly")
	}
}

// TestSearchPropagatesMixedSpaceError verifies that Client.Search surfaces the
// mixed-embedding-space error from the engine instead of silently returning an
// empty result (#663/#681). A degraded index must not look like "no hits".
func TestSearchPropagatesMixedSpaceError(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	// Two documents indexed with different models create a mixed embedding space.
	embA := &fakeEmbedder{dim: 8, model: "model-a"}
	embB := &fakeEmbedder{dim: 8, model: "model-b"}

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = dbClient.Close() }()

	docA := filepath.Join(tempDir, "a.md")
	docB := filepath.Join(tempDir, "b.md")
	if err := os.WriteFile(docA, []byte(strings.Repeat("alpha beta ", 50)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docB, []byte(strings.Repeat("gamma delta ", 50)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.IndexStdin(dbClient, embA, strings.NewReader(strings.Repeat("alpha beta ", 50)), docA); err != nil {
		t.Fatalf("index a: %v", err)
	}
	if err := engine.IndexStdin(dbClient, embB, strings.NewReader(strings.Repeat("gamma delta ", 50)), docB); err != nil {
		t.Fatalf("index b: %v", err)
	}

	c := &Client{db: dbClient, embedder: embA}
	_, err = c.Search("alpha", 5)
	if err == nil {
		t.Fatal("expected Client.Search to propagate the mixed-space error, got nil")
	}
}

// TestSearchPropagatesVectorMode verifies the VectorMode set by the engine is
// carried through to retrieval.Result so a consumer can warn about unreliable
// semantic scores (#663/#681). When the query embedding falls back to the
// local hash while the index uses a real model, the hit is marked "fallback".
func TestSearchPropagatesVectorMode(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	// Index with a real (Ollama-like) model.
	indexEmbedder := &fakeEmbedder{dim: 8, model: "fake-model"}
	// Query with the fallback embedder so the engine flags the hit as "fallback".
	queryEmbedder := &fakeEmbedder{dim: 8, model: engine.LocalHashModelName}

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = dbClient.Close() }()

	doc := filepath.Join(tempDir, "doc.md")
	body := strings.Repeat("semantic search needs real embeddings ", 50)
	if err := os.WriteFile(doc, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.IndexStdin(dbClient, indexEmbedder, strings.NewReader(body), doc); err != nil {
		t.Fatalf("index: %v", err)
	}

	c := &Client{db: dbClient, embedder: queryEmbedder}
	results, err := c.Search("semantic search", 5)
	if err != nil {
		t.Fatalf("Client.Search error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	for _, r := range results {
		if r.VectorMode != "fallback" {
			t.Errorf("Result.VectorMode = %q, want %q", r.VectorMode, "fallback")
		}
	}
}

// fakeEmbedder is a minimal engine.Embedder double for facade-level tests.
type fakeEmbedder struct {
	dim   int
	model string
}

func (f *fakeEmbedder) GenerateVector(text string) []float32 {
	return make([]float32, f.dim)
}
func (f *fakeEmbedder) GenerateVectors(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, f.dim)
	}
	return out
}
func (f *fakeEmbedder) GenerateVectorsWithModel(texts []string) []engine.EmbeddingResult {
	out := make([]engine.EmbeddingResult, len(texts))
	for i := range texts {
		out[i] = engine.EmbeddingResult{Vector: make([]float32, f.dim), Model: f.model}
	}
	return out
}
func (f *fakeEmbedder) GenerateVectorNoRetry(text string) []float32 {
	return make([]float32, f.dim)
}
func (f *fakeEmbedder) GenerateVectorNoRetryWithModel(text string) engine.EmbeddingResult {
	return engine.EmbeddingResult{Vector: make([]float32, f.dim), Model: f.model}
}
func (f *fakeEmbedder) Dim() int          { return f.dim }
func (f *fakeEmbedder) ModelName() string { return f.model }
