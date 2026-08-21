package bench

import (
	"bytes"
	"encoding/csv"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-seek/internal/engine"
)

// TestDeriveChunkID_Deterministic verifies that identical inputs produce the
// same chunk ID across calls, keeping bench runs comparable (issue #302).
func TestDeriveChunkID_Deterministic(t *testing.T) {
	a := deriveChunkID("doc-a", "hash-a", 0)
	b := deriveChunkID("doc-a", "hash-a", 0)
	if a != b {
		t.Errorf("expected deterministic chunk ID, got %q vs %q", a, b)
	}
}

// TestDeriveChunkID_DistinctInputs verifies that different documents, hashes,
// and offsets never collide.
func TestDeriveChunkID_DistinctInputs(t *testing.T) {
	seen := map[string]string{}
	inputs := [][3]any{
		{"doc-a", "hash-a", 0},
		{"doc-b", "hash-a", 0},
		{"doc-a", "hash-b", 0},
		{"doc-a", "hash-a", 1},
		{"doc-a", "hash-a", 10},
	}
	for _, in := range inputs {
		path := in[0].(string)
		hash := in[1].(string)
		start := in[2].(int)
		id := deriveChunkID(path, hash, start)
		if prev, ok := seen[id]; ok {
			t.Errorf("chunk ID collision: %q for both %q and %q", id, prev, in)
		}
		seen[id] = path
	}
	if len(seen) != len(inputs) {
		t.Errorf("expected %d unique IDs, got %d", len(inputs), len(seen))
	}
}

// TestDeriveChunkID_IsUUIDv5 verifies the output has UUID shape, matching the
// chunks.uuid column format used by the indexer.
func TestDeriveChunkID_IsUUIDv5(t *testing.T) {
	id := deriveChunkID("doc-a", "hash-a", 0)
	if len(id) != 36 {
		t.Fatalf("expected 36-char UUID, got %q (%d chars)", id, len(id))
	}
	// UUIDv5: version nibble is 5 in the third group.
	if id[14] != '5' {
		t.Errorf("expected UUIDv5 (version 5), got %q", id)
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Errorf("expected dashed UUID format, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// Harness driver tests (openTempDB / Run / RunAll / WriteCSV / ComputeAggregate)
// ---------------------------------------------------------------------------

// stubEmbedder is a deterministic, offline Embedder used to exercise the
// harness without touching Ollama or the network. Vectors are unit vectors
// seeded from the text hash so repeated runs are stable.
type stubEmbedder struct {
	dim int
}

func (s *stubEmbedder) vec(text string) []float32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	rng := rand.New(rand.NewSource(int64(h.Sum32())))
	v := make([]float32, s.dim)
	var sum float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		sum += x * x
	}
	if sum > 0 {
		inv := float32(1 / math.Sqrt(sum))
		for i := range v {
			v[i] *= inv
		}
	}
	return v
}

func (s *stubEmbedder) GenerateVector(text string) []float32 { return s.vec(text) }

func (s *stubEmbedder) GenerateVectorNoRetry(text string) []float32 { return s.vec(text) }

func (s *stubEmbedder) GenerateVectorNoRetryWithModel(text string) engine.EmbeddingResult {
	return engine.EmbeddingResult{Vector: s.vec(text), Model: s.ModelName()}
}

func (s *stubEmbedder) GenerateVectors(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = s.vec(t)
	}
	return out
}

func (s *stubEmbedder) GenerateVectorsWithModel(texts []string) []engine.EmbeddingResult {
	out := make([]engine.EmbeddingResult, len(texts))
	for i, t := range texts {
		out[i] = engine.EmbeddingResult{Vector: s.vec(t), Model: s.ModelName()}
	}
	return out
}

func (s *stubEmbedder) Dim() int { return s.dim }

func (s *stubEmbedder) ModelName() string { return "stub-model" }

