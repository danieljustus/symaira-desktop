package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/parser"
)

type retrievalChunkFixture struct {
	SchemaVersion int                         `json:"schema_version"`
	Cases         []retrievalChunkFixtureCase `json:"cases"`
}

type retrievalChunkFixtureCase struct {
	ID       string                       `json:"id"`
	Source   string                       `json:"source"`
	Sections []parser.Section             `json:"sections"`
	Chunks   []retrievalChunkFixtureChunk `json:"chunks"`
}

type retrievalChunkFixtureChunk struct {
	UUID        string `json:"uuid"`
	ChunkIndex  int    `json:"chunk_index"`
	Content     string `json:"content"`
	Hash        string `json:"hash"`
	CharStart   *int   `json:"char_start,omitempty"`
	CharEnd     *int   `json:"char_end,omitempty"`
	AnchorKind  string `json:"anchor_kind"`
	AnchorValue string `json:"anchor_value"`
}

func buildRetrievalChunkFixture() retrievalChunkFixture {
	long := strings.Repeat("Abschnitt αβγ bleibt bytegenau. Weitere Wörter folgen hier.\n", 45)
	cases := []retrievalChunkFixtureCase{
		{
			ID:     "unicode-sections-and-overlap",
			Source: "/vault/研究/über.md",
			Sections: []parser.Section{
				{Text: "Einleitung: Grüße 🧭", Start: 7, Anchor: parser.Anchor{Kind: "text", Value: "offset:7"}},
				{Text: "# Überblick\n" + long, Start: 42, Anchor: parser.Anchor{Kind: "heading", Value: "Überblick"}},
				{Text: "owner: Zoë 🧪", Anchor: parser.Anchor{Kind: "metadata", Value: "owner"}, Synthetic: true},
			},
		},
		{
			ID:       "empty-section",
			Source:   "/vault/empty.md",
			Sections: []parser.Section{{Text: "", Anchor: parser.Anchor{Kind: "text", Value: "offset:0"}}},
		},
		{
			ID:       "no-sections",
			Source:   "/vault/no-sections.md",
			Sections: []parser.Section{},
		},
	}
	for i := range cases {
		chunks := buildChunksFromSections(&fakeEmbedder{dim: 8}, cases[i].Source, cases[i].Sections)
		cases[i].Chunks = make([]retrievalChunkFixtureChunk, 0, len(chunks))
		for _, chunk := range chunks {
			cases[i].Chunks = append(cases[i].Chunks, retrievalChunkFixtureChunk{
				UUID: chunk.UUID, ChunkIndex: chunk.ChunkIndex, Content: chunk.Content,
				Hash: chunk.Hash, CharStart: chunk.CharStart, CharEnd: chunk.CharEnd,
				AnchorKind: chunk.AnchorKind, AnchorValue: chunk.AnchorValue,
			})
		}
	}
	return retrievalChunkFixture{SchemaVersion: 1, Cases: cases}
}

// TestRetrievalChunksFixture checks the committed Go oracle and refreshes it
// when PORT_GENERATE=1 is set.
func TestRetrievalChunksFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "..", "testdata", "port", "retrieval", "retrieval-chunks.json")
	content, err := json.MarshalIndent(buildRetrievalChunkFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(fixturePath) //nolint:gosec // fixed repository fixture path.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, content) {
		t.Fatal("retrieval chunks fixture drift; regenerate with PORT_GENERATE=1")
	}
}
