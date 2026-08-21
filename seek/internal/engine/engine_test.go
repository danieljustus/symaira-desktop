package engine

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// TestHashKeyEntropy is a regression test for issue #39. The cache key
// must contain enough entropy that distinct texts are statistically
// guaranteed to produce distinct keys, even at the 10K-entry cache size
// (and well beyond).
func TestHashKeyEntropy(t *testing.T) {
	if got := hashKey("anything"); len(got) != 32 {
		t.Errorf("expected 32 hex chars (128 bits) in cache key, got %d (%q)", len(got), got)
	}

	h := hashKey("alpha")
	if h != hashKey("alpha") {
		t.Error("hashKey must be deterministic")
	}

	if hashKey("alpha") == hashKey("beta") {
		t.Error("hashKey must distinguish different inputs")
	}
}

func TestLocalHashVector(t *testing.T) {
	vec1 := GenerateLocalHashVector("Hello Symaira Seek", 768)
	vec2 := GenerateLocalHashVector("Hello Symaira Seek", 768)
	vec3 := GenerateLocalHashVector("Something else entirely", 768)

	if len(vec1) != 768 {
		t.Errorf("expected vector size 768, got %d", len(vec1))
	}

	// Verify determinism
	for i := range vec1 {
		if vec1[i] != vec2[i] {
			t.Errorf("expected deterministic vector generation")
			break
		}
	}

	// Verify L2 normalization
	var sumSquares float64
	for _, val := range vec1 {
		sumSquares += float64(val * val)
	}
	if math.Abs(sumSquares-1.0) > 1e-5 {
		t.Errorf("expected normalized L2 norm ~1.0, got %f", sumSquares)
	}

	// Cosine similarity with self should be ~1.0
	simSelf := db.CosineSimilarity(vec1, vec2)
	if math.Abs(float64(simSelf-1.0)) > 1e-5 {
		t.Errorf("expected cosine similarity with self to be 1.0, got %f", simSelf)
	}

	// Cosine similarity with different string should be lower
	simDiff := db.CosineSimilarity(vec1, vec3)
	if simDiff >= 0.99 {
		t.Errorf("expected different texts to have lower similarity, got %f", simDiff)
	}
}

// TestNewEmbeddingsGeneratorWithConfig is a regression test for issue #29.
// The factory must return a fully wired EmbeddingsGenerator (HTTP client,
// LRU cache, mutex) that produces vectors without a nil-pointer panic.
// Bare struct construction at CLI/MCP/HTTP call sites was the original
// runtime denial of service and is exercised by no test in this repo.
func TestNewEmbeddingsGeneratorWithConfig(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithConfig("http://localhost:11434/api/embeddings", "nomic-embed-text")
	if eg == nil {
		t.Fatal("expected non-nil EmbeddingsGenerator")
	}
	eg.sleepFn = func(time.Duration) {}
	if eg.OllamaURL == "" {
		t.Error("expected OllamaURL to be set from config")
	}
	if eg.Model == "" {
		t.Error("expected Model to be set from config")
	}

	vec := eg.GenerateVector("regression test for #29")
	if len(vec) != 768 {
		t.Errorf("expected 768-dim vector, got %d", len(vec))
	}

	vec2 := eg.GenerateVector("regression test for #29")
	if len(vec2) != 768 {
		t.Errorf("expected cached vector of 768-dim, got %d", len(vec2))
	}

	batch := eg.GenerateVectors([]string{"alpha", "beta", "gamma"})
	if len(batch) != 3 {
		t.Errorf("expected 3 vectors from batch, got %d", len(batch))
	}
	for i, v := range batch {
		if len(v) != 768 {
			t.Errorf("batch index %d: expected 768-dim vector, got %d", i, len(v))
		}
	}
}

func newTestEmbeddingsGenerator() *EmbeddingsGenerator {
	eg := NewEmbeddingsGenerator()
	eg.sleepFn = func(time.Duration) {}
	return eg
}

// fakeEmbedder is a deterministic in-memory Embedder used to prove that
// SearchHybrid and the indexer accept the interface, not the concrete
// struct, and that the tests can substitute behavior without Ollama.
type fakeEmbedder struct {
	dim int
}

