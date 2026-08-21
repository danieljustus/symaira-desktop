package cli

import (
	"context"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/mcp"
)

func init() {
	Register(&Command{
		Name:  "mcp",
		Short: "Run the MCP server on stdio (see docs/MCP.md)",
		Run:   runMCP,
	})
}

// runMCP serves MCP tool calls over stdin/stdout until EOF. Stdout carries
// JSON-RPC frames only; all diagnostics go to stderr via corekit/mcpserver
// (log/slog) — the same protocol hygiene internal/cli.IO already documents
// for the CLI's own --json mode.
func runMCP(ctx context.Context, iostreams IO, args []string) error {
	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	return mcp.New(a).Run(ctx, iostreams.Stdin, iostreams.Stdout)
}
