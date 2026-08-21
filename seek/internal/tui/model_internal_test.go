package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/danieljustus/symaira-seek/internal/db"
)

func makeResultInternal(path string, chunkIdx int, content string, rrf float32) *db.SearchResult {
	return &db.SearchResult{
		Chunk: &db.Chunk{
			ID:           int64(chunkIdx),
			UUID:         path,
			DocumentPath: path,
			ChunkIndex:   chunkIdx,
			Content:      content,
		},
		RRFScore:    rrf,
		CosineScore: rrf,
		BM25Rank:    chunkIdx + 1,
		VectorRank:  chunkIdx + 1,
	}
}

func sampleResultsInternal() []*db.SearchResult {
	return []*db.SearchResult{
		makeResultInternal("/repo/main.go", 0, "func main() {}", 0.0312),
		makeResultInternal("/repo/engine/retrieval.go", 2, "// SearchHybrid", 0.0287),
		makeResultInternal("/repo/internal/db/db.go", 5, "type Chunk struct{}", 0.0251),
	}
}

func TestViewStatusMessage(t *testing.T) {
	m := New("test", sampleResultsInternal())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(Model)
	m3, _ := m.Update(editorFinishedMsg{err: nil})
	m = m3.(Model)

	view := m.View()
	if !strings.Contains(view, "returned from editor") {
		t.Fatalf("expected status message in view, got: %q", view)
	}
}

func TestEditorFinishedWithError(t *testing.T) {
	m := New("test", sampleResultsInternal())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(Model)
	m3, _ := m.Update(editorFinishedMsg{err: fmt.Errorf("boom")})
	m = m3.(Model)

	view := m.View()
	if !strings.Contains(view, "editor error") {
		t.Fatalf("expected editor error in view, got: %q", view)
	}
}

func TestOpenInEditorEarlyReturns(t *testing.T) {
	m := New("test", []*db.SearchResult{})
	if cmd := m.openInEditor(); cmd != nil {
		t.Fatalf("expected nil cmd for empty results, got %v", cmd)
	}
}