func (f *fakeEmbedder) GenerateVector(text string) []float32 {
	vec := make([]float32, f.dim)
	for i, b := range []byte(text) {
		vec[i%f.dim] += float32(b) / 255.0
	}
	var sumSquares float64
	for _, v := range vec {
		sumSquares += float64(v * v)
	}
	if sumSquares > 0 {
		norm := float32(math.Sqrt(sumSquares))
		for i := range vec {
			vec[i] /= norm
		}
	} else {
		vec[0] = 1.0
	}
	return vec
}

func (f *fakeEmbedder) GenerateVectors(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.GenerateVector(t)
	}
	return out
}

func (f *fakeEmbedder) GenerateVectorsWithModel(texts []string) []EmbeddingResult {
	out := make([]EmbeddingResult, len(texts))
	for i, t := range texts {
		out[i] = EmbeddingResult{Vector: f.GenerateVector(t), Model: f.ModelName()}
	}
	return out
}

func (f *fakeEmbedder) GenerateVectorNoRetry(text string) []float32 {
	return f.GenerateVector(text)
}

func (f *fakeEmbedder) GenerateVectorNoRetryWithModel(text string) EmbeddingResult {
	return EmbeddingResult{Vector: f.GenerateVector(text), Model: f.ModelName()}
}

func (f *fakeEmbedder) Dim() int {
	return f.dim
}

func (f *fakeEmbedder) ModelName() string {
	return "fake-model"
}

// TestSearchHybridAcceptsEmbedderInterface guards the contract from #35:
// the indexer must depend on the Embedder interface, not the concrete
// *EmbeddingsGenerator, so callers can substitute behavior in tests.
func TestSearchHybridAcceptsEmbedderInterface(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-engine-iface-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	docPath := filepath.Join(tempDir, "test.md")
	dbClient.SaveDocument(&db.Document{
		Path:      docPath,
		Hash:      "ifacehash",
		UpdatedAt: time.Now(),
	})

	var ie Embedder = &fakeEmbedder{dim: 768}

	dbClient.SaveChunks([]*db.Chunk{
		{
			UUID:         "iface-uuid-1",
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      "interface driven search",
			Embedding:    ie.GenerateVector("interface driven search"),
			Hash:         "h1",
		},
	})

	res, err := SearchHybrid(dbClient, dbClient, ie, "interface", 5)
	if err != nil {
		t.Fatalf("SearchHybrid with interface embedder failed: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("expected at least one result")
	}
	if res[0].Chunk.UUID != "iface-uuid-1" {
		t.Errorf("expected iface-uuid-1, got %s", res[0].Chunk.UUID)
	}
}

// TestSearchHybridSemanticOnlyMatch is a regression test for issue #65.
// A chunk with high cosine similarity but zero keyword overlap must appear
// in SearchHybrid results even when BM25 has hits on other chunks.
func TestSearchHybridSemanticOnlyMatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-hybrid-semantic-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	docPath := filepath.Join(tempDir, "test.md")
	dbClient.SaveDocument(&db.Document{
		Path:      docPath,
		Hash:      "semhash",
		UpdatedAt: time.Now(),
	})

	embedder := &fakeEmbedder{dim: 768}

	// Chunk A: contains the query keyword "falcon"
	textA := "The swift falcon soars above the mountains"
	// Chunk B: no keyword overlap with "falcon" but semantically similar
	// (both are about birds/flight) — we'll make its embedding close to A's
	textB := "A bird flies high over peaks"

	embeddingA := embedder.GenerateVector(textA)
	embeddingB := embedder.GenerateVector(textB)

	// Make B's embedding artificially similar to A's so cosine sim is high
	// even though there's zero keyword overlap with "falcon"
	for i := range embeddingB {
		embeddingB[i] = embeddingA[i]*0.9 + embeddingB[i]*0.1
	}
	// Re-normalize
	var sumSquares float64
	for _, v := range embeddingB {
		sumSquares += float64(v * v)
	}
	norm := float32(math.Sqrt(sumSquares))
	for i := range embeddingB {
		embeddingB[i] /= norm
	}

	dbClient.SaveChunks([]*db.Chunk{
		{
			UUID:         "uuid-keyword",
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      textA,
			Embedding:    embeddingA,
			Hash:         "ha",
		},
		{
			UUID:         "uuid-semantic",
			DocumentPath: docPath,
			ChunkIndex:   1,
			Content:      textB,
			Embedding:    embeddingB,
			Hash:         "hb",
		},
	})

	// Search for "falcon" — BM25 will find chunk A but not chunk B.
	// The fix ensures chunk B still appears via full vector scan.
	res, err := SearchHybrid(dbClient, dbClient, embedder, "falcon", 10)
	if err != nil {
		t.Fatalf("SearchHybrid failed: %v", err)
	}

	if len(res) < 2 {
		t.Fatalf("expected at least 2 results (keyword + semantic-only match), got %d", len(res))
	}

	found := make(map[string]bool)
	for _, r := range res {
		found[r.Chunk.UUID] = true
	}
	if !found["uuid-keyword"] {
		t.Error("expected uuid-keyword (BM25 match) in results")
	}
	if !found["uuid-semantic"] {
		t.Error("expected uuid-semantic (semantic-only match) in results — vector search was likely still filtered by BM25 candidates")
	}
}

