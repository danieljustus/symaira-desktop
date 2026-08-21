package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/mcp"
)

// runMcp implements the "mcp" subcommand.
func runMcp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	mcpRoomDir := fs.String("room", ".", "Room directory")
	identityName := fs.String("identity", "", "Signing identity name")
	artifactRoot := fs.String("artifact-root", "", "Artifact root directory")
	if err := fs.Parse(args[2:]); err != nil {
		return int(exitcodes.ExitNoInput)
	}
	id := resolveIdentity(*identityName)
	if err := mcp.NewServer(*mcpRoomDir, id, *artifactRoot).ServeStdio(context.Background()); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return int(exitcodes.ExitGeneric)
	}
	return int(exitcodes.ExitOK)
}
