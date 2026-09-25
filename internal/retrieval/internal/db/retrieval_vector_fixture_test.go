package db

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type retrievalVectorFixture struct {
	SchemaVersion int                     `json:"schema_version"`
	Chunks        []*Chunk                `json:"chunks"`
	Searches      []retrievalVectorSearch `json:"searches"`
}

type retrievalVectorSearch struct {
	Query      []float32            `json:"query"`
	PathPrefix string               `json:"path_prefix"`
	Limit      int                  `json:"limit"`
	Results    []retrievalVectorHit `json:"results"`
}

type retrievalVectorHit struct {
	VectorRank  int                      `json:"vector_rank"`
	CosineScore float32                  `json:"cosine_score"`
	Chunk       retrievalVectorSearchRow `json:"chunk"`
}

type retrievalVectorSearchRow struct {
	ID           int64     `json:"id"`
	UUID         string    `json:"uuid"`
	DocumentPath string    `json:"document_path"`
	ChunkIndex   int       `json:"chunk_index"`
	Content      string    `json:"content"`
	Embedding    []float32 `json:"embedding"`
	Hash         string    `json:"hash"`
}

func TestRetrievalVectorFixture(t *testing.T) {
	want, err := makeRetrievalVectorFixture(t)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("../../../../testdata/port/retrieval/retrieval-vector.json")
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
		t.Fatalf("Go retrieval vector oracle changed; regenerate %s", path)
	}
}

func makeRetrievalVectorFixture(t *testing.T) (*retrievalVectorFixture, error) {
	t.Helper()
	database, err := OpenAt(filepath.Join(t.TempDir(), "retrieval.db"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()

	updatedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	chunks := []*Chunk{
		{UUID: "vector-a", DocumentPath: "/vault/alpha/a.md", ChunkIndex: 0, Content: "alpha direction", Embedding: []float32{3, 4}, Hash: "hash-a", Dim: 2, Model: "fixture-model"},
		{UUID: "vector-b", DocumentPath: "/vault/beta/b.md", ChunkIndex: 0, Content: "beta direction", Embedding: []float32{4, 3}, Hash: "hash-b", Dim: 2, Model: "fixture-model"},
		{UUID: "vector-c", DocumentPath: "/vault/alpha/c.md", ChunkIndex: 0, Content: "alpha orthogonal", Embedding: []float32{0, 1}, Hash: "hash-c", Dim: 2, Model: "fixture-model"},
		{UUID: "vector-d", DocumentPath: "/vault/beta/d.md", ChunkIndex: 0, Content: "beta opposite", Embedding: []float32{-1, 0}, Hash: "hash-d", Dim: 2, Model: "fixture-model"},
	}
	for _, chunk := range chunks {
		if err := database.SaveDocument(&Document{Path: chunk.DocumentPath, Hash: "doc-" + chunk.Hash, UpdatedAt: updatedAt}); err != nil {
			return nil, err
		}
	}
	if err := database.SaveChunks(chunks); err != nil {
		return nil, err
	}

	fixture := &retrievalVectorFixture{SchemaVersion: 1, Chunks: chunks}
	for _, search := range []retrievalVectorSearch{
		{Query: []float32{1, 0}, Limit: 4},
		{Query: []float32{1, 0}, PathPrefix: "/vault/alpha/", Limit: 2},
	} {
		var results []*SearchResult
		if search.PathPrefix == "" {
			results, err = database.SearchVector(search.Query, search.Limit)
		} else {
			results, err = database.SearchVectorWithPath(search.Query, search.PathPrefix, search.Limit)
		}
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			search.Results = append(search.Results, retrievalVectorHit{
				VectorRank:  result.VectorRank,
				CosineScore: result.CosineScore,
				Chunk: retrievalVectorSearchRow{
					ID: result.Chunk.ID, UUID: result.Chunk.UUID, DocumentPath: result.Chunk.DocumentPath,
					ChunkIndex: result.Chunk.ChunkIndex, Content: result.Chunk.Content,
					Embedding: result.Chunk.Embedding, Hash: result.Chunk.Hash,
				},
			})
		}
		if search.Results == nil {
			search.Results = []retrievalVectorHit{}
		}
		fixture.Searches = append(fixture.Searches, search)
	}
	return fixture, nil
}