func TestHybridSearch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-engine-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	docPath := filepath.Join(tempDir, "test.md")
	dbClient.SaveDocument(&db.Document{
		Path:      docPath,
		Hash:      "hash123",
		UpdatedAt: time.Now(),
	})

	embedder := newTestEmbeddingsGenerator()

	// Embed some sample text
	text1 := "The swift azure falcon soars above the sleeping canine"
	text2 := "Database management system optimization strategies"

	dbClient.SaveChunks([]*db.Chunk{
		{
			UUID:         "uuid1",
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      text1,
			Embedding:    embedder.GenerateVector(text1),
			Hash:         "chash1",
		},
		{
			UUID:         "uuid2",
			DocumentPath: docPath,
			ChunkIndex:   1,
			Content:      text2,
			Embedding:    embedder.GenerateVector(text2),
			Hash:         "chash2",
		},
	})

	// Search for something related to text1
	res, err := SearchHybrid(dbClient, dbClient, embedder, "falcon soars", 2)
	if err != nil {
		t.Fatalf("SearchHybrid failed: %v", err)
	}

	if len(res) == 0 {
		t.Fatalf("expected results, got none")
	}

	if res[0].Chunk.UUID != "uuid1" {
		t.Errorf("expected primary result to be uuid1 (falcon text), got %s", res[0].Chunk.UUID)
	}

	// Verify rank fields are set
	if res[0].BM25Rank == 0 && res[0].VectorRank == 0 {
		t.Errorf("expected BM25Rank or VectorRank to be non-zero")
	}
}

type mixedSpaceStore struct {
	db.Store
	spaces map[string]int
}

func (m *mixedSpaceStore) DetectMixedEmbeddingSpaces() (map[string]int, error) {
	return m.spaces, nil
}

func (m *mixedSpaceStore) SearchVector(queryVec []float32, limit int) ([]*db.SearchResult, error) {
	return nil, nil
}

func (m *mixedSpaceStore) SearchVectorWithPath(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return m.SearchVector(queryVec, limit)
}

func (m *mixedSpaceStore) SearchBM25(query string, limit int) ([]*db.SearchResult, error) {
	return nil, nil
}

func (m *mixedSpaceStore) SearchBM25WithPath(query string, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return m.SearchBM25(query, limit)
}

func (m *mixedSpaceStore) Upsert(_ context.Context, _ []*db.Chunk) error { return nil }
func (m *mixedSpaceStore) Delete(_ context.Context, _ string) error     { return nil }
func (m *mixedSpaceStore) Search(_ context.Context, _ []float32, _ int) ([]*db.SearchResult, error) {
	return nil, nil
}
func (m *mixedSpaceStore) SearchWithPath(_ context.Context, _ []float32, _ string, _ int) ([]*db.SearchResult, error) {
	return nil, nil
}

