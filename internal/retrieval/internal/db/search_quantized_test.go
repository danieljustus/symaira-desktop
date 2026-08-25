package db

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
)

func newTestCodec(dim int, bitWidth int, seed int) (*turboquant.Codec, error) {
	return turboquant.NewCodec(dim, turboquant.BitWidth(bitWidth), seed, 0)
}

func backfillSidecarsForTest(t *testing.T, d *DB, chunks []*Chunk, bitWidth int, seed int) {
	t.Helper()
	codec, err := newTestCodec(768, bitWidth, seed)
	if err != nil {
		t.Fatalf("newTestCodec: %v", err)
	}
	for _, c := range chunks {
		emb := c.Embedding
		if emb == nil {
			emb = make([]float32, 768)
			emb[0] = 1.0
		}
		var norm float32
		for _, v := range emb {
			norm += v * v
		}
		norm = float32(math.Sqrt(float64(norm)))

		blob, meta, err := codec.EncodeSidecar(emb, norm)
		if err != nil {
			t.Fatalf("EncodeSidecar chunk %d: %v", c.ID, err)
		}
		qcMeta := &QuantSidecarMeta{
			CodecVersion:   meta.CodecVersion,
			Dimension:      meta.Dimension,
			BitWidth:       meta.BitWidth,
			QuantizerMode:  meta.QuantizerMode,
			ProjectionSeed: meta.ProjectionSeed,
			Norm:           meta.Norm,
		}
		if err := d.SaveQuantizedSidecar(c.ID, blob, qcMeta); err != nil {
			t.Fatalf("SaveQuantizedSidecar chunk %d: %v", c.ID, err)
		}
	}
}

func insertTestChunks(t *testing.T, d *DB, n int) []*Chunk {
	t.Helper()
	docPath := "/test/docs.md"
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "test", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	chunks := make([]*Chunk, n)
	for i := 0; i < n; i++ {
		emb := make([]float32, 768)
		for j := range emb {
			emb[j] = float32(math.Sin(float64(i)*0.1 + float64(j)*0.05))
		}
		var sumSquares float64
		for _, v := range emb {
			sumSquares += float64(v * v)
		}
		norm := float32(math.Sqrt(sumSquares))
		if norm > 0 {
			for j := range emb {
				emb[j] /= norm
			}
		}
		chunks[i] = &Chunk{
			UUID:         "test-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "searchable content " + strconv.Itoa(i),
			Hash:         "test-hash-" + strconv.Itoa(i),
		}
		chunks[i].Embedding = emb
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}
	return chunks
}

func makeQueryVec() []float32 {
	q := make([]float32, 768)
	for i := range q {
		q[i] = float32(math.Sin(float64(25)*0.1 + float64(i)*0.05))
	}
	var sum float64
	for _, v := range q {
		sum += float64(v * v)
	}
	n := float32(math.Sqrt(sum))
	if n > 0 {
		for i := range q {
			q[i] /= n
		}
	}
	return q
}

func TestQuantizedSearch_DisabledByDefault(t *testing.T) {
	d := openTestDB(t)
	chunks := insertTestChunks(t, d, 10)
	backfillSidecarsForTest(t, d, chunks, 4, 42)

	if d.GetQuantConfig() != nil {
		t.Error("expected nil quant config by default")
	}

	queryVec := makeQueryVec()
	results, err := d.SearchVectorQuantized(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVectorQuantized: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from fallback search")
	}
}

func TestQuantizedSearch_EnabledReturnsResults(t *testing.T) {
	d := openTestDB(t)
	chunks := insertTestChunks(t, d, 50)
	backfillSidecarsForTest(t, d, chunks, 4, 42)

	d.SetQuantConfig(&QuantConfig{
		Enabled:     true,
		BitWidth:    4,
		Shortlist:   200,
		ExactRerank: true,
		Seed:        42,
	})

	queryVec := makeQueryVec()
	results, err := d.SearchVectorQuantized(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVectorQuantized: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from quantized search")
	}
	for i, r := range results {
		if r.VectorRank != i+1 {
			t.Errorf("result %d: VectorRank = %d, want %d", i, r.VectorRank, i+1)
		}
	}
}

