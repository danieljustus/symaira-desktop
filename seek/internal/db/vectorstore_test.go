package db

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestVectorStoreInterface(t *testing.T) {
	var _ VectorStore = (*DB)(nil)
}

func TestVectorStoreRoundTrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-vectorstore-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	t.Setenv("HOME", tempDir)

	dbClient, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer dbClient.Close()

	var vs VectorStore = dbClient
	ctx := context.Background()

	docPath := tempDir + "/test.md"
	if err := dbClient.SaveDocument(&Document{
		Path:      docPath,
		Hash:      "hash1",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	embedding := make([]float32, 768)
	embedding[0] = 1.0
	chunks := []*Chunk{
		{
			UUID:         "vs-uuid-1",
			DocumentPath: docPath,
			ChunkIndex:   0,
			Content:      "vectorstore roundtrip content",
			Embedding:    embedding,
			Hash:         "h1",
		},
	}

	if err := vs.Upsert(ctx, chunks); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	queryVec := make([]float32, 768)
	queryVec[0] = 1.0
	results, err := vs.Search(ctx, queryVec, 5)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result after Upsert")
	}
	if results[0].Chunk.UUID != "vs-uuid-1" {
		t.Errorf("expected vs-uuid-1, got %s", results[0].Chunk.UUID)
	}

	if err := vs.Delete(ctx, docPath); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	results, err = vs.Search(ctx, queryVec, 5)
	if err != nil {
		t.Fatalf("Search after Delete failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results after Delete, got %d", len(results))
	}
}
