package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "ingest [flags] <file>", "Ingest a single file into the configured vault.")
	help, err := parseFlags(fs, args, "invalid ingest flags")
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

	if cfg.vault == "" {
		return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
			"no vault configured; use --vault, SYMINGEST_VAULT env, or set vault in ~/.config/symingest/config.toml")
	}

	source, err := filepath.Abs(remaining[0])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid source path")
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to open document store")
	}
	defer st.Close()

	engine := ocr.NewEngine(cfg.ocrLang, cfg.ollamaBaseURL, cfg.ollamaModel)
	pipeline := &ingest.Pipeline{
		Engine:     engine,
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: cfg.vault},
		ArchiveDir: cfg.archive,
	}
	configurePostIndex(pipeline, cfg)

	ctx := context.Background()
	res, err := pipeline.Ingest(ctx, source, nil)
	if err != nil {
		if errors.Is(err, ingest.ErrDuplicate) {
			var vPath, aPath string
			if dupErr, ok := err.(*ingest.DuplicateError); ok {
				vPath = dupErr.VaultPath
				aPath = dupErr.ArchivePath
			}
			fmt.Fprintf(stdout, "already ingested: %s\n", source)
			if vPath != "" {
				fmt.Fprintf(stdout, "existing vault path: %s\n", vPath)
			}
			if aPath != "" {
				fmt.Fprintf(stdout, "existing archive path: %s\n", aPath)
			}
			return nil
		}
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"ingestion failed")
	}

	fmt.Fprintf(stdout, "ingested: %s\nengine: %s\ntext length: %d\n",
		source, res.Extract.Engine, len(res.Extract.Text))
	return nil
}