func TestQuantizedSearch_FallbackWhenNoSidecars(t *testing.T) {
	d := openTestDB(t)
	insertTestChunks(t, d, 50)

	d.SetQuantConfig(&QuantConfig{
		Enabled:     true,
		BitWidth:    4,
		Shortlist:   200,
		ExactRerank: true,
		Seed:        42,
	})

	queryVec := makeQueryVec()
	results, err := d.SearchVectorQuantized(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVectorQuantized: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected fallback results when no sidecars exist")
	}
}

func TestQuantizedSearch_FallbackWhenDisabled(t *testing.T) {
	d := openTestDB(t)
	chunks := insertTestChunks(t, d, 50)
	backfillSidecarsForTest(t, d, chunks, 4, 42)

	d.SetQuantConfig(&QuantConfig{
		Enabled:     false,
		BitWidth:    4,
		Shortlist:   200,
		ExactRerank: true,
		Seed:        42,
	})

	queryVec := makeQueryVec()
	results, err := d.SearchVectorQuantized(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVectorQuantized: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected fallback results when disabled")
	}
}

func TestQuantizedSearch_ExactRerankFalse(t *testing.T) {
	d := openTestDB(t)
	chunks := insertTestChunks(t, d, 50)
	backfillSidecarsForTest(t, d, chunks, 4, 42)

	d.SetQuantConfig(&QuantConfig{
		Enabled:     true,
		BitWidth:    4,
		Shortlist:   200,
		ExactRerank: false,
		Seed:        42,
	})

	queryVec := makeQueryVec()
	results, err := d.SearchVectorQuantized(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVectorQuantized: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from quantized search without rerank")
	}
}

func TestQuantizedSearch_MixedSidecarAndNoSidecar(t *testing.T) {
	d := openTestDB(t)
	chunks := insertTestChunks(t, d, 50)

	for i := 0; i < 25; i++ {
		emb := chunks[i].Embedding
		var norm float32
		for _, v := range emb {
			norm += v * v
		}
		norm = float32(math.Sqrt(float64(norm)))

		codec, err := newTestCodec(768, 4, 42)
		if err != nil {
			t.Fatalf("newTestCodec: %v", err)
		}
		blob, meta, err := codec.EncodeSidecar(emb, norm)
		if err != nil {
			t.Fatalf("EncodeSidecar: %v", err)
		}
		qcMeta := &QuantSidecarMeta{
			CodecVersion:   meta.CodecVersion,
			Dimension:      meta.Dimension,
			BitWidth:       meta.BitWidth,
			QuantizerMode:  meta.QuantizerMode,
			ProjectionSeed: meta.ProjectionSeed,
			Norm:           meta.Norm,
		}
		if err := d.SaveQuantizedSidecar(chunks[i].ID, blob, qcMeta); err != nil {
			t.Fatalf("SaveQuantizedSidecar: %v", err)
		}
	}

	d.SetQuantConfig(&QuantConfig{
		Enabled:     true,
		BitWidth:    4,
		Shortlist:   200,
		ExactRerank: true,
		Seed:        42,
	})

	queryVec := makeQueryVec()
	results, err := d.SearchVectorQuantized(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVectorQuantized: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from mixed sidecar/no-sidecar dataset")
	}
}

func TestQuantizedSearch_VectorStoreInterface(t *testing.T) {
	d := openTestDB(t)
	chunks := insertTestChunks(t, d, 50)
	backfillSidecarsForTest(t, d, chunks, 4, 42)

	d.SetQuantConfig(&QuantConfig{
		Enabled:     true,
		BitWidth:    4,
		Shortlist:   200,
		ExactRerank: true,
		Seed:        42,
	})

	queryVec := makeQueryVec()
	results, err := d.Search(context.TODO(), queryVec, 5)
	if err != nil {
		t.Fatalf("VectorStore.Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from VectorStore.Search with quantization")
	}
}

