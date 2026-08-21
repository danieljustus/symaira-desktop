package tui_test

import (
	"testing"

	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/tui"
)

func TestRunEmptyResults(t *testing.T) {
	if err := tui.Run("query", nil); err != nil {
		t.Fatalf("expected nil for nil results, got %v", err)
	}
}

func TestRunNoResults(t *testing.T) {
	if err := tui.Run("query", []*db.SearchResult{}); err != nil {
		t.Fatalf("expected nil for empty results, got %v", err)
	}
}

func TestRunRequiresTTY(t *testing.T) {
	results := []*db.SearchResult{
		{
			Chunk: &db.Chunk{
				DocumentPath: "/tmp/doc.md",
				Content:      "hello world",
			},
		},
	}
	err := tui.Run("query", results)
	if err == nil {
		t.Fatal("expected error when running TUI without a TTY")
	}
}
