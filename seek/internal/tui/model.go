// Package tui provides an interactive terminal user interface for browsing
// semantic search results produced by symaira-seek. It uses the bubbletea
// event loop and lipgloss for styling.
//
// Zero-Stdout contract: this package must never write to os.Stdout directly.
// All terminal rendering goes through the bubbletea renderer; log/diagnostic
// output goes to os.Stderr. The MCP stdio transport is therefore unaffected.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// ─── Styles ──────────────────────────────────────────────────────────────────

var (
	styleBanner = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#C8B8FF")).
			MarginBottom(1)

	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#5B4FBE"))

	styleNormal = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#CCCCCC"))

	styleDim = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	styleScore = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#88C0D0"))

	stylePreviewBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#5B4FBE")).
				Padding(0, 1)

	stylePreviewTitle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#A3BE8C"))

	styleHelp = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#555555")).
			MarginTop(1)

	styleStatusBar = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888"))
)

// ─── Messages ────────────────────────────────────────────────────────────────

// editorFinishedMsg is sent after the external editor process exits.
type editorFinishedMsg struct{ err error }

// ─── Model ───────────────────────────────────────────────────────────────────

// Model is the bubbletea application model for the TUI search browser.
type Model struct {
	results  []*db.SearchResult
	cursor   int
	width    int
	height   int
	listH    int // usable list-panel height (rows)
	previewH int // usable preview-panel height (rows)
	previewW int // usable preview-panel width (cols)
	listW    int // usable list-panel width
	query    string
	quitting bool
	status   string // transient status message (e.g. "Opened in $EDITOR")
}

// New returns a ready-to-use TUI model for the given results set.
func New(query string, results []*db.SearchResult) Model {
	return Model{
		query:   query,
		results: results,
	}
}

// Cursor returns the current cursor position (selected result index).
func (m Model) Cursor() int { return m.cursor }

// Results returns the slice of search results backing the model.
func (m Model) Results() []*db.SearchResult { return m.results }

// ─── bubbletea interface ──────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()

	case tea.KeyMsg:
		m.status = "" // clear transient status on any key
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < len(m.results)-1 {
				m.cursor++
			}

		case "g":
			m.cursor = 0

		case "G":
			if len(m.results) > 0 {
				m.cursor = len(m.results) - 1
			}

		case "enter":
			return m, m.openInEditor()
		}

	case editorFinishedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("editor error: %v", msg.err)
		} else {
			m.status = "returned from editor"
		}
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if len(m.results) == 0 {
		return "\n  No search results to display.\n\n  Press q to quit.\n"
	}

	banner := styleBanner.Render("  symseek › " + m.query)

	list := m.renderList()
	preview := m.renderPreview()

	var body string
	if m.width >= 120 {
		// side-by-side layout
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, preview)
	} else {
		// stacked layout for narrow terminals
		body = lipgloss.JoinVertical(lipgloss.Left, list, preview)
	}

	help := styleHelp.Render("  j/↓ down · k/↑ up · g top · G bottom · Enter open in $EDITOR · q quit")

	var statusLine string
	if m.status != "" {
		statusLine = "\n" + styleStatusBar.Render("  "+m.status)
	}

	return lipgloss.JoinVertical(lipgloss.Left, banner, body, help, statusLine)
}

// ─── Layout ──────────────────────────────────────────────────────────────────

func (m *Model) recalcLayout() {
	// reserve 3 rows: banner + help + possible status
	reserved := 3
	usable := m.height - reserved
	if usable < 4 {
		usable = 4
	}

	if m.width >= 120 {
		m.listW = m.width / 3
		m.previewW = m.width - m.listW - 2
		m.listH = usable
		m.previewH = usable
	} else {
		m.listW = m.width
		m.previewW = m.width
		m.listH = usable / 2
		m.previewH = usable - m.listH
	}
}

// ─── Rendering helpers ────────────────────────────────────────────────────────

func (m Model) renderList() string {
	if m.listH <= 0 {
		m.listH = 20
	}
	if m.listW <= 0 {
		m.listW = 40
	}

	// determine scroll window so selected item is always visible
	start := 0
	maxVisible := m.listH
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.results) {
		end = len(m.results)
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		r := m.results[i]
		label := fmt.Sprintf(" [%d] %s (chunk %d)", i+1,
			filepath.Base(r.Chunk.DocumentPath), r.Chunk.ChunkIndex)

		score := styleScore.Render(fmt.Sprintf("  RRF %.4f", r.RRFScore))

		row := lipgloss.JoinHorizontal(lipgloss.Top, label, score)

		// truncate to list width
		if lipgloss.Width(row) > m.listW-2 {
			row = truncate(row, m.listW-2)
		}

		if i == m.cursor {
			sb.WriteString(styleSelected.Width(m.listW).Render(row))
		} else {
			sb.WriteString(styleNormal.Render(row))
		}
		sb.WriteByte('\n')
	}

	// fill remaining rows
	for i := end - start; i < maxVisible; i++ {
		sb.WriteByte('\n')
	}

	return sb.String()
}

func (m Model) renderPreview() string {
	if m.previewW <= 0 {
		m.previewW = 60
	}
	if m.previewH <= 0 {
		m.previewH = 20
	}
	if len(m.results) == 0 || m.cursor >= len(m.results) {
		return stylePreviewBorder.Width(m.previewW).Height(m.previewH).Render(
			styleDim.Render("No result selected."),
		)
	}

	r := m.results[m.cursor]

	title := stylePreviewTitle.Render(r.Chunk.DocumentPath)
	meta := styleDim.Render(fmt.Sprintf(
		"Chunk %d · RRF %.4f · Cosine %.4f · BM25 rank %d · Vec rank %d",
		r.Chunk.ChunkIndex, r.RRFScore, r.CosineScore, r.BM25Rank, r.VectorRank,
	))

	// limit content lines to available height
	lines := strings.Split(r.Chunk.Content, "\n")
	maxLines := m.previewH - 4 // title + meta + borders
	if maxLines < 1 {
		maxLines = 1
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	content := strings.Join(lines, "\n")

	inner := lipgloss.JoinVertical(lipgloss.Left, title, meta, "", content)
	return stylePreviewBorder.Width(m.previewW - 2).Render(inner)
}

// ─── Editor integration ───────────────────────────────────────────────────────

// openInEditor launches $EDITOR (falling back to "vi") for the currently
// selected result's file. It suspends the bubbletea renderer, waits for the
// editor to exit, then resumes. The actual exec runs in a separate goroutine
// so we can return a tea.Cmd.
func (m Model) openInEditor() tea.Cmd {
	if len(m.results) == 0 || m.cursor >= len(m.results) {
		return nil
	}
	r := m.results[m.cursor]
	filePath := r.Chunk.DocumentPath

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	// Construct the command; some editors accept +<line> to jump to a line.
	// We use chunk_index as a rough line hint (not perfect but helpful).
	lineHint := fmt.Sprintf("+%d", r.Chunk.ChunkIndex*20+1)

	c := exec.Command(editor, lineHint, filePath) //nolint:gosec // editor from env
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorFinishedMsg{err: err}
	})
}

// ─── Utilities ────────────────────────────────────────────────────────────────

// truncate shortens s to at most n visible characters (ANSI-aware via lipgloss).
func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	// crude rune truncation — lipgloss Width handles ANSI sequences
	runes := []rune(s)
	for i := len(runes); i > 0; i-- {
		candidate := string(runes[:i])
		if lipgloss.Width(candidate) <= n-1 {
			return candidate + "…"
		}
	}
	return ""
}
