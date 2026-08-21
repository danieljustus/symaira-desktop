package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/mcp"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the Model Context Protocol server (stdio) for AI agents",
		Long: `Expose symprint over MCP so AI agents can render PDFs, list profiles, and
validate documents. Transport is stdio JSON-RPC 2.0; all logs go to stderr.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := loadConfigOrWarn(cmd)
			mcp.ServerVersion = version
			if err := mcp.StartServer(cmd.Context(), cfg); err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "mcp server")
			}
			return nil
		},
	}
}
