package cli

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/app"
)

func init() {
	Register(&Command{
		Name:     "doctor",
		Short:    "Check the local environment and database health",
		Long:     `Verify that config, data and cache directories exist and the SQLite database is reachable. Output is always safe to paste into a bug report — no contact data is printed.`,
		Examples: `  symrelate doctor`,
		Run:      runDoctor,
	})
}

// runDoctor never prints contact data — only paths and connectivity — so
// its output is always safe to paste into a bug report.
func runDoctor(ctx context.Context, iostreams IO, args []string) error {
	a, err := app.Open(ctx)
	if err != nil {
		fmt.Fprintf(iostreams.Stderr, "database: FAIL (%v)\n", err)
		return err
	}
	defer a.Close()

	fmt.Fprintf(iostreams.Stderr, "config dir: %s\n", a.Paths.ConfigDir)
	fmt.Fprintf(iostreams.Stderr, "data dir:   %s\n", a.Paths.DataDir)
	fmt.Fprintf(iostreams.Stderr, "cache dir:  %s\n", a.Paths.CacheDir)
	fmt.Fprintf(iostreams.Stderr, "database:   %s\n", a.Paths.DatabasePath())

	if err := a.Ping(ctx); err != nil {
		fmt.Fprintf(iostreams.Stderr, "ping: FAIL (%v)\n", err)
		return err
	}
	fmt.Fprintln(iostreams.Stderr, "ping: OK")
	fmt.Fprintln(iostreams.Stdout, "ok")
	return nil
}
