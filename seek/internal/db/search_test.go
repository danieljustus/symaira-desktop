package db

import (
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSignBinarySignature(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
	}{
		{"all positive", []float32{0.1, 0.5, 1.0, 0.3}},
		{"all negative", []float32{-0.1, -0.5, -1.0, -0.3}},
		{"mixed", []float32{-0.1, 0.5, -1.0, 0.3}},
		{"zeros", []float32{0, 0, 0, 0}},
		{"768-dim", make([]float32, 768)},
		{"empty", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := SignBinarySignature(tt.vec)
			if tt.vec == nil {
				if sig != nil {
					t.Errorf("expected nil for nil input, got %v", sig)
				}
				return
			}
			nWords := (len(tt.vec) + 63) / 64
			if len(sig) != nWords*8 {
				t.Errorf("expected %d bytes, got %d", nWords*8, len(sig))
			}
			for i, v := range tt.vec {
				word := i / 64
				bit := uint(i % 64)
				bits := uint64(sig[word*8]) | uint64(sig[word*8+1])<<8 |
					uint64(sig[word*8+2])<<16 | uint64(sig[word*8+3])<<24 |
					uint64(sig[word*8+4])<<32 | uint64(sig[word*8+5])<<40 |
					uint64(sig[word*8+6])<<48 | uint64(sig[word*8+7])<<56
				gotBit := (bits >> bit) & 1
				var wantBit uint64
				if v >= 0 {
					wantBit = 1
				}
				if gotBit != wantBit {
					t.Errorf("dim %d: expected bit %d, got %d (value=%v)", i, wantBit, gotBit, v)
				}
			}
		})
	}
}

