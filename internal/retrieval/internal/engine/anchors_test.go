package engine

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/parser"
)

func TestBuildChunksFromSectionsDoesNotCrossAnchors(t *testing.T) {
	sections := []parser.Section{
		{Text: "page one hit", Start: 0, Anchor: parser.Anchor{Kind: "page", Value: "1"}},
		{Text: "page two hit", Start: len("page one hit") + 2, Anchor: parser.Anchor{Kind: "page", Value: "2"}},
	}
	chunks := buildChunksFromSections(&fakeEmbedder{dim: 8}, "/tmp/example.pdf", sections)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].AnchorKind != "page" || chunks[0].AnchorValue != "1" {
		t.Fatalf("first chunk anchor = %s:%s", chunks[0].AnchorKind, chunks[0].AnchorValue)
	}
	if chunks[1].AnchorKind != "page" || chunks[1].AnchorValue != "2" {
		t.Fatalf("second chunk anchor = %s:%s", chunks[1].AnchorKind, chunks[1].AnchorValue)
	}
}

func TestBuildChunksUsesStableTextOffsetFallback(t *testing.T) {
	chunks := buildChunks(&fakeEmbedder{dim: 8}, "/tmp/example.txt", "plain text")
	if len(chunks) != 1 || chunks[0].AnchorKind != "text" || chunks[0].AnchorValue != "offset:0" {
		t.Fatalf("got %#v, want text offset anchor", chunks)
	}
}