// writeBenchCorpus creates a temporary corpus directory from a filename→content map.
func writeBenchCorpus(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRun_HybridProducesMetrics runs the full hybrid pipeline against a stub
// embedder and verifies the Result carries sane metrics.
func TestRun_HybridProducesMetrics(t *testing.T) {
	corpus := writeBenchCorpus(t, map[string]string{
		"alpha.txt": "The quick brown fox jumps over the lazy dog near the river bank.",
		"beta.txt":  "Quantum entanglement links particles across any distance instantly.",
	})

	res, err := Run(&stubEmbedder{dim: 8}, Config{
		Query:       "quick brown fox river",
		RelevantIDs: []string{"alpha"},
		CorpusDir:   corpus,
		SearchLimit: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("expected no search error, got %q", res.Error)
	}
	if len(res.RankedIDs) == 0 {
		t.Fatal("expected at least one ranked document")
	}
	// Both corpus documents fit within the limit, so the relevant document
	// must be ranked somewhere in the top 10 and MRR must be positive.
	if res.HitRate10 != 1.0 {
		t.Errorf("expected HitRate10=1.0 with 2-doc corpus, got %v", res.HitRate10)
	}
	if res.MRR <= 0 {
		t.Errorf("expected MRR > 0, got %v", res.MRR)
	}
	for name, v := range map[string]float64{
		"HitRate1": res.HitRate1, "HitRate3": res.HitRate3, "HitRate5": res.HitRate5,
		"HitRate10": res.HitRate10, "MRR": res.MRR, "NDCG10": res.NDCG10,
	} {
		if v < 0 || v > 1 {
			t.Errorf("%s out of range [0,1]: %v", name, v)
		}
	}
	if res.Latency < 0 {
		t.Errorf("expected non-negative latency, got %v", res.Latency)
	}
}

// TestRun_VectorOnlyMode exercises the SearchVectorOnlyMode branch of Run.
func TestRun_VectorOnlyMode(t *testing.T) {
	corpus := writeBenchCorpus(t, map[string]string{
		"alpha.txt": "The quick brown fox jumps over the lazy dog near the river bank.",
		"beta.txt":  "Quantum entanglement links particles across any distance instantly.",
	})

	res, err := Run(&stubEmbedder{dim: 8}, Config{
		Query:       "quick brown fox river",
		RelevantIDs: []string{"alpha"},
		CorpusDir:   corpus,
		SearchLimit: 10,
		Mode:        SearchVectorOnlyMode,
	})
	if err != nil {
		t.Fatalf("Run (vector-only): %v", err)
	}
	if res.Error != "" {
		t.Fatalf("expected no search error, got %q", res.Error)
	}
	if res.Latency < 0 {
		t.Errorf("expected non-negative latency, got %v", res.Latency)
	}
}

// TestRun_MissingCorpusDir covers the corpus-loading error path of Run.
func TestRun_MissingCorpusDir(t *testing.T) {
	_, err := Run(&stubEmbedder{dim: 8}, Config{
		Query:       "anything",
		RelevantIDs: []string{"alpha"},
		CorpusDir:   filepath.Join(t.TempDir(), "does-not-exist"),
		SearchLimit: 5,
	})
	if err == nil {
		t.Fatal("expected error for missing corpus directory")
	}
	if !strings.Contains(err.Error(), "loading corpus") {
		t.Errorf("expected corpus error, got %v", err)
	}
}

// TestRun_OpenTempDBError covers the openTempDB failure path: pointing TMPDIR
// at a regular file makes os.MkdirTemp fail before any database is opened.
func TestRun_OpenTempDBError(t *testing.T) {
	corpus := writeBenchCorpus(t, map[string]string{
		"alpha.txt": "The quick brown fox jumps over the lazy dog near the river bank.",
	})
	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notDir)

	_, err := Run(&stubEmbedder{dim: 8}, Config{
		Query:       "quick brown fox",
		RelevantIDs: []string{"alpha"},
		CorpusDir:   corpus,
		SearchLimit: 5,
	})
	if err == nil {
		t.Fatal("expected error when temp dir creation fails")
	}
	if !strings.Contains(err.Error(), "creating temp home") {
		t.Errorf("expected temp-home error, got %v", err)
	}
}

// TestRunAll_ProducesResults verifies RunAll returns one Result per judgment.
func TestRunAll_ProducesResults(t *testing.T) {
	corpus := writeBenchCorpus(t, map[string]string{
		"alpha.txt": "The quick brown fox jumps over the lazy dog near the river bank.",
		"beta.txt":  "Quantum entanglement links particles across any distance instantly.",
	})
	judgments := filepath.Join(t.TempDir(), "judgments.yaml")
	content := "queries:\n" +
		"  - query: \"quick brown fox river\"\n" +
		"    relevant_ids: [\"alpha\"]\n" +
		"  - query: \"quantum entanglement particles\"\n" +
		"    relevant_ids: [\"beta\"]\n"
	if err := os.WriteFile(judgments, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	results, err := RunAll(&stubEmbedder{dim: 8}, corpus, judgments, 10, engine.RerankConfig{}, engine.ExpandConfig{})
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("unexpected per-query error %q for %q", r.Error, r.Query)
		}
	}
}

