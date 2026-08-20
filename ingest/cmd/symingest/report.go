package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/vaultreview"
	symseekint "github.com/danieljustus/symaira-ingest/internal/symseek"
)

func runReviewReport(args []string) error {
	fs := flag.NewFlagSet("review-report", flag.ContinueOnError)
	htmlPath := fs.String("html", "", "Write a local HTML review report")
	jsonFlag := fs.Bool("json", false, "Output filtered review rows as JSON")
	failed := fs.Bool("failed", false, "Show failed documents")
	warningsOnly := fs.Bool("warnings", false, "Show documents with warnings or errors")
	missingMetadata := fs.Bool("missing-metadata", false, "Show documents missing key metadata paths/MIME")
	lowBody := fs.Bool("low-body", false, "Show documents with low/short body warnings")
	duplicateContent := fs.Bool("duplicate-content", false, "Show verify findings for duplicate original bytes across Paperless IDs")
	unsupported := fs.Bool("unsupported", false, "Show unsupported format findings")
	unresolved := fs.Bool("unresolved", false, "Show unresolved metadata reference findings")
	configureUsage(fs, "review-report [flags] <migration-or-verify-report.json>", "Generate a human-reviewable migration/verify report without document body text.")
	help, err := parseFlags(fs, args, "invalid review-report flags")
	if help || err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "review-report requires migration-or-verify-report.json")
	}
	report, err := vaultreview.BuildReviewReport(fs.Arg(0), vaultreview.ReviewFilters{Failed: *failed, Warnings: *warningsOnly, MissingMetadata: *missingMetadata, LowBody: *lowBody, DuplicateContent: *duplicateContent, Unsupported: *unsupported, Unresolved: *unresolved})
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "review report failed")
	}
	if *htmlPath != "" {
		if err := vaultreview.WriteReviewHTML(*htmlPath, report); err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to write HTML review report")
		}
		fmt.Fprintf(stdout, "Review HTML written to %s\n", *htmlPath)
	}
	if *jsonFlag {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal review report")
		}
		fmt.Fprintln(stdout, string(data))
		return nil
	}
	for _, d := range report.Documents {
		fmt.Fprintf(stdout, "document %d: %s mime=%s vault=%s archive=%s error=%s warnings=%d\n", d.ID, d.Status, d.MIME, d.VaultPath, d.ArchivePath, d.Error, len(d.Warnings))
	}
	return nil
}

type reportValidationResult struct {
	Path          string   `json:"path"`
	Kind          string   `json:"kind"`
	SchemaVersion int      `json:"schema_version"`
	ToolVersion   string   `json:"tool_version,omitempty"`
	Valid         bool     `json:"valid"`
	Errors        []string `json:"errors,omitempty"`
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output validation result as JSON")
	configureUsage(fs, "report [flags] validate <report.json>", "Validate a machine-readable migration, verify, or cutover JSON report.")
	help, err := parseFlags(fs, args, "invalid report flags")
	if help || err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) != 2 || remaining[0] != "validate" {
		fs.Usage()
		return nil
	}
	result := validateReportFile(remaining[1])
	if *jsonFlag {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal report validation")
		}
		fmt.Fprintln(stdout, string(data))
	} else if result.Valid {
		fmt.Fprintf(stdout, "report valid: %s (%s schema_version=%d)\n", result.Path, result.Kind, result.SchemaVersion)
	} else {
		fmt.Fprintf(stdout, "report invalid: %s\n", result.Path)
		for _, e := range result.Errors {
			fmt.Fprintf(stdout, "- %s\n", e)
		}
	}
	if !result.Valid {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "report validation failed")
	}
	return nil
}

func validateReportFile(path string) reportValidationResult {
	result := reportValidationResult{Path: path, Valid: true}
	data, err := os.ReadFile(path)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, "invalid JSON: "+err.Error())
		return result
	}
	if v, ok := raw["schema_version"].(float64); ok {
		result.SchemaVersion = int(v)
	}
	if v, ok := raw["tool_version"].(string); ok {
		result.ToolVersion = v
	}
	switch {
	case raw["ready"] != nil && raw["checks"] != nil:
		result.Kind = "cutover"
		var report vaultreview.CutoverReport
		if err := json.Unmarshal(data, &report); err != nil {
			result.Errors = append(result.Errors, "invalid cutover report: "+err.Error())
		}
	case raw["ok"] != nil && raw["checks"] != nil && raw["passed"] != nil && raw["failed"] != nil:
		result.Kind = "search"
		var report symseekint.ValidationReport
		if err := json.Unmarshal(data, &report); err != nil {
			result.Errors = append(result.Errors, "invalid search validation report: "+err.Error())
		}
	case raw["source_documents"] != nil && raw["verified"] != nil:
		result.Kind = "verify"
		var report paperlessimport.VerifyReport
		if err := json.Unmarshal(data, &report); err != nil {
			result.Errors = append(result.Errors, "invalid verify report: "+err.Error())
		}
	case raw["documents"] != nil && raw["total"] != nil:
		result.Kind = "migration"
		var report paperlessimport.MigrationReport
		if err := json.Unmarshal(data, &report); err != nil {
			result.Errors = append(result.Errors, "invalid migration report: "+err.Error())
		}
	default:
		result.Errors = append(result.Errors, "unknown report kind")
	}
	if result.SchemaVersion != paperlessimport.ReportSchemaVersion {
		result.Errors = append(result.Errors, fmt.Sprintf("schema_version=%d; expected %d", result.SchemaVersion, paperlessimport.ReportSchemaVersion))
	}
	if result.Kind != "cutover" && result.ToolVersion == "" {
		result.Errors = append(result.Errors, "missing tool_version")
	}
	if len(result.Errors) > 0 {
		result.Valid = false
	}
	return result
}