func TestSearchHybrid_MixedSpaceReturnsError(t *testing.T) {
	store := &mixedSpaceStore{
		spaces: map[string]int{
			"768/model-a": 100,
			"384/model-b": 50,
		},
	}
	embedder := &fakeEmbedder{dim: 768}

	_, err := SearchHybrid(store, store, embedder, "test query", 5)
	if err == nil {
		t.Fatal("expected error for mixed embedding spaces, got nil")
	}
	if !strings.Contains(err.Error(), "mixed embedding spaces") {
		t.Errorf("expected error about mixed embedding spaces, got: %v", err)
	}
}

func TestSearchHybrid_EmptyDBNoError(t *testing.T) {
	store := &mixedSpaceStore{
		spaces: map[string]int{},
	}
	embedder := &fakeEmbedder{dim: 768}

	_, err := SearchHybrid(store, store, embedder, "test query", 5)
	if err != nil {
		t.Fatalf("expected no error for empty DB, got: %v", err)
	}
}

type fakeVectorStore struct {
	searchFn func(queryVec []float32, limit int) ([]*db.SearchResult, error)
}

func (f *fakeVectorStore) Upsert(_ context.Context, _ []*db.Chunk) error { return nil }
func (f *fakeVectorStore) Delete(_ context.Context, _ string) error     { return nil }
func (f *fakeVectorStore) Search(_ context.Context, queryVec []float32, limit int) ([]*db.SearchResult, error) {
	if f.searchFn != nil {
		return f.searchFn(queryVec, limit)
	}
	return nil, nil
}
func (f *fakeVectorStore) SearchWithPath(_ context.Context, queryVec []float32, _ string, limit int) ([]*db.SearchResult, error) {
	return f.Search(context.Background(), queryVec, limit)
}

func TestSearchHybridUsesVectorStore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-hybrid-vs-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	called := false
	vs := &fakeVectorStore{
		searchFn: func(queryVec []float32, limit int) ([]*db.SearchResult, error) {
			called = true
			return nil, nil
		},
	}

	_, err = SearchHybrid(dbClient, vs, embedder, "test query", 5)
	if err != nil {
		t.Fatalf("SearchHybrid failed: %v", err)
	}
	if !called {
		t.Error("expected VectorStore.Search to be called")
	}
}

// TestSearchHybridWithPathFilter is a regression test for issue #254. A
// non-empty PathFilter must be passed to both BM25 and vector search legs.
func TestSearchHybridWithPathFilter(t *testing.T) {
	embedder := &fakeEmbedder{dim: 768}

	var bm25Path, vectorPath string
	store := &pathFilterStore{
		bm25Fn: func(query, pathPrefix string, limit int) ([]*db.SearchResult, error) {
			bm25Path = pathPrefix
			return []*db.SearchResult{}, nil
		},
		vectorFn: func(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
			vectorPath = pathPrefix
			return []*db.SearchResult{}, nil
		},
	}
	vs := &pathFilterVectorStore{
		searchFn: func(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
			vectorPath = pathPrefix
			return []*db.SearchResult{}, nil
		},
	}

	_, err := SearchHybridWithOptions(store, vs, embedder, "test", 5, SearchOptions{PathFilter: "/home/user/docs/project-a/"})
	if err != nil {
		t.Fatalf("SearchHybridWithOptions failed: %v", err)
	}

	if bm25Path != "/home/user/docs/project-a/" {
		t.Errorf("expected BM25 path prefix %q, got %q", "/home/user/docs/project-a/", bm25Path)
	}
	if vectorPath != "/home/user/docs/project-a/" {
		t.Errorf("expected vector path prefix %q, got %q", "/home/user/docs/project-a/", vectorPath)
	}
}

type pathFilterStore struct {
	bm25Fn   func(query, pathPrefix string, limit int) ([]*db.SearchResult, error)
	vectorFn func(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error)
}