// TestRunAll_BadJudgments covers the judgment-loading error path of RunAll.
func TestRunAll_BadJudgments(t *testing.T) {
	_, err := RunAll(&stubEmbedder{dim: 8}, t.TempDir(), filepath.Join(t.TempDir(), "missing.yaml"),
		10, engine.RerankConfig{}, engine.ExpandConfig{})
	if err == nil {
		t.Fatal("expected error for missing judgments file")
	}
	if !strings.Contains(err.Error(), "loading judgments") {
		t.Errorf("expected judgments error, got %v", err)
	}
}

// TestWriteCSV_HeaderAndRows verifies WriteCSV emits the expected header and
// one row per result, including CSV quoting for fields containing commas.
func TestWriteCSV_HeaderAndRows(t *testing.T) {
	results := []*Result{
		{
			Query: "quick brown fox", RankedIDs: []string{"alpha"},
			HitRate1: 1, HitRate3: 1, HitRate5: 1, HitRate10: 1,
			MRR: 1, NDCG10: 0.75, Latency: 1500 * time.Microsecond,
		},
		{
			Query: "query, with comma", RankedIDs: nil,
			HitRate1: 0, HitRate3: 0.5, HitRate5: 0.5, HitRate10: 0.5,
			MRR: 0.25, NDCG10: 0.5, Latency: 2 * time.Millisecond, Error: "boom",
		},
	}

	var buf bytes.Buffer
	if err := WriteCSV(&buf, results); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("parsing CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected header + 2 rows, got %d records", len(records))
	}
	wantHeader := []string{"query", "hit_rate_1", "hit_rate_3", "hit_rate_5", "hit_rate_10",
		"mrr", "ndcg_10", "latency_ms", "error"}
	if strings.Join(records[0], "|") != strings.Join(wantHeader, "|") {
		t.Errorf("unexpected header: %v", records[0])
	}
	wantRow1 := []string{"quick brown fox", "1.0000", "1.0000", "1.0000", "1.0000",
		"1.0000", "0.7500", "2", ""}
	if strings.Join(records[1], "|") != strings.Join(wantRow1, "|") {
		t.Errorf("unexpected row 1: %v", records[1])
	}
	wantRow2 := []string{"query, with comma", "0.0000", "0.5000", "0.5000", "0.5000",
		"0.2500", "0.5000", "2", "boom"}
	if strings.Join(records[2], "|") != strings.Join(wantRow2, "|") {
		t.Errorf("unexpected row 2: %v", records[2])
	}
}

// TestComputeAggregate_Means verifies ComputeAggregate averages the metrics
// across non-errored results only.
func TestComputeAggregate_Means(t *testing.T) {
	results := []*Result{
		{Query: "q1", HitRate1: 1, HitRate3: 1, HitRate5: 1, HitRate10: 1, MRR: 1, NDCG10: 1},
		{Query: "q2", HitRate1: 0, HitRate3: 0.5, HitRate5: 0.5, HitRate10: 0.5, MRR: 0.5, NDCG10: 0.5},
		{Query: "q3", Error: "boom", HitRate1: 99, HitRate3: 99, HitRate5: 99, HitRate10: 99, MRR: 99, NDCG10: 99},
	}

	agg := ComputeAggregate(results)
	if agg.Count != 2 {
		t.Fatalf("expected Count=2 (errored result excluded), got %d", agg.Count)
	}
	want := map[string]float64{
		"HitRate1": 0.5, "HitRate3": 0.75, "HitRate5": 0.75, "HitRate10": 0.75,
		"MRR": 0.75, "NDCG10": 0.75,
	}
	got := map[string]float64{
		"HitRate1": agg.HitRate1, "HitRate3": agg.HitRate3, "HitRate5": agg.HitRate5,
		"HitRate10": agg.HitRate10, "MRR": agg.MRR, "NDCG10": agg.NDCG10,
	}
	for name, w := range want {
		if math.Abs(got[name]-w) > 1e-9 {
			t.Errorf("%s: got %v, want %v", name, got[name], w)
		}
	}
}

// TestComputeAggregate_Empty verifies the zero aggregate for empty or
// fully-errored result sets.
func TestComputeAggregate_Empty(t *testing.T) {
	agg := ComputeAggregate(nil)
	if agg.Count != 0 || agg.HitRate1 != 0 || agg.MRR != 0 || agg.NDCG10 != 0 {
		t.Errorf("expected zero aggregate for empty input, got %+v", agg)
	}
	agg2 := ComputeAggregate([]*Result{{Query: "q", Error: "boom"}})
	if agg2.Count != 0 {
		t.Errorf("expected zero count when all results have errors, got %d", agg2.Count)
	}
}
