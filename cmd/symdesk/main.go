package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-corekit/logkit"
	"github.com/danieljustus/symaira-corekit/versionkit"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/mcp"
)

var version = "0.6.9"
var schemaVersion = 1

var (
	cfg       *config.Config
	jsonFlag  bool
	vaultFlag string
)

func main() {
	cobra.OnInitialize(initConfig)

	rootCmd := newRootCmd()

	if err := rootCmd.Execute(); err != nil {
		if jsonFlag {
			// output error as JSON
			errObj := map[string]string{"error": err.Error()}
			if b, err := json.Marshal(errObj); err == nil {
				fmt.Println(string(b))
			}
		} else {
			fmt.Fprintln(os.Stderr, exitcodes.FormatCLIError(err))
		}
		os.Exit(int(exitcodes.ExitCodeFromError(err)))
	}
}

func initConfig() {
	var err error
	cfg, err = config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(int(exitcodes.ExitConfig))
	}
}

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "symdesk",
		Short:         "Symaira-Desktop: Local-first Markdown Vault CLI and MCP Server",
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Zero-stdio pollution: logging to stderr
			slog.SetDefault(logkit.New(os.Stderr, slog.LevelInfo, "text"))
			// Allow the app to pass a vault path explicitly on every command.
			if vaultFlag != "" {
				cfg.Vault = vaultFlag
			}
		},
	}
	rootCmd.Version = version
	rootCmd.PersistentFlags().BoolVar(&jsonFlag, "json", false, "output in JSON format")
	rootCmd.PersistentFlags().StringVar(&vaultFlag, "vault", "", "override vault path")

	// 1. Version Command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := versionkit.New("symdesk", version, schemaVersion)
			if jsonFlag {
				return info.Write(os.Stdout)
			}
			fmt.Println(info.String())
			return nil
		},
	}
	rootCmd.AddCommand(versionCmd)

	// 2. MCP Command
	var allowWrite bool
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start the stdio MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcp.StartServer(cfg, version, allowWrite)
		},
	}
	mcpCmd.Flags().BoolVar(&allowWrite, "allow-write", false, "enable mutating MCP tools (note creation, ingest, status changes)")
	rootCmd.AddCommand(mcpCmd)

	// 3. Doctor Command (Stub)
	registerCommands(rootCmd)

	return rootCmd
}
