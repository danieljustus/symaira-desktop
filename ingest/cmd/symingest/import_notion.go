package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/notionimport"
)

func runNotionImport(args []string) error {
	fs := flag.NewFlagSet("import notion", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "List what would be imported without writing")
	reportPath := fs.String("report", "", "Write a JSON migration report to this path")
	importRunID := fs.String("import-run-id", "", "Use this run ID for idempotency; re-running with the same ID skips already-imported notes")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "import notion [flags] <path>", "Import a Notion Markdown + CSV export into the vault. Provide the path to the unzipped Notion export directory.")

	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs.Usage()
		return nil
	}

	help, err := parseFlags(fs, args, "invalid import notion flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		fs.Usage()
		return nil
	}

	sourceDir, err := filepath.Abs(remaining[0])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid source path")
	}

	if cfg.vault == "" && !*dryRun {
		return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
			"no vault configured; use --vault, SYMINGEST_VAULT env, or set vault in ~/.config/symingest/config.toml")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := notionimport.Options{
		SourceDir:   sourceDir,
		Vault:       cfg.vault,
		DryRun:      *dryRun,
		ImportRunID: *importRunID,
	}

	stats, err := notionimport.Run(ctx, opts)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"notion import failed")
	}

	if *dryRun {
		fmt.Fprintf(stdout, "Notion import dry-run: %d would-import, %d skipped (of %d total)\n",
			stats.Imported, stats.Skipped, stats.Total)
	} else {
		fmt.Fprintf(stdout, "Notion import complete: %d imported, %d skipped, %d failed (of %d total)\n",
			stats.Imported, stats.Skipped, stats.Failed, stats.Total)
	}
	if *reportPath != "" {
		if err := notionimport.WriteMigrationReport(*reportPath, stats.BuildMigrationReport(*dryRun)); err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
				"failed to write migration report")
		}
		fmt.Fprintf(stdout, "Migration report written to %s\n", *reportPath)
	}
	if stats.Failed > 0 {
		return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict,
			"notion import completed with %d failed item(s)", stats.Failed)
	}
	return nil
}