func (p *pathFilterStore) Close() error                                             { return nil }
func (p *pathFilterStore) SaveDocument(doc *db.Document) error                      { return nil }
func (p *pathFilterStore) DeleteDocument(path string) error                         { return nil }
func (p *pathFilterStore) GetDocument(path string) (*db.Document, error)            { return nil, nil }
func (p *pathFilterStore) ListDocuments() ([]*db.Document, error)                   { return nil, nil }
func (p *pathFilterStore) SaveChunks(chunks []*db.Chunk) error                      { return nil }
func (p *pathFilterStore) GetChunksForDocument(docPath string) ([]*db.Chunk, error)  { return nil, nil }
func (p *pathFilterStore) GetStats() (*db.Stats, error)                             { return &db.Stats{}, nil }
func (p *pathFilterStore) SearchBM25(query string, limit int) ([]*db.SearchResult, error) {
	return p.bm25Fn(query, "", limit)
}
func (p *pathFilterStore) SearchBM25WithPath(query string, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return p.bm25Fn(query, pathPrefix, limit)
}
func (p *pathFilterStore) SearchVector(queryVec []float32, limit int) ([]*db.SearchResult, error) {
	return p.vectorFn(queryVec, "", limit)
}
func (p *pathFilterStore) SearchVectorWithPath(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return p.vectorFn(queryVec, pathPrefix, limit)
}
func (p *pathFilterStore) DetectMixedEmbeddingSpaces() (map[string]int, error) { return map[string]int{}, nil }
func (p *pathFilterStore) SetFolderContext(path, text string) error             { return nil }
func (p *pathFilterStore) GetFolderContexts() ([]db.FolderContext, error)          { return nil, nil }
func (p *pathFilterStore) GetMatchingContext(path string) (*db.FolderContext, error) { return nil, nil }
func (p *pathFilterStore) SaveExtractions(extractions []*db.Extraction) error     { return nil }
func (p *pathFilterStore) DeleteExtractionsForDocument(docPath string) error      { return nil }
func (p *pathFilterStore) GetDocumentExtractions(docPath string) ([]*db.Extraction, error) { return nil, nil }
func (p *pathFilterStore) ListExtractions(class string, limit int) ([]*db.Extraction, error) { return nil, nil }
func (p *pathFilterStore) SearchExtractions(queryStr string, limit int) ([]*db.Extraction, error) { return nil, nil }

type pathFilterVectorStore struct {
	searchFn func(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error)
}

func (p *pathFilterVectorStore) Upsert(_ context.Context, _ []*db.Chunk) error { return nil }
func (p *pathFilterVectorStore) Delete(_ context.Context, _ string) error       { return nil }
func (p *pathFilterVectorStore) Search(_ context.Context, queryVec []float32, limit int) ([]*db.SearchResult, error) {
	return p.searchFn(queryVec, "", limit)
}
func (p *pathFilterVectorStore) SearchWithPath(_ context.Context, queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return p.searchFn(queryVec, pathPrefix, limit)
}

// fallbackQueryEmbedder behaves like fakeEmbedder for indexing but reports the
// local hash fallback for the query vector used in search.
type fallbackQueryEmbedder struct {
	fakeEmbedder
}

func (f *fallbackQueryEmbedder) GenerateVectorNoRetryWithModel(text string) EmbeddingResult {
	return EmbeddingResult{Vector: f.fakeEmbedder.GenerateVectorNoRetry(text), Model: localHashModelName}
}

// fallbackSpaceStore is a minimal in-memory store that reports a single
// Ollama-embedded space and returns the configured vector results.
type fallbackSpaceStore struct {
	db.Store
	spaces  map[string]int
	results []*db.SearchResult
}

func (f *fallbackSpaceStore) DetectMixedEmbeddingSpaces() (map[string]int, error) {
	return f.spaces, nil
}

func (f *fallbackSpaceStore) SearchBM25(query string, limit int) ([]*db.SearchResult, error) { return nil, nil }
func (f *fallbackSpaceStore) SearchBM25WithPath(query string, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return nil, nil
}
func (f *fallbackSpaceStore) SearchVector(queryVec []float32, limit int) ([]*db.SearchResult, error) {
	return f.results, nil
}
func (f *fallbackSpaceStore) SearchVectorWithPath(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	return f.SearchVector(queryVec, limit)
}
func (f *fallbackSpaceStore) Upsert(_ context.Context, _ []*db.Chunk) error { return nil }
func (f *fallbackSpaceStore) Delete(_ context.Context, _ string) error      { return nil }
func (f *fallbackSpaceStore) Search(_ context.Context, _ []float32, _ int) ([]*db.SearchResult, error) {
	return f.results, nil
}
func (f *fallbackSpaceStore) SearchWithPath(_ context.Context, _ []float32, _ string, _ int) ([]*db.SearchResult, error) {
	return f.results, nil
}

