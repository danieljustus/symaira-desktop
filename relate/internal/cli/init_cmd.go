package cli

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/app"
)

func init() {
	Register(&Command{
		Name:  "init",
		Short: "Create the local profile directories and database",
		Run:   runInit,
	})
}

func runInit(ctx context.Context, iostreams IO, args []string) error {
	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	fmt.Fprintf(iostreams.Stdout, "config dir: %s\n", a.Paths.ConfigDir)
	fmt.Fprintf(iostreams.Stdout, "data dir:   %s\n", a.Paths.DataDir)
	fmt.Fprintf(iostreams.Stdout, "cache dir:  %s\n", a.Paths.CacheDir)
	fmt.Fprintf(iostreams.Stdout, "database:   %s\n", a.Paths.DatabasePath())
	return nil
}
