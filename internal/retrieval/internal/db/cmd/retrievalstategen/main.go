// Command retrievalstategen freezes Go pending-embedding and embedding-space queries.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

const (
	defaultOracleCommit  = "38891d35eb8ceb6c348eca9a78b3fb2873677e3d"
	defaultOracleRelease = "post-v0.13.0-dependency-refresh"
)

type fixture struct {
	SchemaVersion     int            `json:"schema_version"`
	Oracle            oracle         `json:"oracle"`
	Documents         []document     `json:"documents"`
	Chunks            []chunk        `json:"chunks"`
	LegacyNullUUIDs   []string       `json:"legacy_null_uuids"`
	PendingTotal      int            `json:"pending_total"`
	PendingByDocument []pendingCount `json:"pending_by_document"`
	EmbeddingSpaces   []spaceCount   `json:"embedding_spaces"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type document struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type chunk struct {
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

type pendingCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type spaceCount struct {
	Space string `json:"space"`
	Count int    `json:"count"`
}

func main() {
	output := flag.String("output", "testdata/port/retrieval/embedding-state.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", defaultOracleCommit, "Go oracle commit")
	release := flag.String("oracle-release", defaultOracleRelease, "Go oracle release")
	flag.Parse()
	data, err := buildFixture(oracle{Commit: *commit, Release: *release})
	if err != nil {
		fatal("build fixture: %v", err)
	}
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil || !bytes.Equal(current, data) {
			fatal("embedding-state fixture is stale; regenerate deliberately")
		}
		fmt.Println("PASS embedding-state fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, data, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Println("PASS embedding-state fixture generated")
}

func buildFixture(oracle oracle) ([]byte, error) {
	documents := []document{
		{Path: "/docs/alpha.md", Hash: "alpha"},
		{Path: "/docs/beta.md", Hash: "beta"},
		{Path: "/docs/legacy.md", Hash: "legacy"},
	}
	chunks := []chunk{
		{UUID: "alpha-768", DocumentPath: documents[0].Path, ChunkIndex: 0, Content: "alpha vector a", Embedding: []float32{1, 0}, Hash: "alpha-768", Dim: 768, Model: "model-a"},
		{UUID: "alpha-384", DocumentPath: documents[0].Path, ChunkIndex: 1, Content: "alpha vector b", Embedding: []float32{0, 1}, Hash: "alpha-384", Dim: 384, Model: "model-b"},
		{UUID: "alpha-pending", DocumentPath: documents[0].Path, ChunkIndex: 2, Content: "alpha local hash", Embedding: []float32{1, 0}, Hash: "alpha-pending", Dim: 64, Model: "local-hash", EmbeddingPending: true},
		{UUID: "beta-768", DocumentPath: documents[1].Path, ChunkIndex: 0, Content: "beta vector a", Embedding: []float32{1, 1}, Hash: "beta-768", Dim: 768, Model: "model-a"},
		{UUID: "beta-pending", DocumentPath: documents[1].Path, ChunkIndex: 1, Content: "beta local hash", Embedding: []float32{0, 1}, Hash: "beta-pending", Dim: 64, Model: "local-hash", EmbeddingPending: true},
		{UUID: "legacy-null", DocumentPath: documents[2].Path, ChunkIndex: 0, Content: "legacy embedding", Embedding: []float32{1, 0}, Hash: "legacy-null"},
	}
	path, err := os.CreateTemp("", "symdesk-embedding-state-*.db")
	if err != nil {
		return nil, err
	}
	dbPath := path.Name()
	_ = path.Close()
	defer os.Remove(dbPath)

	store, err := db.OpenAt(dbPath)
	if err != nil {
		return nil, err
	}
	for _, item := range documents {
		if err := store.SaveDocument(&db.Document{Path: item.Path, Hash: item.Hash, UpdatedAt: time.Unix(0, 0).UTC()}); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	rows := make([]*db.Chunk, len(chunks))
	for i, item := range chunks {
		rows[i] = &db.Chunk{
			UUID: item.UUID, DocumentPath: item.DocumentPath, ChunkIndex: item.ChunkIndex,
			Content: item.Content, Embedding: item.Embedding, Hash: item.Hash, Dim: item.Dim,
			Model: item.Model, EmbeddingPending: item.EmbeddingPending,
		}
	}
	if err := store.SaveChunks(rows); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, err
	}
	legacyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := legacyDB.Exec("UPDATE chunks SET embedding_dim = NULL, embedding_model = NULL WHERE uuid = ?", "legacy-null"); err != nil {
		_ = legacyDB.Close()
		return nil, err
	}
	if err := legacyDB.Close(); err != nil {
		return nil, err
	}
	store, err = db.OpenAt(dbPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	result := fixture{
		SchemaVersion: 1, Oracle: oracle, Documents: documents, Chunks: chunks,
		LegacyNullUUIDs: []string{"legacy-null"},
	}
	result.PendingTotal, err = store.CountPendingChunks()
	if err != nil {
		return nil, err
	}
	for _, item := range append(documents, document{Path: "/docs/missing.md"}) {
		count, err := store.CountPendingChunksForDocument(item.Path)
		if err != nil {
			return nil, err
		}
		result.PendingByDocument = append(result.PendingByDocument, pendingCount{Path: item.Path, Count: count})
	}
	spaces, err := store.DetectMixedEmbeddingSpaces()
	if err != nil {
		return nil, err
	}
	for space, count := range spaces {
		result.EmbeddingSpaces = append(result.EmbeddingSpaces, spaceCount{Space: space, Count: count})
	}
	sort.Slice(result.EmbeddingSpaces, func(i, j int) bool {
		return result.EmbeddingSpaces[i].Space < result.EmbeddingSpaces[j].Space
	})
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
