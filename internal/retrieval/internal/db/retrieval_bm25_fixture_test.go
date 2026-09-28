package db

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type retrievalBM25Fixture struct {
	SchemaVersion int                         `json:"schema_version"`
	Migrations    []string                    `json:"migrations"`
	Documents     []*Document                 `json:"documents"`
	Chunks        []*Chunk                    `json:"chunks"`
	ByDocument    []retrievalBM25DocumentRows `json:"by_document"`
	Searches      []retrievalBM25Search       `json:"searches"`
}

type retrievalBM25DocumentRows struct {
	Path   string   `json:"path"`
	Chunks []*Chunk `json:"chunks"`
}

type retrievalBM25Search struct {
	Query   string             `json:"query"`
	Path    string             `json:"path"`
	Limit   int                `json:"limit"`
	Results []retrievalBM25Hit `json:"results"`
}

type retrievalBM25Hit struct {
	BM25Rank int                      `json:"bm25_rank"`
	Chunk    retrievalBM25SearchChunk `json:"chunk"`
}

type retrievalBM25SearchChunk struct {
	ID           int64     `json:"id"`
	UUID         string    `json:"uuid"`
	DocumentPath string    `json:"document_path"`
	ChunkIndex   int       `json:"chunk_index"`
	Content      string    `json:"content"`
	Embedding    []float32 `json:"embedding"`
	Hash         string    `json:"hash"`
}

func TestRetrievalBM25Fixture(t *testing.T) {
	want, err := makeRetrievalBM25Fixture(t)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("../../../../testdata/port/retrieval/retrieval-bm25.json")
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
		t.Fatalf("Go retrieval oracle changed; regenerate %s", path)
	}
}

func makeRetrievalBM25Fixture(t *testing.T) (*retrievalBM25Fixture, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retrieval.db")
	database, err := OpenAt(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()

	updatedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	documents := []*Document{
		{Path: "/vault/alpha/a.md", Hash: "doc-a", UpdatedAt: updatedAt},
		{Path: "/vault/alpha/b.md", Hash: "doc-b", UpdatedAt: updatedAt},
		{Path: "/vault/beta/c.md", Hash: "doc-c", UpdatedAt: updatedAt},
		{Path: "/vault/beta/d.md", Hash: "doc-d", UpdatedAt: updatedAt},
	}
	for _, document := range documents {
		if err := database.SaveDocument(document); err != nil {
			return nil, err
		}
	}
	chunks := []*Chunk{
		{UUID: "retrieval-a", DocumentPath: documents[0].Path, ChunkIndex: 0, Content: "quartz quartz quartz guide", Embedding: []float32{}, Hash: "hash-a", Dim: 0, Model: "", CharStart: intPointer(0), CharEnd: intPointer(26), AnchorKind: "heading", AnchorValue: "alpha"},
		{UUID: "retrieval-b", DocumentPath: documents[1].Path, ChunkIndex: 0, Content: "quartz quartz guide", Embedding: []float32{}, Hash: "hash-b", Dim: 0, Model: "", CharStart: intPointer(0), CharEnd: intPointer(19), AnchorKind: "heading", AnchorValue: "beta"},
		{UUID: "retrieval-c", DocumentPath: documents[2].Path, ChunkIndex: 0, Content: "quartz guide", Embedding: []float32{}, Hash: "hash-c", Dim: 0, Model: "", CharStart: intPointer(0), CharEnd: intPointer(12), AnchorKind: "heading", AnchorValue: "gamma"},
		{UUID: "retrieval-d", DocumentPath: documents[3].Path, ChunkIndex: 0, Content: "unrelated material", Embedding: []float32{}, Hash: "hash-d", Dim: 0, Model: "", CharStart: intPointer(0), CharEnd: intPointer(18), AnchorKind: "heading", AnchorValue: "delta"},
	}
	if err := database.SaveChunks(chunks); err != nil {
		return nil, err
	}

	fixture := &retrievalBM25Fixture{
		SchemaVersion: 1,
		Documents:     documents,
		Chunks:        chunks,
	}
	rows, err := database.conn.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return nil, err
		}
		fixture.Migrations = append(fixture.Migrations, version)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, document := range documents {
		stored, err := database.GetChunksForDocument(document.Path)
		if err != nil {
			return nil, err
		}
		fixture.ByDocument = append(fixture.ByDocument, retrievalBM25DocumentRows{Path: document.Path, Chunks: stored})
	}
	for _, query := range []retrievalBM25Search{
		{Query: "quartz", Limit: 10},
		{Query: "quartz", Path: "/vault/alpha/", Limit: 1},
		{Query: "quartz", Path: "/vault/beta/", Limit: 1},
		{Query: "no-match", Limit: 10},
		{Query: "the", Limit: 10},
	} {
		var results []*SearchResult
		if query.Path == "" {
			results, err = database.SearchBM25(query.Query, query.Limit)
		} else {
			results, err = database.SearchBM25WithPath(query.Query, query.Path, query.Limit)
		}
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			query.Results = append(query.Results, retrievalBM25Hit{
				BM25Rank: result.BM25Rank,
				Chunk: retrievalBM25SearchChunk{
					ID: result.Chunk.ID, UUID: result.Chunk.UUID, DocumentPath: result.Chunk.DocumentPath,
					ChunkIndex: result.Chunk.ChunkIndex, Content: result.Chunk.Content,
					Embedding: result.Chunk.Embedding, Hash: result.Chunk.Hash,
				},
			})
		}
		if query.Results == nil {
			query.Results = []retrievalBM25Hit{}
		}
		fixture.Searches = append(fixture.Searches, query)
	}
	return fixture, nil
}

func intPointer(value int) *int { return &value }
