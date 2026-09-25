package db

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

const (
	retrievalStateOracleCommit  = "38891d35eb8ceb6c348eca9a78b3fb2873677e3d"
	retrievalStateOracleRelease = "post-v0.13.0-dependency-refresh"
)

type retrievalEmbeddingStateFixture struct {
	SchemaVersion     int                            `json:"schema_version"`
	Oracle            retrievalStateOracle           `json:"oracle"`
	Documents         []retrievalStateDocument       `json:"documents"`
	Chunks            []retrievalStateChunk          `json:"chunks"`
	LegacyNullUUIDs   []string                       `json:"legacy_null_uuids"`
	PendingTotal      int                            `json:"pending_total"`
	PendingByDocument []retrievalStatePendingCount   `json:"pending_by_document"`
	EmbeddingSpaces   []retrievalStateEmbeddingSpace `json:"embedding_spaces"`
}

type retrievalStateOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type retrievalStateDocument struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type retrievalStateChunk struct {
	UUID             string    `json:"uuid"`
	DocumentPath     string    `json:"document_path"`
	ChunkIndex       int       `json:"chunk_index"`
	Content          string    `json:"content"`
	Embedding        []float32 `json:"embedding"`
	Hash             string    `json:"hash"`
	Dim              int       `json:"dim"`
	Model            string    `json:"embedding_model"`
	EmbeddingPending bool      `json:"embedding_pending"`
}

type retrievalStatePendingCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type retrievalStateEmbeddingSpace struct {
	Space string `json:"space"`
	Count int    `json:"count"`
}

func TestRetrievalEmbeddingStateFixture(t *testing.T) {
	want, err := makeRetrievalEmbeddingStateFixture(t)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("../../../../testdata/port/retrieval/embedding-state.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	// #nosec G304 -- fixture path is fixed relative to this test package.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("Go retrieval oracle changed; regenerate %s", path)
	}
}

func makeRetrievalEmbeddingStateFixture(t *testing.T) (*retrievalEmbeddingStateFixture, error) {
	t.Helper()
	documents := []retrievalStateDocument{
		{Path: "/docs/alpha.md", Hash: "alpha"},
		{Path: "/docs/beta.md", Hash: "beta"},
		{Path: "/docs/legacy.md", Hash: "legacy"},
	}
	chunks := []retrievalStateChunk{
		{UUID: "alpha-768", DocumentPath: documents[0].Path, ChunkIndex: 0, Content: "alpha vector a", Embedding: []float32{1, 0}, Hash: "alpha-768", Dim: 768, Model: "model-a"},
		{UUID: "alpha-384", DocumentPath: documents[0].Path, ChunkIndex: 1, Content: "alpha vector b", Embedding: []float32{0, 1}, Hash: "alpha-384", Dim: 384, Model: "model-b"},
		{UUID: "alpha-pending", DocumentPath: documents[0].Path, ChunkIndex: 2, Content: "alpha local hash", Embedding: []float32{1, 0}, Hash: "alpha-pending", Dim: 64, Model: "local-hash", EmbeddingPending: true},
		{UUID: "beta-768", DocumentPath: documents[1].Path, ChunkIndex: 0, Content: "beta vector a", Embedding: []float32{1, 1}, Hash: "beta-768", Dim: 768, Model: "model-a"},
		{UUID: "beta-pending", DocumentPath: documents[1].Path, ChunkIndex: 1, Content: "beta local hash", Embedding: []float32{0, 1}, Hash: "beta-pending", Dim: 64, Model: "local-hash", EmbeddingPending: true},
		{UUID: "legacy-null", DocumentPath: documents[2].Path, ChunkIndex: 0, Content: "legacy embedding", Embedding: []float32{1, 0}, Hash: "legacy-null"},
	}
	database, err := OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	updatedAt := time.Unix(0, 0).UTC()
	for _, document := range documents {
		if err := database.SaveDocument(&Document{Path: document.Path, Hash: document.Hash, UpdatedAt: updatedAt}); err != nil {
			return nil, err
		}
	}
	rows := make([]*Chunk, len(chunks))
	for index, item := range chunks {
		rows[index] = &Chunk{
			UUID: item.UUID, DocumentPath: item.DocumentPath, ChunkIndex: item.ChunkIndex,
			Content: item.Content, Embedding: item.Embedding, Hash: item.Hash, Dim: item.Dim,
			Model: item.Model, EmbeddingPending: item.EmbeddingPending,
		}
	}
	if err := database.SaveChunks(rows); err != nil {
		return nil, err
	}
	if _, err := database.conn.Exec("UPDATE chunks SET embedding_dim = NULL, embedding_model = NULL WHERE uuid = ?", "legacy-null"); err != nil {
		return nil, err
	}
	fixture := &retrievalEmbeddingStateFixture{
		SchemaVersion: 1,
		Oracle: retrievalStateOracle{
			Commit: retrievalStateOracleCommit, Release: retrievalStateOracleRelease,
		},
		Documents:       documents,
		Chunks:          chunks,
		LegacyNullUUIDs: []string{"legacy-null"},
	}
	fixture.PendingTotal, err = database.CountPendingChunks()
	if err != nil {
		return nil, err
	}
	for _, document := range append(documents, retrievalStateDocument{Path: "/docs/missing.md"}) {
		count, err := database.CountPendingChunksForDocument(document.Path)
		if err != nil {
			return nil, err
		}
		fixture.PendingByDocument = append(fixture.PendingByDocument, retrievalStatePendingCount{Path: document.Path, Count: count})
	}
	spaces, err := database.DetectMixedEmbeddingSpaces()
	if err != nil {
		return nil, err
	}
	for space, count := range spaces {
		fixture.EmbeddingSpaces = append(fixture.EmbeddingSpaces, retrievalStateEmbeddingSpace{Space: space, Count: count})
	}
	sort.Slice(fixture.EmbeddingSpaces, func(i, j int) bool {
		return fixture.EmbeddingSpaces[i].Space < fixture.EmbeddingSpaces[j].Space
	})
	return fixture, nil
}
