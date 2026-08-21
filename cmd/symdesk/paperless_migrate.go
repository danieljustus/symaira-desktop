package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	symingestconfig "github.com/danieljustus/symaira-desktop/internal/symingest/config"
	"github.com/danieljustus/symaira-desktop/internal/symingest/ingest"
	"github.com/danieljustus/symaira-desktop/internal/symingest/ocr"
	"github.com/danieljustus/symaira-desktop/internal/symingest/paperlessimport"
	"github.com/danieljustus/symaira-desktop/internal/symingest/store"
	"github.com/danieljustus/symaira-desktop/internal/symingest/writer"
)

// newPaperlessMigrateCmd imports documents from a live Paperless-ngx instance
// through its API, using the absorbed symingest pipeline (OCR + store +
// writer). It complements the export-based `paperless import` command: this is
// the direct API migration path (repo consolidation step 3b).
func newPaperlessMigrateCmd() *cobra.Command {
	var (
		baseURL     string
		token       string
		sinceStr    string
		dryRun      bool
		plan        bool
		resume      bool
		deepVerify  bool
		retryFailed bool
		concurrency int
		limit       int
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Import documents from a Paperless-ngx instance via its API",
		Long: `Connect to a running Paperless-ngx instance (--base-url + --token) and
import its documents into the vault, running them through the symingest OCR
pipeline and writing Markdown + YAML frontmatter notes. Idempotent: re-running
updates existing notes keyed on the Paperless document ID and checksum.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			if baseURL == "" {
				return fmt.Errorf("--base-url is required")
			}
			if token == "" {
				return fmt.Errorf("--token is required")
			}

			cfg, err := symingestconfig.Load()
			if err != nil {
				return fmt.Errorf("load symingest config: %w", err)
			}

			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return fmt.Errorf("open document store: %w", err)
			}
			defer st.Close()

			engine := ocr.NewEngine(cfg.OCRLang, cfg.OllamaBaseURL, cfg.OllamaModel)
			pipeline := &ingest.Pipeline{
				Engine:     engine,
				Store:      st,
				Writer:     &writer.NoteWriter{Vault: cfg.Vault},
				ArchiveDir: cfg.ArchivePath,
			}

			var since time.Time
			if sinceStr != "" {
				since, err = time.Parse(time.RFC3339, sinceStr)
				if err != nil {
					return fmt.Errorf("invalid --since (want RFC3339): %w", err)
				}
			}

			opts := paperlessimport.Options{
				BaseURL:       baseURL,
				Token:         token,
				Since:         since,
				DryRun:        dryRun,
				Plan:          plan,
				Resume:        resume,
				DeepVerify:    deepVerify,
				RetryFailed:   retryFailed,
				Concurrency:   concurrency,
				Limit:         limit,
				TargetVault:   cfg.Vault,
				TargetArchive: cfg.ArchivePath,
			}

			stats, err := paperlessimport.Run(ctx, opts, pipeline)
			if err != nil {
				return fmt.Errorf("paperless migration failed: %w", err)
			}

			if jsonFlag {
				return outputResult(stats)
			}

			fmt.Printf("Paperless migration %s\n", formatDryRun(dryRun))
			fmt.Printf("  Total:    %d\n", stats.Total)
			fmt.Printf("  Imported: %d\n", stats.Imported)
			fmt.Printf("  Skipped:  %d\n", stats.Skipped)
			fmt.Printf("  Failed:   %d\n", stats.Failed)
			for _, w := range stats.Warnings {
				fmt.Printf("  warning:  %s\n", w)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&baseURL, "base-url", "", "Paperless-ngx base URL (required)")
	cmd.Flags().StringVar(&token, "token", "", "Paperless-ngx API token (required)")
	cmd.Flags().StringVar(&sinceStr, "since", "", "only import documents modified since this RFC3339 timestamp")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would happen without writing")
	cmd.Flags().BoolVar(&plan, "plan", false, "print the import plan and exit")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume from the last checkpoint")
	cmd.Flags().BoolVar(&deepVerify, "deep-verify", false, "re-download originals and compare SHA-256 (slow)")
	cmd.Flags().BoolVar(&retryFailed, "retry-failed", false, "only retry documents previously recorded as failed")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "concurrent document processing (default 1)")
	cmd.Flags().IntVar(&limit, "limit", 0, "limit the number of documents (0 = all)")

	return cmd
}
