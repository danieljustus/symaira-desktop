package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/secret"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func runImport(args []string) error {
	fs := flag.NewFlagSet("import paperless", flag.ContinueOnError)
	baseURL := fs.String("base-url", "", "Paperless-ngx instance URL (or PAPERLESS_URL env)")
	token := fs.String("token", "", "API token, or PAPERLESS_TOKEN env; also accepts keychain://service/account or symvault://ref")
	sinceStr := fs.String("since", "", "Only import documents whose Paperless created date is on or after this date (YYYY-MM-DD)")
	limit := fs.Int("limit", 0, "Import at most N documents (newest first); 0 means no limit")
	idsStr := fs.String("ids", "", "Import only these Paperless document IDs (comma-separated); takes precedence over --since and --limit")
	preserveStoragePaths := fs.Bool("preserve-storage-paths", false, "Place notes under vault subdirectories derived from each document's Paperless storage path")
	dryRun := fs.Bool("dry-run", false, "List what would be imported without writing")
	plan := fs.Bool("plan", false, "Plan a Paperless import without downloading document bodies or writing vault/archive/import-state")
	resume := fs.Bool("resume", false, "Resume an interrupted import by skipping documents already imported for this target")
	retryFailed := fs.Bool("retry-failed", false, "Retry only documents recorded as failed for this target")
	concurrency := fs.Int("concurrency", 1, "Maximum number of Paperless documents to process concurrently")
	checkpointEvery := fs.Int("checkpoint-every", 0, "Print a progress checkpoint after every N processed documents; 0 disables checkpoints")
	reportPath := fs.String("report", "", "Write a JSON migration report to this path (works with --plan, --dry-run and real imports)")
	verify := fs.Bool("verify", false, "Verify a completed import against the Paperless source instead of importing")
	deepVerify := fs.Bool("deep", false, "With --verify, re-download each Paperless original and compare SHA-256 against the archive")
	statusOnly := fs.Bool("status", false, "List per-document import status from a previous run, then exit")
	statusSummary := fs.Bool("summary", false, "With --status, print counts by import status")
	statusFailed := fs.Bool("failed", false, "With --status, show only failed documents")
	jsonFlag := fs.Bool("json", false, "With --status or --verify, output the result as JSON")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "import paperless [flags]", "Import documents from a Paperless-ngx instance into the vault. Use --plan, --dry-run, --limit, or --ids to run a small, inspectable pilot before a full migration; bounds apply to --plan, --dry-run and real imports alike. Imports are resumable: a document already recorded as imported is skipped on a re-run, and a document that previously failed is retried automatically.")

	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs.Usage()
		return nil
	}

	if args[0] == "notion" {
		return runNotionImport(args[1:])
	}

	if args[0] != "paperless" {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation,
			"unknown import subcommand %q; supported: paperless, notion", args[0])
	}

	help, err := parseFlags(fs, args[1:], "invalid import flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}

	if *baseURL == "" {
		*baseURL = os.Getenv("PAPERLESS_URL")
	}
	if *baseURL == "" {
		*baseURL = cfg.paperlessBaseURL
	}
	if *token == "" {
		*token = os.Getenv("PAPERLESS_TOKEN")
	}
	if *baseURL == "" {
		return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
			"base-url is required (use --base-url, PAPERLESS_URL env, or paperless_base_url in config)")
	}
	if *token == "" && !*statusOnly {
		return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
			"token is required (use --token or the PAPERLESS_TOKEN env var)")
	}

	if *plan && *dryRun {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"--plan and --dry-run are mutually exclusive")
	}
	if *retryFailed && (*plan || *dryRun) {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"--retry-failed is only valid for real imports")
	}
	if *deepVerify && !*verify {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"--deep is only valid with --verify")
	}
	if *concurrency < 1 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid concurrency %d; must be at least 1", *concurrency)
	}
	if *checkpointEvery < 0 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid checkpoint interval %d; must be zero or positive", *checkpointEvery)
	}

	if cfg.vault == "" && !*statusOnly && !*plan && !*dryRun {
		return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
			"no vault configured; use --vault, SYMINGEST_VAULT env, or set vault in ~/.config/symingest/config.toml")
	}

	if *statusOnly {
		st, err := store.Open(cfg.db)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
				"failed to open document store")
		}
		defer st.Close()

		statusFilter := ""
		if *statusFailed {
			statusFilter = "failed"
		}
		states, err := st.ListPaperlessImportState(context.Background(), *baseURL, statusFilter)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
				"failed to list paperless import status")
		}

		if *statusSummary {
			summary := buildPaperlessStatusSummary(*baseURL, states)
			if *jsonFlag {
				data, err := json.MarshalIndent(summary, "", "  ")
				if err != nil {
					return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
						"failed to marshal import status summary to JSON")
				}
				fmt.Fprintln(stdout, string(data))
				return nil
			}
			fmt.Fprintf(stdout, "Paperless import status for %s: total=%d imported=%d skipped=%d failed=%d pending=%d\n",
				summary.BaseURL, summary.Total, summary.Imported, summary.Skipped, summary.Failed, summary.Pending)
			return nil
		}

		if *jsonFlag {
			if states == nil {
				fmt.Fprintln(stdout, "[]")
				return nil
			}
			data, err := json.MarshalIndent(states, "", "  ")
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
					"failed to marshal import status to JSON")
			}
			fmt.Fprintln(stdout, string(data))
			return nil
		}

		if len(states) == 0 {
			fmt.Fprintf(stdout, "No recorded import status for %s\n", *baseURL)
			return nil
		}
		for _, s := range states {
			if s.LastError != "" {
				fmt.Fprintf(stdout, "document %d: %s (%s)\n", s.PaperlessDocumentID, s.Status, s.LastError)
			} else {
				fmt.Fprintf(stdout, "document %d: %s\n", s.PaperlessDocumentID, s.Status)
			}
		}
		return nil
	}

	var since time.Time
	if *sinceStr != "" {
		since, err = time.Parse("2006-01-02", *sinceStr)
		if err != nil {
			return exitcodes.Wrapf(err, exitcodes.ExitData, exitcodes.KindValidation,
				"invalid since date %q; expected YYYY-MM-DD", *sinceStr)
		}
	}

	if *limit < 0 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid limit %d; must be zero or positive", *limit)
	}

	ids, err := parseDocumentIDs(*idsStr)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid ids")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *token != "" {
		resolvedToken, err := secret.Resolve(ctx, *token)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
				"failed to resolve --token")
		}
		*token = resolvedToken
	}

	var st *store.Store
	{
		var err error
		st, err = store.Open(cfg.db)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
				"failed to open document store")
		}
		defer st.Close()
	}

	if *verify {
		report, err := paperlessimport.Verify(ctx, paperlessimport.Options{BaseURL: *baseURL, Token: *token, Since: since, Limit: *limit, IDs: ids, DeepVerify: *deepVerify, TargetVault: cfg.vault, TargetArchive: cfg.archive}, cfg.vault, st)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
				"verification failed")
		}
		if *jsonFlag {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
					"failed to marshal verification report to JSON")
			}
			fmt.Fprintln(stdout, string(data))
		} else {
			printVerifyReport(stdout, report)
		}
		if !report.Complete() {
			return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict,
				"migration verification found discrepancies: %d missing, %d duplicate, %d missing-archive, %d hash-mismatch, %d source-hash-mismatch, %d mismatched",
				len(report.Missing), len(report.Duplicate), len(report.MissingArchive), len(report.HashMismatch), len(report.SourceHashMismatch), len(report.Mismatches))
		}
		return nil
	}

	engine := ocr.NewEngine(cfg.ocrLang, cfg.ollamaBaseURL, cfg.ollamaModel)
	pipeline := &ingest.Pipeline{
		Engine:     engine,
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: cfg.vault},
		ArchiveDir: cfg.archive,
	}
	configurePostIndex(pipeline, cfg)

	opts := paperlessimport.Options{
		BaseURL:              *baseURL,
		Token:                *token,
		Since:                since,
		DryRun:               *dryRun,
		Plan:                 *plan,
		Resume:               *resume,
		RetryFailed:          *retryFailed,
		Concurrency:          *concurrency,
		CheckpointEvery:      *checkpointEvery,
		Limit:                *limit,
		IDs:                  ids,
		PreserveStoragePaths: *preserveStoragePaths,
		TargetVault:          cfg.vault,
		TargetArchive:        cfg.archive,
	}

	stats, err := paperlessimport.Run(ctx, opts, pipeline)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"import failed")
	}

	if *plan {
		fmt.Fprintf(stdout, "Import plan complete: %d documents analyzed, %d unsupported type groups\n", stats.Total, len(stats.Audit.UnsupportedFileTypes))
	} else {
		fmt.Fprintf(stdout, "Import complete: %d imported, %d skipped, %d failed (of %d total)\n",
			stats.Imported, stats.Skipped, stats.Failed, stats.Total)
	}
	// For a bounded pilot run, echo exactly which documents were selected so
	// the operator can inspect them. Document content is never printed.
	if (*limit > 0 || len(ids) > 0) && len(stats.SelectedIDs) > 0 {
		fmt.Fprintf(stdout, "Selected documentIDs: %s\n", joinInts(stats.SelectedIDs))
	}
	if *reportPath != "" {
		if err := paperlessimport.WriteMigrationReport(*reportPath, stats.BuildMigrationReport(*dryRun || *plan)); err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
				"failed to write migration report")
		}
		fmt.Fprintf(stdout, "Migration report written to %s\n", *reportPath)
	}
	if stats.Failed > 0 {
		fmt.Fprintf(stdout, "Re-run the same command to retry failed documents; use --status to inspect them.\n")
		return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict,
			"import completed with %d failed document(s)", stats.Failed)
	}
	return nil
}

