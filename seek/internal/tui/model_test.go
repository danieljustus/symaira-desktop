package tui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/tui"
)

// makeResult is a test helper that builds a minimal SearchResult.
func makeResult(path string, chunkIdx int, content string, rrf float32) *db.SearchResult {
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

func sampleResults() []*db.SearchResult {
	return []*db.SearchResult{
		makeResult("/repo/main.go", 0, "func main() {\n\tfmt.Println(\"hello\")\n}", 0.0312),
		makeResult("/repo/engine/retrieval.go", 2, "// SearchHybrid runs BM25 and vector search concurrently.", 0.0287),
		makeResult("/repo/internal/db/db.go", 5, "type Chunk struct {\n\tID int64\n}", 0.0251),
	}
}

// TestNew verifies model construction without panicking.
func TestNew(t *testing.T) {
	m := tui.New("hybrid search", sampleResults())
	if m.Results() == nil {
		t.Fatal("expected results, got nil")
	}
	if m.Cursor() != 0 {
		t.Fatalf("expected initial cursor 0, got %d", m.Cursor())
	}
}

// TestNavigation verifies j/k cursor movement.
func TestNavigation(t *testing.T) {
	m := tui.New("test", sampleResults())

	// simulate window size so layout is initialised
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	// move down
	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = m3.(tui.Model)
	if m.Cursor() != 1 {
		t.Fatalf("expected cursor 1 after j, got %d", m.Cursor())
	}

	// move up
	m4, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = m4.(tui.Model)
	if m.Cursor() != 0 {
		t.Fatalf("expected cursor 0 after k, got %d", m.Cursor())
	}
}

// TestGoToEnd verifies G key jumps to last result.
func TestGoToEnd(t *testing.T) {
	results := sampleResults()
	m := tui.New("test", results)

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = m3.(tui.Model)
	if m.Cursor() != len(results)-1 {
		t.Fatalf("expected cursor %d after G, got %d", len(results)-1, m.Cursor())
	}
}

// TestGoToTop verifies g key jumps to first result.
func TestGoToTop(t *testing.T) {
	results := sampleResults()
	m := tui.New("test", results)

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	// go to end first
	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = m3.(tui.Model)

	// then back to top
	m4, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = m4.(tui.Model)
	if m.Cursor() != 0 {
		t.Fatalf("expected cursor 0 after g, got %d", m.Cursor())
	}
}

// TestQuit verifies q triggers quit.
func TestQuit(t *testing.T) {
	m := tui.New("test", sampleResults())

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	// After q the model sets quitting=true; the View should return empty.
	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	finalM := m3.(tui.Model)
	view := finalM.View()
	if strings.TrimSpace(view) != "" {
		t.Fatalf("expected empty view after q, got: %q", view)
	}
	_ = cmd
}

func TestInit(t *testing.T) {
	m := tui.New("test", sampleResults())
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("expected nil init cmd, got %v", cmd)
	}
}

func TestEscQuits(t *testing.T) {
	m := tui.New("test", sampleResults())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	m3, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected quit command after esc")
	}
	view := m3.(tui.Model).View()
	if strings.TrimSpace(view) != "" {
		t.Fatalf("expected empty view after esc, got: %q", view)
	}
}

func TestCtrlCQuits(t *testing.T) {
	m := tui.New("test", sampleResults())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit command after ctrl+c")
	}
}

func TestEnterOpensEditor(t *testing.T) {
	t.Setenv("EDITOR", "true")
	results := sampleResults()
	m := tui.New("test", results)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected editor command after enter")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("expected editor finished message")
	}
}

func TestEnterNoResults(t *testing.T) {
	m := tui.New("test", []*db.SearchResult{})
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected nil command for empty results, got %v", cmd)
	}
}

func TestCursorBounds(t *testing.T) {
	results := sampleResults()
	m := tui.New("test", results)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = m3.(tui.Model)
	if m.Cursor() != 0 {
		t.Fatalf("expected cursor 0 after k at top, got %d", m.Cursor())
	}

	for i := 0; i < len(results)+3; i++ {
		m4, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = m4.(tui.Model)
	}
	if m.Cursor() != len(results)-1 {
		t.Fatalf("expected cursor %d after repeated j, got %d", len(results)-1, m.Cursor())
	}
}

func TestViewNarrowWidth(t *testing.T) {
	m := tui.New("test", sampleResults())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 50})
	m = m2.(tui.Model)

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view for narrow width")
	}
}

func TestViewContainsQuery(t *testing.T) {
	m := tui.New("semantic search", sampleResults())

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	view := m.View()
	if !strings.Contains(view, "semantic search") {
		t.Fatalf("expected view to contain query %q", "semantic search")
	}
}

// TestEmptyResults verifies the TUI handles an empty result set gracefully.
func TestEmptyResults(t *testing.T) {
	m := tui.New("nothing", []*db.SearchResult{})

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = m2.(tui.Model)

	view := m.View()
	if !strings.Contains(view, "No search results") {
		t.Fatalf("expected empty-state message, got: %q", view)
	}
}
