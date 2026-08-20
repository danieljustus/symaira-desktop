package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"

	"github.com/danieljustus/symaira-ingest/internal/ingest"
	symseekint "github.com/danieljustus/symaira-ingest/internal/symseek"
	"github.com/danieljustus/symaira-ingest/internal/version"
)

func configurePostIndex(pipeline *ingest.Pipeline, cfg *resolvedConfig) {
	if pipeline == nil || !cfg.symseekEnabled {
		return
	}
	client := symseekint.Client{Binary: cfg.symseekBinary, Timeout: 2 * time.Minute}
	pipeline.PostIndex = func(ctx context.Context, path string) error {
		res := client.Index(ctx, path)
		if !res.OK {
			return fmt.Errorf("%s", res.Error)
		}
		log.Printf("symseek indexed generated note: %s", path)
		return nil
	}
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fixturesPath := fs.String("fixtures", "", "JSON fixture file for 'validate': [{query,min_results,must_contain,limit}]")
	reportPath := fs.String("report", "", "Write search validation report JSON to this path")
	limit := fs.Int("limit", 5, "Default search result limit for validation fixtures")
	home := fs.String("home", "", "Override HOME for symseek, useful for isolated test indexes")
	symseekBinary := fs.String("symseek-binary", "", "Path to symseek binary; defaults to PATH lookup or config")
	jsonFlag := fs.Bool("json", false, "Output machine-readable JSON")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "search [flags] <index|validate> [path]", "Index the vault with symseek and validate search fixtures. Flags must appear before the subcommand.")
	help, err := parseFlags(fs, args, "invalid search flags")
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
	binary := strings.TrimSpace(*symseekBinary)
	if binary == "" {
		binary = cfg.symseekBinary
	}
	client := symseekint.Client{Binary: binary, Timeout: 5 * time.Minute, Home: strings.TrimSpace(*home)}
	ctx := context.Background()

	switch remaining[0] {
	case "index":
		target := strings.TrimSpace(cfg.vault)
		if len(remaining) > 1 {
			target = remaining[1]
		}
		if target == "" {
			return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
				"no vault configured; use --vault, SYMINGEST_VAULT env, or pass an index path")
		}
		res := client.Index(ctx, target)
		if *jsonFlag {
			data, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal symseek index result")
			}
			fmt.Fprintln(stdout, string(data))
		} else if res.OK {
			fmt.Fprintf(stdout, "symseek indexed: %s\n", target)
			if res.Output != "" {
				fmt.Fprintln(stdout, res.Output)
			}
		} else {
			fmt.Fprintf(stdout, "symseek index failed: %s\n", res.Error)
		}
		if !res.OK {
			return exitcodes.Wrapf(nil, exitcodes.ExitSoftware, exitcodes.KindInternal, "symseek index failed")
		}
		return nil

	case "validate":
		if *fixturesPath == "" {
			return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "search validate requires --fixtures")
		}
		fixtures, err := symseekint.LoadFixtures(*fixturesPath)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "failed to load search fixtures")
		}
		report := client.Validate(ctx, fixtures, *limit, version.Version)
		if *reportPath != "" {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal search validation report")
			}
			if err := os.WriteFile(*reportPath, data, 0o600); err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to write search validation report")
			}
		}
		if *jsonFlag || *reportPath == "" {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal search validation report")
			}
			fmt.Fprintln(stdout, string(data))
		} else {
			fmt.Fprintf(stdout, "search validation: %d passed, %d failed (report: %s)\n", report.Passed, report.Failed, *reportPath)
		}
		if !report.OK {
			return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict, "search validation failed: %d failed fixture(s)", report.Failed)
		}
		return nil
	default:
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "unknown search command %q", remaining[0])
	}
}
