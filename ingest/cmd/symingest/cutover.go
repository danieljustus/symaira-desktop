package main

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/vaultreview"
)

func runCutoverCheck(args []string) error {
	fs := flag.NewFlagSet("cutover-check", flag.ContinueOnError)
	dryRunReport := fs.String("dry-run-report", "", "Full dry-run JSON report produced by 'symingest import paperless --dry-run --report'")
	importReport := fs.String("import-report", "", "Full real-import JSON report produced by 'symingest import paperless --report'")
	verifyReport := fs.String("verify-report", "", "Verifier JSON report produced by 'symingest import paperless --verify --json'")
	searchReport := fs.String("search-report", "", "Search validation JSON report produced by 'symingest search validate --report'")
	vault := fs.String("vault", "", "Vault path to validate")
	minDocuments := fs.Int("min-documents", 0, "Minimum source document count expected before cutover")
	minBodyLength := fs.Int("min-body-length", 0, "Fail cutover if any note body is shorter than this many non-whitespace bytes")
	jsonFlag := fs.Bool("json", false, "Output the cutover report as JSON")
	configureUsage(fs, "cutover-check [flags]", "Gate whether Paperless-ngx can stop being the source of truth. Requires the dry-run, real import, verify, and vault-validation evidence from the replacement runbook.")
	help, err := parseFlags(fs, args, "invalid cutover-check flags")
	if help || err != nil {
		return err
	}
	report, err := vaultreview.BuildCutoverReport(vaultreview.CutoverOptions{
		DryRunReportPath: *dryRunReport,
		ImportReportPath: *importReport,
		VerifyReportPath: *verifyReport,
		SearchReportPath: *searchReport,
		VaultPath:        *vault,
		MinDocuments:     *minDocuments,
		MinBodyLength:    *minBodyLength,
	})
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "cutover check failed")
	}
	if *jsonFlag {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal cutover report")
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		status := "BLOCKED"
		if report.Ready {
			status = "READY"
		}
		fmt.Fprintf(stdout, "Paperless replacement cutover: %s\n", status)
		for _, c := range report.Checks {
			fmt.Fprintf(stdout, "[%s] %s: %s\n", c.Status, c.Name, c.Message)
		}
	}
	if !report.Ready {
		return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict,
			"cutover blocked by %d issue(s)", len(report.Blockers))
	}
	return nil
}
