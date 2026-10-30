package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The IVF fixture records real Go behaviour of the approximate vector layer:
// the Hamming shortlist plus bounded heap ranking on larger corpora, the IVF
// index built by the first unscoped scan of at least 128 chunks and reused by
// later searches (also across reopen), its incremental maintenance on writes,
// and the persisted index_storage snapshot with its generation. Every step
// carries the generation and the SHA-256 of the persisted snapshot so a port must
// reproduce the exact clustering, not only the ranked output.

type retrievalIVFFixture struct {
	SchemaVersion int                    `json:"schema_version"`
	Scenarios     []retrievalIVFScenario `json:"scenarios"`
}

type retrievalIVFScenario struct {
	Name  string             `json:"name"`
	Steps []retrievalIVFStep `json:"steps"`
}

type retrievalIVFStep struct {
	Op         string               `json:"op"`
	Path       string               `json:"path,omitempty"`
	Chunks     []*Chunk             `json:"chunks,omitempty"`
	Query      []float32            `json:"query,omitempty"`
	PathPrefix string               `json:"path_prefix,omitempty"`
	Limit      int                  `json:"limit,omitempty"`
	Results    []retrievalVectorHit `json:"results,omitempty"`
	State      retrievalIVFState    `json:"state"`
}

type retrievalIVFState struct {
	Generation int64 `json:"generation"`
	Chunks     int   `json:"chunks"`
	// IVFSHA256 and IVFBytes describe the persisted index_storage snapshot;
	// both are null when no snapshot is stored.
	IVFSHA256 *string `json:"ivf_sha256"`
	IVFBytes  *int    `json:"ivf_bytes"`
}

func TestRetrievalIVFFixture(t *testing.T) {
	want, err := makeRetrievalIVFFixture(t)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("../../../../testdata/port/retrieval/retrieval-ivf.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	// #nosec G304 -- the fixture path is fixed relative to this test package.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("Go retrieval IVF oracle changed; regenerate %s", path)
	}
}

type ivfRecorder struct {
	t        *testing.T
	path     string
	db       *DB
	steps    []retrievalIVFStep
	updated  time.Time
	failures []error
}

func (r *ivfRecorder) state() retrievalIVFState {
	var state retrievalIVFState
	if err := r.db.conn.QueryRow("SELECT value FROM index_meta WHERE key = 'generation'").Scan(&state.Generation); err != nil {
		r.t.Fatal(err)
	}
	if err := r.db.conn.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&state.Chunks); err != nil {
		r.t.Fatal(err)
	}
	var data []byte
	if err := r.db.conn.QueryRow("SELECT data FROM index_storage WHERE key = 'ivf'").Scan(&data); err == nil {
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		size := len(data)
		state.IVFSHA256 = &digest
		state.IVFBytes = &size
	}
	return state
}

func (r *ivfRecorder) record(step retrievalIVFStep) {
	step.State = r.state()
	r.steps = append(r.steps, step)
}