func TestHammingDistance(t *testing.T) {
	tests := []struct {
		name string
		a, b []byte
		want int
	}{
		{"identical", []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, 0},
		{"all differ", []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, 64},
		{"one bit", []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, 1},
		{"two words differ 8 bits", []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, 8},
		{"two words differ 1 bit", []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, 1},
		{"length mismatch", []byte{0xFF}, []byte{0xFF, 0x00}, math.MaxInt},
		{"empty", nil, nil, math.MaxInt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HammingDistance(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("HammingDistance() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBinaryQuantizedTwoStageRecall(t *testing.T) {
	d := setupDB(t)

	docPath := filepath.Join(t.TempDir(), "bq-recall.md")
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "bq", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	const nChunks = 50
	chunks := make([]*Chunk, nChunks)
	for i := 0; i < nChunks; i++ {
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
			UUID:         "bq-" + strconv.Itoa(i),
			DocumentPath: docPath,
			ChunkIndex:   i,
			Content:      "content " + strconv.Itoa(i),
			Embedding:    emb,
			Hash:         "bq-hash-" + strconv.Itoa(i),
		}
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	queryVec := make([]float32, 768)
	copy(queryVec, chunks[25].Embedding)

	twoStageResults, err := d.SearchVector(queryVec, 5)
	if err != nil {
		t.Fatalf("SearchVector (two-stage): %v", err)
	}
	if len(twoStageResults) == 0 {
		t.Fatal("expected non-empty two-stage results")
	}

	cosineResults, err := d.searchVectorFullScanCosine(queryVec, l2Norm(queryVec), 5)
	if err != nil {
		t.Fatalf("searchVectorFullScanCosine: %v", err)
	}
	if len(cosineResults) == 0 {
		t.Fatal("expected non-empty cosine results")
	}

	if twoStageResults[0].Chunk.UUID != cosineResults[0].Chunk.UUID {
		t.Errorf("top result mismatch: two-stage=%s, cosine=%s",
			twoStageResults[0].Chunk.UUID, cosineResults[0].Chunk.UUID)
	}

	cosineSet := make(map[string]struct{}, len(cosineResults))
	for _, r := range cosineResults {
		cosineSet[r.Chunk.UUID] = struct{}{}
	}
	overlap := 0
	for _, r := range twoStageResults {
		if _, ok := cosineSet[r.Chunk.UUID]; ok {
			overlap++
		}
	}
	recall := float64(overlap) / float64(len(twoStageResults))
	t.Logf("two-stage recall vs cosine baseline: %d/%d (%.1f%%)", overlap, len(twoStageResults), recall*100)
	if recall < 0.95 {
		t.Errorf("recall %.1f%% is below 95%% threshold", recall*100)
	}
}

func BenchmarkSearchVectorBinaryQuantized(b *testing.B) {
	d := setupDB(b)
	const nChunks = 1000
	seedBenchmarkDB(b, d, nChunks)
	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := d.SearchVector(query, 10)
		if err != nil {
			b.Fatalf("SearchVector: %v", err)
		}
		if len(results) == 0 {
			b.Fatal("expected non-empty results")
		}
	}
}

func BenchmarkSearchVectorCosineBaseline(b *testing.B) {
	d := setupDB(b)
	const nChunks = 1000
	seedBenchmarkDB(b, d, nChunks)
	query := make([]float32, 768)
	for i := range query {
		query[i] = 0.5
	}
	qNorm := l2Norm(query)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := d.searchVectorFullScanCosine(query, qNorm, 10)
		if err != nil {
			b.Fatalf("searchVectorFullScanCosine: %v", err)
		}
		if len(results) == 0 {
			b.Fatal("expected non-empty results")
		}
	}
}

// TestSearchBM25WithPath is a regression test for issue #254. The BM25 leg must
// apply the path prefix in SQL so that results are restricted before the
// limit is applied.
func TestSearchBM25WithPath(t *testing.T) {
	d := setupDB(t)

	now := time.Now()
	docs := []string{
		"/home/user/docs/project-a/readme.md",
		"/home/user/docs/project-a/spec.md",
		"/home/user/docs/project-b/readme.md",
	}
	for _, p := range docs {
		if err := d.SaveDocument(&Document{Path: p, Hash: "h-" + p, UpdatedAt: now}); err != nil {
			t.Fatalf("SaveDocument: %v", err)
		}
	}

	chunks := []*Chunk{
		makeChunk("u-a-readme", docs[0], 0, "project alpha documentation", 10),
		makeChunk("u-a-spec", docs[1], 0, "project alpha specification", 20),
		makeChunk("u-b-readme", docs[2], 0, "project beta documentation", 30),
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	all, err := d.SearchBM25("documentation", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 documentation hits without filter, got %d", len(all))
	}

	scoped, err := d.SearchBM25WithPath("documentation", "/home/user/docs/project-a/", 10)
	if err != nil {
		t.Fatalf("SearchBM25WithPath: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("expected 1 scoped hit, got %d", len(scoped))
	}
	if scoped[0].Chunk.DocumentPath != docs[0] {
		t.Errorf("expected path %q, got %q", docs[0], scoped[0].Chunk.DocumentPath)
	}

	empty, err := d.SearchBM25WithPath("documentation", "/nonexistent/", 10)
	if err != nil {
		t.Fatalf("SearchBM25WithPath empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 hits for nonexistent prefix, got %d", len(empty))
	}
}

// TestSearchVectorWithPath is a regression test for issue #254. The vector leg
// must apply the path prefix before scoring so that the limit is applied to
// scoped results.
func TestSearchVectorWithPath(t *testing.T) {
	d := setupDB(t)

	now := time.Now()
	docs := []string{
		"/home/user/docs/project-a/readme.md",
		"/home/user/docs/project-b/readme.md",
	}
	for _, p := range docs {
		if err := d.SaveDocument(&Document{Path: p, Hash: "h-" + p, UpdatedAt: now}); err != nil {
			t.Fatalf("SaveDocument: %v", err)
		}
	}

	chunks := []*Chunk{
		makeChunk("u-a-readme", docs[0], 0, "project alpha", 10),
		makeChunk("u-b-readme", docs[1], 0, "project beta", 20),
	}
	if err := d.SaveChunks(chunks); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}

	queryVec := make([]float32, 768)
	for i := range queryVec {
		queryVec[i] = float32(10+i) / 1000.0
	}

	all, err := d.SearchVector(queryVec, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 vector hits without filter, got %d", len(all))
	}

	scoped, err := d.SearchVectorWithPath(queryVec, "/home/user/docs/project-a/", 10)
	if err != nil {
		t.Fatalf("SearchVectorWithPath: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("expected 1 scoped hit, got %d", len(scoped))
	}
	if scoped[0].Chunk.DocumentPath != docs[0] {
		t.Errorf("expected path %q, got %q", docs[0], scoped[0].Chunk.DocumentPath)
	}

	empty, err := d.SearchVectorWithPath(queryVec, "/nonexistent/", 10)
	if err != nil {
		t.Fatalf("SearchVectorWithPath empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 hits for nonexistent prefix, got %d", len(empty))
	}
}
