package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-corekit/logkit"
	"github.com/danieljustus/symaira-corekit/versionkit"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/mcp"
)

// version is set by ldflags at build time: -X main.version=...
// When empty, debug.ReadBuildInfo() provides the module version (e.g. go install ...@latest).
var version = ""
var schemaVersion = 1

func init() {
	if version != "" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		v := strings.TrimPrefix(info.Main.Version, "v")
		if v != "" && v != "(devel)" {
			version = v
			return
		}
	}
	version = "(devel)"
}

var (
	cfg        *config.Config
	jsonFlag   bool
	outputFlag string
	vaultFlag  string
)

// Command groups shown by `symdesk --help` (#467). Every command registered
// on the root command should carry one of these GroupIDs so the help output
// reads as scannable sections instead of one flat alphabetical list.
const (
	groupVault       = "vault"
	groupDocuments   = "documents"
	groupAI          = "ai"
	groupServer      = "server"
	groupMaintenance = "maintenance"
)

// ensureCommandGroups registers the #467 command groups on cmd if they
// aren't already present. It is idempotent (checked via ContainsGroup) so it
// can safely run both in newRootCmd and at the top of registerCommands,
// which is also exercised directly by tests against a bare
// &cobra.Command{Use: "test"} that never goes through newRootCmd.
func ensureCommandGroups(cmd *cobra.Command) {
	for _, g := range []*cobra.Group{
		{ID: groupVault, Title: "Vault Commands:"},
		{ID: groupDocuments, Title: "Document Commands:"},
		{ID: groupAI, Title: "AI Commands:"},
		{ID: groupServer, Title: "Server Commands:"},
		{ID: groupMaintenance, Title: "Maintenance Commands:"},
	} {
		if !cmd.ContainsGroup(g.ID) {
			cmd.AddGroup(g)
		}
	}
}

func main() {
	cobra.OnInitialize(initConfig)

	rootCmd := newRootCmd()

	if err := rootCmd.Execute(); err != nil {
		var reported jsonReportedError
		switch {
		case errors.As(err, &reported):
			// The command already wrote a complete JSON report describing this
			// failure; a second document would break strict decoders (#438).
			// Only the exit code is still owed.
		case jsonFlag:
			// output error as JSON
			errObj := map[string]string{"error": err.Error()}
			if b, err := json.Marshal(errObj); err == nil {
				fmt.Println(string(b))
			}
		default:
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
		SilenceUsage:  true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Zero-stdio pollution: logging to stderr
			slog.SetDefault(logkit.New(os.Stderr, slog.LevelInfo, "text"))
			// Allow the app to pass a vault path explicitly on every command.
			if vaultFlag != "" {
				cfg.Vault = vaultFlag
			}
			// --output text|json|yaml is the canonical output switch;
			// --json stays as a bit-for-bit alias for --output json.
			if outputFlag != "" {
				switch outputFlag {
				case "json":
					jsonFlag = true
				case "text", "yaml":
					jsonFlag = false
				default:
					fmt.Fprintf(os.Stderr, "invalid --output value %q (want text|json|yaml)\n", outputFlag)
					os.Exit(int(exitcodes.ExitGeneric))
				}
			}
		},
	}
	rootCmd.Version = version
	rootCmd.PersistentFlags().BoolVar(&jsonFlag, "json", false, "output in JSON format")
	rootCmd.PersistentFlags().StringVar(&outputFlag, "output", "", "output format: text|json|yaml (--json is shorthand for --output json)")
	rootCmd.PersistentFlags().StringVar(&vaultFlag, "vault", "", "override document vault path (the Markdown workspace; unrelated to symvault)")

	// Command groups (#467): every command added below (here and in
	// registerCommands) gets a GroupID so `symdesk --help` renders scannable
	// sections instead of one 45-entry alphabetical list. `version` and the
	// cobra-provided `completion`/`help` commands intentionally stay
	// ungrouped and land under cobra's default "Additional Commands:".
	// cobra.Command.AddCommand panics if a subcommand's GroupID isn't
	// registered yet, so this must run before any GroupID-tagged
	// AddCommand call below (and registerCommands calls it again,
	// idempotently, for callers that build their own bare root command).
	ensureCommandGroups(rootCmd)

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
		Use:     "mcp",
		Short:   "Start the stdio MCP server",
		GroupID: groupServer,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcp.StartServer(cfg, version, allowWrite)
		},
	}
	mcpCmd.Flags().BoolVar(&allowWrite, "allow-write", false, "enable mutating MCP tools (note creation, ingest, status changes)")
	rootCmd.AddCommand(mcpCmd)

	serveCmd := newServeCmd()
	serveCmd.GroupID = groupServer
	rootCmd.AddCommand(serveCmd)

	workerCmd := newWorkerCmd()
	workerCmd.GroupID = groupServer
	rootCmd.AddCommand(workerCmd)

	permCmd := newPermissionsCmd()
	permCmd.GroupID = groupServer
	rootCmd.AddCommand(permCmd)

	// 3. Doctor Command (Stub)
	registerCommands(rootCmd)

	return rootCmd
}