type paperlessStatusSummary struct {
	BaseURL  string `json:"base_url"`
	Total    int    `json:"total"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
	Pending  int    `json:"pending"`
}

func buildPaperlessStatusSummary(baseURL string, states []*store.PaperlessImportState) paperlessStatusSummary {
	s := paperlessStatusSummary{BaseURL: baseURL, Total: len(states)}
	for _, st := range states {
		switch st.Status {
		case "imported":
			s.Imported++
		case "skipped":
			s.Skipped++
		case "failed":
			s.Failed++
		case "pending":
			s.Pending++
		}
	}
	return s
}

func printVerifyReport(w io.Writer, r *paperlessimport.VerifyReport) {
	fmt.Fprintf(w, "Migration verification: %d source documents, %d vault notes, %d verified\n",
		r.SourceDocuments, r.VaultNotes, r.Verified)
	if r.DeepVerify {
		fmt.Fprintf(w, "  deep verify: %d Paperless downloads matched archived originals\n", r.DeepVerified)
	}
	if len(r.Missing) > 0 {
		fmt.Fprintf(w, "  missing from vault (%d): %s\n", len(r.Missing), joinInts(r.Missing))
	}
	if len(r.Duplicate) > 0 {
		fmt.Fprintf(w, "  duplicate notes (%d): %s\n", len(r.Duplicate), joinInts(r.Duplicate))
	}
	if len(r.MissingArchive) > 0 {
		fmt.Fprintf(w, "  missing archived original (%d): %s\n", len(r.MissingArchive), joinInts(r.MissingArchive))
	}
	if len(r.HashMismatch) > 0 {
		fmt.Fprintf(w, "  local archive hash mismatches (%d): %s\n", len(r.HashMismatch), joinInts(r.HashMismatch))
	}
	if len(r.SourceHashMismatch) > 0 {
		fmt.Fprintf(w, "  Paperless download hash mismatches (%d): %s\n", len(r.SourceHashMismatch), joinInts(r.SourceHashMismatch))
	}
	if len(r.Mismatches) > 0 {
		fmt.Fprintf(w, "  metadata mismatches (%d):\n", len(r.Mismatches))
		for _, m := range r.Mismatches {
			fmt.Fprintf(w, "    document %d: %s expected %q, got %q\n", m.DocumentID, m.Field, m.Expected, m.Got)
		}
	}
	if r.Complete() {
		fmt.Fprintln(w, "  OK: vault matches the Paperless source")
	}
}

// parseDocumentIDs parses a comma-separated list of Paperless document IDs,
// tolerating surrounding whitespace and empty entries. Each ID must be a
// positive integer.
func parseDocumentIDs(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var ids []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("document ID %q is not a number", part)
		}
		if id <= 0 {
			return nil, fmt.Errorf("document ID %d must be positive", id)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// joinInts renders a slice of ints as a comma-separated string.
func joinInts(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
