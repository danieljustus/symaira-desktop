package db

import (
	"testing"
	"time"
)

func TestCountPendingChunks(t *testing.T) {
	d := setupDB(t)

	fixtures := []struct {
		docPath string
		chunks  []*Chunk
	}{
		{
			docPath: "/docs/alpha.md",
			chunks: []*Chunk{
				{UUID: "alpha-1", DocumentPath: "/docs/alpha.md", ChunkIndex: 0, Content: "alpha pending one", Embedding: []float32{1, 0}, Hash: "alpha-1", EmbeddingPending: true},
				{UUID: "alpha-2", DocumentPath: "/docs/alpha.md", ChunkIndex: 1, Content: "alpha ready", Embedding: []float32{0, 1}, Hash: "alpha-2"},
				{UUID: "alpha-3", DocumentPath: "/docs/alpha.md", ChunkIndex: 2, Content: "alpha pending two", Embedding: []float32{1, 1}, Hash: "alpha-3", EmbeddingPending: true},
			},
		},
		{
			docPath: "/docs/beta.md",
			chunks: []*Chunk{
				{UUID: "beta-1", DocumentPath: "/docs/beta.md", ChunkIndex: 0, Content: "beta ready one", Embedding: []float32{1, 0}, Hash: "beta-1"},
				{UUID: "beta-2", DocumentPath: "/docs/beta.md", ChunkIndex: 1, Content: "beta pending", Embedding: []float32{0, 1}, Hash: "beta-2", EmbeddingPending: true},
				{UUID: "beta-3", DocumentPath: "/docs/beta.md", ChunkIndex: 2, Content: "beta ready two", Embedding: []float32{1, 1}, Hash: "beta-3"},
			},
		},
		{
			docPath: "/docs/gamma.md",
			chunks: []*Chunk{
				{UUID: "gamma-1", DocumentPath: "/docs/gamma.md", ChunkIndex: 0, Content: "gamma ready", Embedding: []float32{1, 0}, Hash: "gamma-1"},
			},
		},
	}

	for _, fixture := range fixtures {
		if err := d.SaveDocument(&Document{Path: fixture.docPath, Hash: fixture.docPath, UpdatedAt: time.Now()}); err != nil {
			t.Fatalf("SaveDocument(%q): %v", fixture.docPath, err)
		}
		if err := d.SaveChunks(fixture.chunks); err != nil {
			t.Fatalf("SaveChunks(%q): %v", fixture.docPath, err)
		}
	}

	if got, err := d.CountPendingChunks(); err != nil {
		t.Fatalf("CountPendingChunks: %v", err)
	} else if got != 3 {
		t.Errorf("CountPendingChunks = %d, want 3", got)
	}

	for _, test := range []struct {
		docPath string
		want    int
	}{
		{docPath: "/docs/alpha.md", want: 2},
		{docPath: "/docs/beta.md", want: 1},
		{docPath: "/docs/gamma.md", want: 0},
		{docPath: "/docs/missing.md", want: 0},
	} {
		t.Run(test.docPath, func(t *testing.T) {
			got, err := d.CountPendingChunksForDocument(test.docPath)
			if err != nil {
				t.Fatalf("CountPendingChunksForDocument(%q): %v", test.docPath, err)
			}
			if got != test.want {
				t.Errorf("CountPendingChunksForDocument(%q) = %d, want %d", test.docPath, got, test.want)
			}
		})
	}
}

func TestCountPendingChunksClosedDatabase(t *testing.T) {
	d := setupDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, test := range []struct {
		name string
		call func() (int, error)
		want string
	}{
		{
			name: "all documents",
			call: d.CountPendingChunks,
			want: "count pending chunks: sql: database is closed",
		},
		{
			name: "one document",
			call: func() (int, error) {
				return d.CountPendingChunksForDocument("/docs/alpha.md")
			},
			want: "count pending chunks for document: sql: database is closed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.call()
			if got != 0 {
				t.Errorf("count = %d after database close, want 0", got)
			}
			if err == nil {
				t.Fatal("count accepted a closed database")
			}
			if got := err.Error(); got != test.want {
				t.Errorf("error = %q, want %q", got, test.want)
			}
		})
	}
}