func (r *ivfRecorder) open() {
	database, err := OpenAt(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	r.db = database
	r.record(retrievalIVFStep{Op: "open"})
}

func (r *ivfRecorder) close() {
	r.record(retrievalIVFStep{Op: "close"})
	if err := r.db.Close(); err != nil {
		r.t.Fatal(err)
	}
	r.db = nil
}

func (r *ivfRecorder) save(path string, chunks []*Chunk) {
	if err := r.db.SaveDocument(&Document{Path: path, Hash: "doc-" + path, UpdatedAt: r.updated}); err != nil {
		r.t.Fatal(err)
	}
	if err := r.db.SaveChunks(chunks); err != nil {
		r.t.Fatal(err)
	}
	r.record(retrievalIVFStep{Op: "save", Path: path, Chunks: cloneChunks(chunks)})
}

func (r *ivfRecorder) remove(path string) {
	if err := r.db.DeleteDocument(path); err != nil {
		r.t.Fatal(err)
	}
	r.record(retrievalIVFStep{Op: "delete", Path: path})
}

// replace mirrors the engine's commitIndex for an existing document:
// DeleteDocument, SaveDocument, then SaveChunks.
func (r *ivfRecorder) replace(path string, chunks []*Chunk) {
	if err := r.db.DeleteDocument(path); err != nil {
		r.t.Fatal(err)
	}
	if err := r.db.SaveDocument(&Document{Path: path, Hash: "doc2-" + path, UpdatedAt: r.updated}); err != nil {
		r.t.Fatal(err)
	}
	if err := r.db.SaveChunks(chunks); err != nil {
		r.t.Fatal(err)
	}
	r.record(retrievalIVFStep{Op: "replace", Path: path, Chunks: cloneChunks(chunks)})
}

func (r *ivfRecorder) search(query []float32, prefix string, limit int) {
	var results []*SearchResult
	var err error
	if prefix == "" {
		results, err = r.db.SearchVector(query, limit)
	} else {
		results, err = r.db.SearchVectorWithPath(query, prefix, limit)
	}
	if err != nil {
		r.t.Fatal(err)
	}
	step := retrievalIVFStep{Op: "search", Query: query, PathPrefix: prefix, Limit: limit}
	for _, result := range results {
		step.Results = append(step.Results, retrievalVectorHit{
			VectorRank:  result.VectorRank,
			CosineScore: result.CosineScore,
			Chunk: retrievalVectorSearchRow{
				ID: result.Chunk.ID, UUID: result.Chunk.UUID, DocumentPath: result.Chunk.DocumentPath,
				ChunkIndex: result.Chunk.ChunkIndex, Content: result.Chunk.Content,
				Embedding: result.Chunk.Embedding, Hash: result.Chunk.Hash,
			},
		})
	}
	r.record(step)
}

// cloneChunks keeps the inputs as the test wrote them: SaveChunks fills in
// IDs and norms on the originals.
func cloneChunks(chunks []*Chunk) []*Chunk {
	out := make([]*Chunk, len(chunks))
	for i, chunk := range chunks {
		c := *chunk
		c.ID = 0
		c.Norm = 0
		out[i] = &c
	}
	return out
}

func ivfVector(rng *rand.Rand, dim int) []float32 {
	vector := make([]float32, dim)
	for i := range vector {
		vector[i] = float32(math.Round((rng.Float64()*2-1)*1000) / 1000)
	}
	return vector
}

func ivfChunks(rng *rand.Rand, area string, doc, count, dim int, shared []float32) []*Chunk {
	path := fmt.Sprintf("/vault/%s/doc-%02d.md", area, doc)
	chunks := make([]*Chunk, count)
	for i := range chunks {
		embedding := ivfVector(rng, dim)
		if shared != nil && i%3 == 0 {
			// Exact duplicates force score ties through the heap.
			embedding = append([]float32(nil), shared...)
		}
		chunks[i] = &Chunk{
			UUID: fmt.Sprintf("ivf-%s-%02d-%02d", area, doc, i), DocumentPath: path, ChunkIndex: i,
			Content: fmt.Sprintf("%s document %d chunk %d", area, doc, i), Embedding: embedding,
			Hash: fmt.Sprintf("hash-%s-%02d-%02d", area, doc, i), Dim: dim, Model: "fixture-model",
		}
	}
	return chunks
}

func makeRetrievalIVFFixture(t *testing.T) (*retrievalIVFFixture, error) {
	t.Helper()
	fixture := &retrievalIVFFixture{SchemaVersion: 1}
	for _, build := range []struct {
		name string
		run  func(*ivfRecorder)
	}{
		{"lifecycle", ivfLifecycle},
		{"small-corpus", ivfSmallCorpus},
		{"identical-vectors", ivfIdenticalVectors},
	} {
		recorder := &ivfRecorder{t: t, path: filepath.Join(t.TempDir(), "retrieval.db"), updated: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
		build.run(recorder)
		if recorder.db != nil {
			if err := recorder.db.Close(); err != nil {
				return nil, err
			}
		}
		fixture.Scenarios = append(fixture.Scenarios, retrievalIVFScenario{Name: build.name, Steps: recorder.steps})
	}
	return fixture, nil
}

func ivfLifecycle(r *ivfRecorder) {
	const dim = 8
	rng := rand.New(rand.NewSource(20261030))
	shared := ivfVector(rng, dim)
	r.open()
	for doc := 0; doc < 18; doc++ {
		r.save(fmt.Sprintf("/vault/area-a/doc-%02d.md", doc), ivfChunks(rng, "area-a", doc, 10, dim, shared))
	}
	for doc := 0; doc < 12; doc++ {
		r.save(fmt.Sprintf("/vault/area-b/doc-%02d.md", doc), ivfChunks(rng, "area-b", doc, 10, dim, nil))
	}
	queries := [][]float32{ivfVector(rng, dim), ivfVector(rng, dim), ivfVector(rng, dim), shared}
	// First unscoped search: exact scan with the Hamming shortlist, then the
	// IVF snapshot is built and persisted.
	r.search(queries[0], "", 10)
	// Later searches use the IVF candidates.
	r.search(queries[0], "", 10)
	r.search(queries[1], "", 3)
	r.search(queries[2], "", 50)
	r.search(queries[3], "", 12)
	r.search(queries[1], "/vault/area-a/", 5)
	r.search(queries[2], "/VAULT/AREA-B/doc-0", 7)
	r.search(make([]float32, dim), "", 5)
	r.search([]float32{1, 2, 3}, "", 5)
	r.search(queries[0], "/vault/nowhere/", 5)
	r.close()
	r.open()
	r.search(queries[0], "", 10)
	r.search(queries[3], "", 30)
	r.search(queries[1], "/vault/area-b/", 4)
	// Incremental adds, then enough churn to force a re-cluster.
	for doc := 30; doc < 36; doc++ {
		r.save(fmt.Sprintf("/vault/area-c/doc-%02d.md", doc), ivfChunks(rng, "area-c", doc, 10, dim, nil))
		r.search(queries[2], "", 10)
	}
	r.remove("/vault/area-a/doc-00.md")
	r.remove("/vault/area-a/doc-01.md")
	r.search(queries[1], "", 10)
	for doc := 2; doc < 9; doc++ {
		r.remove(fmt.Sprintf("/vault/area-a/doc-%02d.md", doc))
	}
	r.search(queries[0], "", 10)
	r.replace("/vault/area-b/doc-00.md", ivfChunks(rng, "area-b", 0, 10, dim, shared))
	r.search(queries[3], "", 10)
	r.close()
	// Reopen after writes: the stored snapshot is stale and is dropped.
	r.open()
	r.search(queries[0], "/vault/area-c/", 6)
	r.search(queries[2], "", 10)
	r.search(queries[2], "", 10)
	r.close()
}

func ivfSmallCorpus(r *ivfRecorder) {
	const dim = 4
	rng := rand.New(rand.NewSource(7))
	r.open()
	for doc := 0; doc < 6; doc++ {
		r.save(fmt.Sprintf("/vault/small/doc-%02d.md", doc), ivfChunks(rng, "small", doc, 10, dim, nil))
	}
	q := ivfVector(rng, dim)
	r.search(q, "", 5)
	r.search(q, "", 5)
	r.search(q, "/vault/small/doc-0", 3)
	r.search(make([]float32, dim), "/vault/small/", 3)
	r.search(make([]float32, dim), "", 3)
	r.close()
}

func ivfIdenticalVectors(r *ivfRecorder) {
	const dim = 6
	rng := rand.New(rand.NewSource(99))
	same := ivfVector(rng, dim)
	r.open()
	for doc := 0; doc < 14; doc++ {
		chunks := make([]*Chunk, 10)
		for i := range chunks {
			embedding := append([]float32(nil), same...)
			if doc == 13 && i < 4 {
				embedding = ivfVector(rng, dim)
			}
			chunks[i] = &Chunk{
				UUID: fmt.Sprintf("same-%02d-%02d", doc, i), DocumentPath: fmt.Sprintf("/vault/same/doc-%02d.md", doc), ChunkIndex: i,
				Content: fmt.Sprintf("same %d %d", doc, i), Embedding: embedding,
				Hash: fmt.Sprintf("same-hash-%02d-%02d", doc, i), Dim: dim, Model: "fixture-model",
			}
		}
		r.save(fmt.Sprintf("/vault/same/doc-%02d.md", doc), chunks)
	}
	q := ivfVector(rng, dim)
	r.search(q, "", 8)
	r.search(q, "", 8)
	r.search(same, "", 20)
	r.close()
	r.open()
	r.search(q, "", 8)
	r.search(same, "/vault/same/doc-1", 6)
	r.close()
}