func TestSetQuantConfig_Defaults(t *testing.T) {
	d := openTestDB(t)

	d.SetQuantConfig(&QuantConfig{Enabled: true})
	cfg := d.GetQuantConfig()
	if cfg.BitWidth != 4 {
		t.Errorf("expected default BitWidth=4, got %d", cfg.BitWidth)
	}
	if cfg.Shortlist != 200 {
		t.Errorf("expected default Shortlist=200, got %d", cfg.Shortlist)
	}
}

func TestSetQuantConfig_NilDisables(t *testing.T) {
	d := openTestDB(t)
	d.SetQuantConfig(&QuantConfig{Enabled: true})
	d.SetQuantConfig(nil)
	if d.GetQuantConfig() != nil {
		t.Error("expected nil after setting nil config")
	}
}

// insertTestChunksForPaths saves documents and chunks under the given paths
// using the same deterministic embedding generation as insertTestChunks.
func insertTestChunksForPaths(t *testing.T, d *DB, paths []string, nPerPath int) []*Chunk {
	t.Helper()
	now := time.Now()
	for _, p := range paths {
		if err := d.SaveDocument(&Document{Path: p, Hash: "test-hash-" + p, UpdatedAt: now}); err != nil {
			t.Fatalf("SaveDocument(%q): %v", p, err)
		}
	}
	var chunks []*Chunk
	counter := 0
	for _, p := range paths {
		for i := 0; i < nPerPath; i++ {
			emb := make([]float32, 768)
			for j := range emb {
				emb[j] = float32(math.Sin(float64(counter)*0.1 + float64(j)*0.05))
			}
			var sumSquares float64
			for _, v := range emb {
				sumSquares += float64(v * v)
			}
			norm := float32(math.Sqrt(sumSquares))
			if norm > 0 {
				for j := range emb {
					emb[j] /= norm
				}
			}
			chunks = append(chunks, &Chunk{
				UUID:         "test-path-" + strconv.Itoa(counter),
				DocumentPath: p,
				ChunkIndex:   i,
				Content:      "searchable content " + strconv.Itoa(counter),
				Hash:         "test-hash-" + strconv.Itoa(counter),
				Embedding:    emb,
			})
			counter++
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}
	return chunks
}

// TestSearchWithPath_PathPrefixFiltering covers the VectorStore.SearchWithPath
// path-prefix filter (issue #316): only chunks whose document path starts with
// the prefix may be returned. It exercises the real database path through the
// VectorStore interface used by MCP search_documents(path_prefix) and the
// engine PathFilter, across the standard fallback and both quantized modes.
func TestSearchWithPath_PathPrefixFiltering(t *testing.T) {
	d := openTestDB(t)

	dirA := "/docs/project-a"
	dirB := "/docs/project-b"
	chunks := insertTestChunksForPaths(t, d, []string{
		dirA + "/readme.md",
		dirA + "/guide.md",
		dirB + "/readme.md",
	}, 1)
	backfillSidecarsForTest(t, d, chunks, 4, 42)

	queryVec := makeQueryVec()

	search := func(prefix string) []*SearchResult {
		t.Helper()
		results, err := d.SearchWithPath(context.TODO(), queryVec, prefix, 10)
		if err != nil {
			t.Fatalf("SearchWithPath(%q): %v", prefix, err)
		}
		return results
	}

	// Baseline: no filter returns chunks from both directories.
	if unfiltered := search(""); len(unfiltered) != 3 {
		t.Fatalf("expected 3 unfiltered results, got %d", len(unfiltered))
	}

	t.Run("fallback standard search", func(t *testing.T) {
		scoped := search(dirA + "/")
		if len(scoped) != 2 {
			t.Fatalf("expected 2 results under %q, got %d", dirA+"/", len(scoped))
		}
		for _, r := range scoped {
			if !strings.HasPrefix(r.Chunk.DocumentPath, dirA+"/") {
				t.Errorf("result path %q does not match prefix %q", r.Chunk.DocumentPath, dirA+"/")
			}
		}
		if empty := search("/nonexistent/"); len(empty) != 0 {
			t.Errorf("expected 0 results for nonexistent prefix, got %d", len(empty))
		}
	})

	t.Run("quantized with exact rerank", func(t *testing.T) {
		d.SetQuantConfig(&QuantConfig{Enabled: true, BitWidth: 4, Shortlist: 200, ExactRerank: true, Seed: 42})
		scoped := search(dirB + "/")
		if len(scoped) != 1 {
			t.Fatalf("expected 1 result under %q, got %d", dirB+"/", len(scoped))
		}
		if scoped[0].Chunk.DocumentPath != dirB+"/readme.md" {
			t.Errorf("expected path %q, got %q", dirB+"/readme.md", scoped[0].Chunk.DocumentPath)
		}
	})

	t.Run("quantized without rerank", func(t *testing.T) {
		d.SetQuantConfig(&QuantConfig{Enabled: true, BitWidth: 4, Shortlist: 200, ExactRerank: false, Seed: 42})
		scoped := search(dirA + "/")
		if len(scoped) != 2 {
			t.Fatalf("expected 2 results under %q, got %d", dirA+"/", len(scoped))
		}
		for _, r := range scoped {
			if !strings.HasPrefix(r.Chunk.DocumentPath, dirA+"/") {
				t.Errorf("result path %q does not match prefix %q", r.Chunk.DocumentPath, dirA+"/")
			}
		}
	})
}

// TestSearchResult_Structured covers the consumer-facing Structured JSON shape
// shared by CLI/MCP/HTTP output (issue #316): a populated result must map every
// field, and the nil-chunk/nil-receiver branches must return nil instead of
// panicking in production output paths.
func TestSearchResult_Structured(t *testing.T) {
	t.Run("non-nil chunk maps all fields", func(t *testing.T) {
		charStart, charEnd := 12, 34
		r := &SearchResult{
			Chunk: &Chunk{
				UUID:         "chunk-uuid-1",
				DocumentPath: "/docs/project-a/readme.md",
				CharStart:    &charStart,
				CharEnd:      &charEnd,
				Content:      "passage text",
			},
			RRFScore:   0.75,
			VectorMode: "quantized",
		}
		s := r.Structured()
		if s == nil {
			t.Fatal("expected non-nil StructuredSearchResult")
		}
		if s.Path != "/docs/project-a/readme.md" {
			t.Errorf("Path = %q, want %q", s.Path, "/docs/project-a/readme.md")
		}
		if s.ChunkID != "chunk-uuid-1" {
			t.Errorf("ChunkID = %q, want %q", s.ChunkID, "chunk-uuid-1")
		}
		if s.CharStart == nil || *s.CharStart != 12 {
			t.Errorf("CharStart = %v, want 12", s.CharStart)
		}
		if s.CharEnd == nil || *s.CharEnd != 34 {
			t.Errorf("CharEnd = %v, want 34", s.CharEnd)
		}
		if s.Score != 0.75 {
			t.Errorf("Score = %v, want 0.75", s.Score)
		}
		if s.Snippet != "passage text" {
			t.Errorf("Snippet = %q, want %q", s.Snippet, "passage text")
		}
		if s.VectorMode != "quantized" {
			t.Errorf("VectorMode = %q, want %q", s.VectorMode, "quantized")
		}

		// The consumer-facing JSON shape must expose the citation fields and
		// must not leak the full embedding vector.
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		for _, want := range []string{`"path"`, `"chunk_id"`, `"snippet"`, `"score"`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("marshaled JSON missing %s: %s", want, raw)
			}
		}
		if strings.Contains(string(raw), "embedding") {
			t.Errorf("marshaled JSON must not contain embedding: %s", raw)
		}
	})

	t.Run("nil chunk returns nil", func(t *testing.T) {
		r := &SearchResult{Chunk: nil, RRFScore: 0.25}
		if s := r.Structured(); s != nil {
			t.Errorf("expected nil for nil chunk, got %+v", s)
		}
	})

	t.Run("nil receiver returns nil", func(t *testing.T) {
		var r *SearchResult
		if s := r.Structured(); s != nil {
			t.Errorf("expected nil for nil receiver, got %+v", s)
		}
	})
}
