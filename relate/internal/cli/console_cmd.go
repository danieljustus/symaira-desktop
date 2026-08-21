package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/console"
)

func init() {
	Register(&Command{
		Name:  "console",
		Short: "Run the local web console on 127.0.0.1 (see docs/CONSOLE.md)",
		Long:  `Start a localhost-only web UI for browsing and managing contacts in a browser. A session token is generated automatically and printed in the startup URL.`,
		Examples: `  symrelate console
  symrelate console --port 8080`,
		Run: runConsole,
	})
}

func runConsole(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	port := fs.Int("port", 0, "TCP port to bind on 127.0.0.1 (0 picks a free port)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	token, err := console.GenerateToken()
	if err != nil {
		return fmt.Errorf("failed to generate a session token: %w", err)
	}
	if err := writeConsoleTokenFile(a.Paths.DataDir, token); err != nil {
		fmt.Fprintf(iostreams.Stderr, "warning: failed to persist session token: %v\n", err)
	}

	ln, err := console.Listen(*port)
	if err != nil {
		return fmt.Errorf("failed to bind the console listener: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(iostreams.Stderr, "symrelate console: listening on http://%s/?token=%s\n", ln.Addr().String(), token)
	fmt.Fprintln(iostreams.Stderr, "symrelate console: open the URL above in a browser; the token is required once to start a session")

	srv := console.New(a, token)
	return srv.Serve(ctx, ln)
}

// writeConsoleTokenFile persists the current session token at a fixed,
// 0600 path so a companion tool on the same machine (or the user re-
// running `symrelate console --port` with the same port) can read it
// without it being printed anywhere logs might capture long-term. It is
// advisory only — the server always trusts the in-memory token it was
// started with, never this file, so a stale or tampered file can never
// grant access.
func writeConsoleTokenFile(dataDir, token string) error {
	path := filepath.Join(dataDir, "console-token")
	return os.WriteFile(path, []byte(token+"\n"), 0o600)
}
