package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	processingDir := fs.String("processing-dir", "", "Move stable files here before enqueueing them")
	processedDir := fs.String("processed-dir", "", "Move successfully processed source files here")
	failedDir := fs.String("failed-dir", "", "Move failed source files here and write .error.json sidecars")
	stableFor := fs.Duration("stable-for", time.Second, "How long a file must remain unchanged before enqueueing")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "watch [flags] <dir>", "Watch a directory for new or modified files and ingest them in the background.")
	help, err := parseFlags(fs, args, "invalid watch flags")
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

	inboxDir, err := filepath.Abs(remaining[0])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid inbox directory path")
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to open document store")
	}
	defer st.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	lock, err := acquireWatchLock(inboxDir, cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"watcher lock refused duplicate start")
	}
	defer lock.Release()

	// Reset any running jobs to pending on startup
	if err := st.ResetRunningJobs(ctx); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindInternal,
			"failed to reset running jobs")
	}

	watcher, err := ingest.NewWatcherWithOptions(st, inboxDir, ingest.WatcherOptions{
		StableFor:     *stableFor,
		ProcessingDir: *processingDir,
		ProcessedDir:  *processedDir,
		FailedDir:     *failedDir,
	})
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindInternal,
			"failed to initialize watcher")
	}
	defer watcher.Close()

	engine := ocr.NewEngine(cfg.ocrLang, cfg.ollamaBaseURL, cfg.ollamaModel)
	pipeline := &ingest.Pipeline{
		Engine:       engine,
		Store:        st,
		Writer:       &writer.NoteWriter{Vault: cfg.vault},
		ArchiveDir:   cfg.archive,
		ProcessedDir: *processedDir,
		FailedDir:    *failedDir,
	}
	configurePostIndex(pipeline, cfg)

	if err := watcher.Start(ctx); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindInternal,
			"failed to start watcher")
	}

	var mailPoller *ingest.MailPoller
	if len(cfg.raw.IMAPAccounts) > 0 {
		pollInterval, err := time.ParseDuration(cfg.raw.IMAPPollInterval)
		if err != nil {
			return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
				"imap_poll_interval: invalid duration %q", cfg.raw.IMAPPollInterval)
		}
		mailPoller, err = ingest.NewMailPoller(st, cfg.raw.IMAPAccounts, ingest.MailPollerOptions{
			Interval:      pollInterval,
			ProcessingDir: *processingDir,
			FailedDir:     *failedDir,
		})
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindInternal, "failed to initialize mail poller")
		}
		if err := mailPoller.Start(ctx); err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindInternal, "failed to start mail poller")
		}
		defer mailPoller.Close()
	}

	go ingest.StartWorker(ctx, pipeline)

	log.Printf("Watching directory: %s", inboxDir)
	log.Printf("Vault directory:    %s", cfg.vault)
	log.Printf("Archive directory:  %s", cfg.archive)
	log.Printf("Database:           %s", cfg.db)
	log.Println("Press Ctrl+C to stop.")

	<-ctx.Done()
	log.Println("Shutting down watch command...")
	return nil
}