// TestSearchHybrid_FallbackQueryAgainstOllamaIndex_WarnsAndMarksVectorMode verifies
// that a query fallback against an Ollama-embedded index emits a stderr warning
// and sets VectorMode to "fallback" on every result (issue #270).
func TestSearchHybrid_FallbackQueryAgainstOllamaIndex_WarnsAndMarksVectorMode(t *testing.T) {
	store := &fallbackSpaceStore{
		spaces: map[string]int{"768/ollama-model": 1},
		results: []*db.SearchResult{
			{Chunk: &db.Chunk{UUID: "c1", DocumentPath: "doc.md", Content: "test content"}, CosineScore: 0.5},
		},
	}
	embedder := &fallbackQueryEmbedder{fakeEmbedder{dim: 768}}

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	results, err := SearchHybrid(store, store, embedder, "query", 5)

	w.Close()
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var buf strings.Builder
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("failed to read stderr: %v", copyErr)
	}
	stderr := buf.String()
	if !strings.Contains(stderr, "query embedding fell back") {
		t.Errorf("expected stderr warning, got %q", stderr)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	for _, res := range results {
		if res.VectorMode != "fallback" {
			t.Errorf("expected VectorMode=fallback, got %q", res.VectorMode)
		}
	}
}

// TestSearchHybrid_FallbackQueryAgainstFallbackIndex_NoWarning verifies that no
// warning is emitted when both the query and the index use the local hash
// fallback, because the spaces are consistent (issue #270).
func TestSearchHybrid_FallbackQueryAgainstFallbackIndex_NoWarning(t *testing.T) {
	store := &fallbackSpaceStore{
		spaces: map[string]int{"768/local-hash": 1},
		results: []*db.SearchResult{
			{Chunk: &db.Chunk{UUID: "c1", DocumentPath: "doc.md", Content: "test content"}, CosineScore: 0.5},
		},
	}
	embedder := &fallbackQueryEmbedder{fakeEmbedder{dim: 768}}

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	results, err := SearchHybrid(store, store, embedder, "query", 5)

	w.Close()
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var buf strings.Builder
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("failed to read stderr: %v", copyErr)
	}
	if strings.Contains(buf.String(), "query embedding fell back") {
		t.Errorf("unexpected stderr warning, got %q", buf.String())
	}
	if len(results) > 0 && results[0].VectorMode != "" {
		t.Errorf("expected empty VectorMode for consistent fallback space, got %q", results[0].VectorMode)
	}
}

// TestSearchHybrid_OllamaQueryAgainstOllamaIndex_NoWarning verifies the normal
// path: an Ollama query vector against an Ollama index produces no warning and
// leaves VectorMode empty.
func TestSearchHybrid_OllamaQueryAgainstOllamaIndex_NoWarning(t *testing.T) {
	store := &fallbackSpaceStore{
		spaces: map[string]int{"768/ollama-model": 1},
		results: []*db.SearchResult{
			{Chunk: &db.Chunk{UUID: "c1", DocumentPath: "doc.md", Content: "test content"}, CosineScore: 0.5},
		},
	}
	embedder := &fakeEmbedder{dim: 768}

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	results, err := SearchHybrid(store, store, embedder, "query", 5)

	w.Close()
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var buf strings.Builder
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("failed to read stderr: %v", copyErr)
	}
	if strings.Contains(buf.String(), "query embedding fell back") {
		t.Errorf("unexpected stderr warning, got %q", buf.String())
	}
	if len(results) > 0 && results[0].VectorMode != "" {
		t.Errorf("expected empty VectorMode for Ollama query+index, got %q", results[0].VectorMode)
	}
}
