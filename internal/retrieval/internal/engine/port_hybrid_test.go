package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

var hybridOracleCommit = func() string { return fixtureoracle.Current().Commit }

type hybridFixture struct {
	SchemaVersion  int                 `json:"schema_version"`
	OracleCommit   string              `json:"oracle_commit"`
	SourceHashes   map[string]string   `json:"source_hashes"`
	Normalizations []string            `json:"normalizations"`
	Cases          []hybridFixtureCase `json:"cases"`
}

type hybridFixtureCase struct {
	ID         string         `json:"id"`
	Query      string         `json:"query"`
	Embedding  []float32      `json:"embedding"`
	QueryModel string         `json:"query_model"`
	PathPrefix string         `json:"path_prefix"`
	Limit      int            `json:"limit"`
	Corpus     string         `json:"corpus"`
	Chunks     []hybridChunk  `json:"chunks"`
	Failure    string         `json:"failure,omitempty"`
	Results    []hybridResult `json:"results"`
	Warnings   []string       `json:"warnings"`
	ErrorClass string         `json:"error_class,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type hybridChunk struct {
	UUID      string    `json:"uuid"`
	Path      string    `json:"document_path"`
	Index     int       `json:"chunk_index"`
	Content   string    `json:"content"`
	Embedding []float32 `json:"embedding"`
	Hash      string    `json:"hash"`
	Dim       int       `json:"dim"`
	Model     string    `json:"embedding_model"`
}

type hybridResult struct {
	ID              int64    `json:"id"`
	UUID            string   `json:"uuid"`
	Path            string   `json:"document_path"`
	ChunkIndex      int      `json:"chunk_index"`
	Content         string   `json:"content"`
	Hash            string   `json:"hash"`
	BM25Rank        int      `json:"bm25_rank"`
	VectorRank      int      `json:"vector_rank"`
	RRFScore        float32  `json:"rrf_score"`
	CosineScore     float32  `json:"cosine_score"`
	MetadataMatches []string `json:"metadata_matches,omitempty"`
	VectorMode      string   `json:"vector_mode,omitempty"`
}

type hybridEmbedder struct {
	vector []float32
	model  string
}

func (e hybridEmbedder) GenerateVector(string) []float32 { return e.vector }
func (e hybridEmbedder) GenerateVectors(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = e.vector
	}
	return out
}
func (e hybridEmbedder) GenerateVectorsWithModel(texts []string) []EmbeddingResult {
	out := make([]EmbeddingResult, len(texts))
	for i := range out {
		out[i] = EmbeddingResult{Vector: e.vector, Model: e.model}
	}
	return out
}
func (e hybridEmbedder) GenerateVectorNoRetry(string) []float32 { return e.vector }
func (e hybridEmbedder) GenerateVectorNoRetryWithModel(string) EmbeddingResult {
	return EmbeddingResult{Vector: e.vector, Model: e.model}
}
func (e hybridEmbedder) Dim() int          { return len(e.vector) }
func (e hybridEmbedder) ModelName() string { return e.model }

type hybridBM25Failure struct{ db.Store }

func (s hybridBM25Failure) SearchBM25(string, int) ([]*db.SearchResult, error) {
	return nil, errors.New("fixture BM25 failure")
}
func (s hybridBM25Failure) SearchBM25WithPath(string, string, int) ([]*db.SearchResult, error) {
	return nil, errors.New("fixture BM25 failure")
}

type hybridVectorFailure struct{ db.VectorStore }

func (s hybridVectorFailure) Search(context.Context, []float32, int) ([]*db.SearchResult, error) {
	return nil, errors.New("fixture vector failure")
}
func (s hybridVectorFailure) SearchWithPath(context.Context, []float32, string, int) ([]*db.SearchResult, error) {
	return nil, errors.New("fixture vector failure")
}

func TestRetrievalHybridFixture(t *testing.T) {
	root := hybridRepoRoot(t)
	fixture := hybridFixture{
		SchemaVersion: 1,
		OracleCommit:  hybridOracleCommit(),
		SourceHashes:  hybridSourceHashes(t, root),
		Normalizations: []string{
			"results with equal final float32 rrf_score are compared by uuid only within the contiguous tie group because Go merges a map and does not define that order",
			"BM25 failure warning compares the stable warning class and fallback behavior; database error detail differs by backend",
			"mixed embedding error example pairs are sorted by space/count because Go ranges its space map in nondeterministic order",
		},
		Cases: hybridCases(),
	}
	if err := hybridVerifyPinnedSources(root, fixture.SourceHashes); err != nil {
		t.Fatal(err)
	}
	for i := range fixture.Cases {
		fixture.Cases[i].Results = []hybridResult{}
		fixture.Cases[i].Warnings = []string{}
	}

	if os.Getenv("PORT_GENERATE") == "1" {
		for i := range fixture.Cases {
			fixture.Cases[i] = hybridObserveCase(t, fixture.Cases[i])
		}
		writeHybridFixture(t, root, fixture)
		return
	}

	path := filepath.Join(root, "testdata/port/retrieval/hybrid.json")
	data, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture
	if err != nil {
		t.Fatalf("read Go-owned hybrid fixture (generate with PORT_GENERATE=1): %v", err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if fixture.SchemaVersion != 1 || fixture.OracleCommit != hybridOracleCommit() {
		t.Fatalf("unexpected fixture schema/oracle %d/%q", fixture.SchemaVersion, fixture.OracleCommit)
	}
	if !reflect.DeepEqual(fixture.SourceHashes, hybridSourceHashes(t, root)) {
		t.Fatal("hybrid production source hashes drifted from the frozen Go oracle")
	}
	if err := hybridVerifyPinnedSources(root, fixture.SourceHashes); err != nil {
		t.Fatal(err)
	}
	wantCases := hybridCases()
	if len(fixture.Cases) != len(wantCases) {
		t.Fatalf("fixture has %d cases; generator defines %d", len(fixture.Cases), len(wantCases))
	}
	for i, input := range wantCases {
		got := fixture.Cases[i]
		if got.ID != input.ID || got.Query != input.Query || !reflect.DeepEqual(got.Embedding, input.Embedding) || got.QueryModel != input.QueryModel || got.PathPrefix != input.PathPrefix || got.Limit != input.Limit || got.Corpus != input.Corpus || got.Failure != input.Failure {
			t.Fatalf("fixture input %d (%s) differs from Go oracle inputs", i, input.ID)
		}
		observed := hybridObserveCase(t, input)
		if !reflect.DeepEqual(observed.Results, got.Results) || !reflect.DeepEqual(observed.Warnings, got.Warnings) || observed.ErrorClass != got.ErrorClass || observed.Error != got.Error {
			t.Fatalf("Go hybrid oracle drift in %s\n got: %#v\nwant: %#v", input.ID, observed, got)
		}
	}
}

func hybridCases() []hybridFixtureCase {
	return []hybridFixtureCase{
		{ID: "empty-query", Query: "", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 5, Corpus: "base"},
		{ID: "keyword-leg", Query: "orchid", Embedding: []float32{0, 1}, QueryModel: "fixture-model", Limit: 10, Corpus: "base"},
		{ID: "semantic-leg", Query: "unmatched term", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 10, Corpus: "base"},
		{ID: "overlap-and-metadata", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 10, Corpus: "base"},
		{ID: "equal-rrf-tie", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 5, Corpus: "tie"},
		{ID: "limit-and-path", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: "fixture-model", PathPrefix: "/vault/project/", Limit: 1, Corpus: "base"},
		{ID: "mixed-space-rejected", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 5, Corpus: "mixed"},
		{ID: "local-hash-fallback-warning", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: localHashModelName, Limit: 5, Corpus: "remote"},
		{ID: "bm25-degrades-to-vector", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 5, Corpus: "base", Failure: "bm25"},
		{ID: "vector-error-propagates", Query: "orchid", Embedding: []float32{1, 0}, QueryModel: "fixture-model", Limit: 5, Corpus: "base", Failure: "vector"},
	}
}

func hybridCorpus(name string) []hybridChunk {
	base := []hybridChunk{
		{UUID: "hybrid-overlap", Path: "/vault/project/overlap.md", Content: "orchid manual for careful gardeners", Embedding: []float32{1, 0}, Hash: "hash-overlap", Dim: 2, Model: "fixture-model"},
		{UUID: "hybrid-keyword", Path: "/vault/project/keyword.md", Content: "orchid grows well in shade", Embedding: []float32{0, 1}, Hash: "hash-keyword", Dim: 2, Model: "fixture-model"},
		{UUID: "hybrid-semantic", Path: "/vault/project/semantic.md", Content: "a plant guide for garden care", Embedding: []float32{0.8, 0.6}, Hash: "hash-semantic", Dim: 2, Model: "fixture-model"},
		{UUID: "hybrid-metadata", Path: "/vault/project/metadata.md", Content: "general notes\n__SYMDESK_SEARCH_METADATA_START__\ntitle: orchid field notes\nsource: garden archive\n__SYMDESK_SEARCH_METADATA_END__", Embedding: []float32{0.2, 0.98}, Hash: "hash-metadata", Dim: 2, Model: "fixture-model"},
		{UUID: "hybrid-outside", Path: "/vault/other/outside.md", Content: "orchid outside project", Embedding: []float32{-1, 0}, Hash: "hash-outside", Dim: 2, Model: "fixture-model"},
	}
	switch name {
	case "tie":
		return []hybridChunk{
			{UUID: "tie-a", Path: "/vault/tie/a.md", Content: "orchid orchid orchid", Embedding: []float32{0.8, 0.6}, Hash: "hash-tie-a", Dim: 2, Model: "fixture-model"},
			{UUID: "tie-b", Path: "/vault/tie/b.md", Content: "orchid", Embedding: []float32{1, 0}, Hash: "hash-tie-b", Dim: 2, Model: "fixture-model"},
		}
	case "mixed":
		mixed := append([]hybridChunk(nil), base[:2]...)
		mixed[1].Model = "other-model"
		return mixed
	case "remote":
		remote := append([]hybridChunk(nil), base[:2]...)
		for i := range remote {
			remote[i].Model = "remote-model"
		}
		return remote
	default:
		return base
	}
}

func hybridObserveCase(t *testing.T, input hybridFixtureCase) hybridFixtureCase {
	t.Helper()
	input.Results, input.Warnings, input.Error, input.ErrorClass = []hybridResult{}, []string{}, "", ""
	input.Chunks = hybridCorpus(input.Corpus)
	for i := range input.Chunks {
		input.Chunks[i].Index = i
	}
	database, err := db.OpenAt(filepath.Join(t.TempDir(), "hybrid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	for _, chunk := range input.Chunks {
		if err := database.SaveDocument(&db.Document{Path: chunk.Path, Hash: "doc-" + chunk.Hash, UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
		if err := database.SaveChunks([]*db.Chunk{{UUID: chunk.UUID, DocumentPath: chunk.Path, ChunkIndex: chunk.Index, Content: chunk.Content, Embedding: chunk.Embedding, Hash: chunk.Hash, Dim: chunk.Dim, Model: chunk.Model}}); err != nil {
			t.Fatal(err)
		}
	}
	var store db.Store = database
	var vectorStore db.VectorStore = database
	if input.Failure == "bm25" {
		store = hybridBM25Failure{Store: database}
	}
	if input.Failure == "vector" {
		vectorStore = hybridVectorFailure{VectorStore: database}
	}
	embedder := hybridEmbedder{vector: input.Embedding, model: input.QueryModel}
	results, stderr, err := hybridCaptureStderr(func() ([]*db.SearchResult, error) {
		return SearchHybridWithOptions(store, vectorStore, embedder, input.Query, input.Limit, SearchOptions{PathFilter: input.PathPrefix})
	})
	input.Warnings = hybridLines(stderr)
	if err != nil {
		input.ErrorClass = "vector_failure"
		if strings.Contains(err.Error(), "mixed embedding spaces") {
			input.ErrorClass = "mixed_embedding_spaces"
		}
		input.Error = hybridNormalizeMixedError(err.Error())
		return input
	}
	for _, result := range results {
		matches := result.MetadataMatches
		input.Results = append(input.Results, hybridResult{
			ID: result.Chunk.ID, UUID: result.Chunk.UUID, Path: result.Chunk.DocumentPath, ChunkIndex: result.Chunk.ChunkIndex,
			Content: result.Chunk.Content, Hash: result.Chunk.Hash, BM25Rank: result.BM25Rank,
			VectorRank: result.VectorRank, RRFScore: result.RRFScore, CosineScore: result.CosineScore,
			MetadataMatches: matches, VectorMode: result.VectorMode,
		})
	}
	// Go builds `combined` by ranging over a map; equal RRF groups have no
	// semantic order. Canonicalize only those groups for fixture stability.
	for start := 0; start < len(input.Results); {
		end := start + 1
		for end < len(input.Results) && input.Results[end].RRFScore == input.Results[start].RRFScore {
			end++
		}
		sort.Slice(input.Results[start:end], func(i, j int) bool { return input.Results[start+i].UUID < input.Results[start+j].UUID })
		start = end
	}
	return input
}

func hybridCaptureStderr(run func() ([]*db.SearchResult, error)) ([]*db.SearchResult, string, error) {
	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, "", err
	}
	os.Stderr = writer
	results, runErr := run()
	_ = writer.Close()
	os.Stderr = old
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil && runErr == nil {
		runErr = readErr
	}
	return results, string(output), runErr
}

func hybridLines(output string) []string {
	if output == "" {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(output, "\n"), "\n")
}

func hybridNormalizeMixedError(message string) string {
	const prefix = "index contains mixed embedding spaces ("
	const suffix = "); re-index with a single model before searching"
	body, ok := strings.CutPrefix(message, prefix)
	if !ok {
		return message
	}
	body, ok = strings.CutSuffix(body, suffix)
	if !ok {
		return message
	}
	// Each map-derived pair has its own parenthesized chunk count. The
	// envelope, rather than the first closing parenthesis, bounds the list.
	pairs := strings.Split(body, ", ")
	sort.Strings(pairs)
	return prefix + strings.Join(pairs, ", ") + suffix
}

func TestHybridMixedErrorNormalization(t *testing.T) {
	const ordered = "index contains mixed embedding spaces (2/fixture-model (1 chunks), 2/other-model (1 chunks)); re-index with a single model before searching"
	const reversed = "index contains mixed embedding spaces (2/other-model (1 chunks), 2/fixture-model (1 chunks)); re-index with a single model before searching"
	if got := hybridNormalizeMixedError(reversed); got != ordered {
		t.Fatalf("nested count parentheses must not truncate the sortable list: %s", got)
	}
	const unrelated = "vector failure (second, first)"
	if got := hybridNormalizeMixedError(unrelated); got != unrelated {
		t.Fatalf("unrelated diagnostics must stay unchanged: %s", got)
	}
}

func hybridRepoRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source location unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "../../../.."))
}

var hybridSourcePaths = []string{
	"internal/retrieval/internal/engine/retrieval.go",
	"internal/retrieval/internal/engine/metadata.go",
	"internal/retrieval/internal/engine/embeddings.go",
	"internal/retrieval/internal/db/db.go",
	"internal/retrieval/internal/db/search.go",
	"internal/retrieval/internal/db/search_quantized.go",
	"internal/retrieval/internal/db/vectorstore.go",
	"internal/retrieval/internal/db/vecmath.go",
	"internal/searchquery/german.go",
	"internal/retrieval/internal/db/migrations/0001_baseline.sql",
	"internal/retrieval/internal/db/migrations/0002_meta.sql",
	"internal/retrieval/internal/db/migrations/0003_index_storage.sql",
	"internal/retrieval/internal/db/migrations/0004_binary_signature.sql",
	"internal/retrieval/internal/db/migrations/0005_quantized_sidecar.sql",
	"internal/retrieval/internal/db/migrations/0006_folder_contexts.sql",
	"internal/retrieval/internal/db/migrations/0007_backfill_embedding_dim.sql",
	"internal/retrieval/internal/db/migrations/0008_chunk_spans.sql",
	"internal/retrieval/internal/db/migrations/0009_extractions.sql",
	"internal/retrieval/internal/db/migrations/0010_embedding_pending.sql",
	"internal/retrieval/internal/db/migrations/0011_location_anchors.sql",
	"internal/retrieval/internal/db/migrations/0012_german_norm.sql",
	"internal/retrieval/internal/db/migrations/0013_german_trigram.sql",
}

func hybridSourceHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(hybridSourcePaths))
	for _, rel := range hybridSourcePaths {
		data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed repository source list
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[rel] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func hybridVerifyPinnedSources(root string, hashes map[string]string) error {
	for _, rel := range hybridSourcePaths {
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("Go oracle source path is not local: %s", rel)
		}
		//nolint:gosec // fixed Git subcommand, pinned commit, and allowlisted test sources.
		cmd := exec.Command("git", "show", hybridOracleCommit()+":"+filepath.ToSlash(rel))
		cmd.Dir = root
		data, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("read pinned Go source %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != hashes[rel] {
			return fmt.Errorf("Go oracle source %s differs from %s: pinned %s, current %s", rel, hybridOracleCommit(), got, hashes[rel])
		}
	}
	return nil
}

func writeHybridFixture(t *testing.T, root string, fixture hybridFixture) {
	t.Helper()
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "testdata/port/retrieval/hybrid.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
