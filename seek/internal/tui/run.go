package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// Run starts the interactive TUI for the given results and query string.
// It writes the bubbletea output to the terminal directly (not os.Stdout),
// preserving the MCP stdio zero-pollution contract.
//
// Returns an error if the TUI cannot start (e.g. not a real TTY).
func Run(query string, results []*db.SearchResult) error {
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "symseek tui: no results to display")
		return nil
	}

	m := New(query, results)

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
