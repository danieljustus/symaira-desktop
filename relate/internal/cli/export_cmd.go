package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/danieljustus/symaira-relate/internal/app"
)

func init() {
	Register(&Command{
		Name:  "export",
		Short: "Export contacts as JSON or CSV",
		Run:   runExport,
	})
}

func runExport(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate export <json|csv> --out <file>")
	}
	verb, rest := args[0], args[1:]

	fs := flag.NewFlagSet("export "+verb, flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	out := fs.String("out", "", "output file path (required)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("usage: symrelate export %s --out <file>", verb)
	}

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	f, err := createSecureFile(*out)
	if err != nil {
		return fmt.Errorf("failed to create export file: %w", err)
	}
	defer f.Close()

	switch verb {
	case "json":
		err = a.Security.ExportJSON(ctx, f)
	case "csv":
		err = a.Security.ExportCSV(ctx, f)
	default:
		return fmt.Errorf("symrelate export: unknown format %q (want json or csv)", verb)
	}
	if err != nil {
		os.Remove(*out)
		return err
	}
	fmt.Fprintf(iostreams.Stdout, "exported to %s\n", *out)
	return nil
}
