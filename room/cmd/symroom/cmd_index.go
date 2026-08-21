package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/index"
	"github.com/danieljustus/symaira-room/internal/journal"
)

// runIndex implements the "index" subcommand.
func runIndex(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 || args[2] != "rebuild" {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom index rebuild")
		return int(exitcodes.ExitOK)
	}

	j := journal.New(filepath.Join(roomDir(), "journal"))
	dbPath := filepath.Join(".symroom", "index.sqlite")
	indexer := index.New(dbPath)

	if err := indexer.Rebuild(j); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error rebuilding index: %v\n", err)
		return int(exitcodes.ExitGeneric)
	}

	_, _ = fmt.Fprintf(stdout, "Rebuilt derived index at %s\n", dbPath)
	return int(exitcodes.ExitOK)
}
