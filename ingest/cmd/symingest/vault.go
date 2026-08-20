package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/vaultreview"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

func runValidateVault(args []string) error {
	fs := flag.NewFlagSet("validate-vault", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output validation failures as JSON")
	minBodyLength := fs.Int("min-body-length", 0, "Fail notes whose extracted Markdown body is shorter than this many non-whitespace bytes")
	configureUsage(fs, "validate-vault [flags] <vault>", "Validate symingest Markdown frontmatter, archive links, hashes, Paperless IDs and optional OCR/text body length gates.")
	help, err := parseFlags(fs, args, "invalid validate-vault flags")
	if help || err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "validate-vault requires a vault path")
	}
	if *minBodyLength < 0 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "min-body-length must be zero or positive")
	}
	report, err := vaultreview.ValidateVaultWithOptions(fs.Arg(0), vaultreview.ValidationOptions{MinBodyLength: *minBodyLength})
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "vault validation failed")
	}
	if *jsonFlag {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal validation report")
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintf(stdout, "Vault validation: %d files, %d failures\n", report.Files, len(report.Failures))
		for _, f := range report.Failures {
			fmt.Fprintf(stdout, "%s: %s: %s\n", f.File, f.Check, f.Message)
		}
	}
	if !report.OK() {
		return exitcodes.Wrapf(nil, exitcodes.ExitConflict, exitcodes.KindConflict, "vault validation found %d failures", len(report.Failures))
	}
	return nil
}

func correctionFromFlags(fs *flag.FlagSet, paperlessID *int, addTags, removeTags *stringList, correspondent, documentType, storagePath *string) vaultreview.Correction {
	c := vaultreview.Correction{PaperlessID: *paperlessID, AddTags: []string(*addTags), RemoveTags: []string(*removeTags)}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["correspondent"] {
		c.Correspondent = correspondent
	}
	if visited["document-type"] {
		c.DocumentType = documentType
	}
	if visited["storage-path"] {
		c.StoragePath = storagePath
	}
	return c
}

func printUpdateResults(results []vaultreview.UpdateResult) {
	for _, r := range results {
		mode := "updated"
		if r.DryRun {
			mode = "would update"
		} else if !r.Written {
			mode = "unchanged"
		}
		backup := ""
		if r.BackupPath != "" {
			backup = " backup=" + r.BackupPath
		}
		fmt.Fprintf(stdout, "%s: paperless_id=%d %s (%s)%s\n", mode, r.PaperlessID, r.File, strings.Join(r.Changes, ", "), backup)
	}
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	vault := fs.String("vault", "", "Vault path")
	paperlessID := fs.Int("paperless-id", 0, "Paperless document ID to update")
	dryRun := fs.Bool("dry-run", false, "Show exact frontmatter changes without writing")
	var addTags, removeTags stringList
	fs.Var(&addTags, "add-tag", "Tag to add (repeatable or comma-separated)")
	fs.Var(&removeTags, "remove-tag", "Tag to remove (repeatable or comma-separated; inbox is protected)")
	correspondent := fs.String("correspondent", "", "Set correspondent")
	documentType := fs.String("document-type", "", "Set document type")
	storagePath := fs.String("storage-path", "", "Set Paperless storage path metadata")
	configureUsage(fs, "update [flags]", "Safely update one note frontmatter by Paperless ID.")
	help, err := parseFlags(fs, args, "invalid update flags")
	if help || err != nil {
		return err
	}
	if *vault == "" {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "--vault is required")
	}
	res, err := vaultreview.ApplyCorrection(*vault, correctionFromFlags(fs, paperlessID, &addTags, &removeTags, correspondent, documentType, storagePath), *dryRun)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "update failed")
	}
	printUpdateResults([]vaultreview.UpdateResult{*res})
	return nil
}

func runBulkUpdate(args []string) error {
	fs := flag.NewFlagSet("bulk-update", flag.ContinueOnError)
	vault := fs.String("vault", "", "Vault path")
	where := fs.String("where", "", "Selector, currently tag:<name>")
	dryRun := fs.Bool("dry-run", false, "Show exact frontmatter changes without writing")
	maxUpdates := fs.Int("max", 0, "Refuse when matched corrections exceed this count; 0 disables")
	requireCount := fs.Int("require-count", 0, "Refuse unless exactly this many notes match; 0 disables")
	backupDir := fs.String("backup-dir", "", "Directory for undo backups before writes; default .symingest-backups next to each note")
	paperlessID := fs.Int("paperless-id", 0, "Ignored for bulk updates")
	var addTags, removeTags stringList
	fs.Var(&addTags, "add-tag", "Tag to add")
	fs.Var(&removeTags, "remove-tag", "Tag to remove; inbox is protected")
	correspondent := fs.String("correspondent", "", "Set correspondent")
	documentType := fs.String("document-type", "", "Set document type")
	storagePath := fs.String("storage-path", "", "Set Paperless storage path metadata")
	configureUsage(fs, "bulk-update [flags]", "Safely update multiple notes selected by frontmatter.")
	help, err := parseFlags(fs, args, "invalid bulk-update flags")
	if help || err != nil {
		return err
	}
	if *vault == "" || !strings.HasPrefix(*where, "tag:") {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "--vault and --where tag:<name> are required")
	}
	results, err := vaultreview.BulkUpdateByTagWithOptions(*vault, strings.TrimPrefix(*where, "tag:"), correctionFromFlags(fs, paperlessID, &addTags, &removeTags, correspondent, documentType, storagePath), vaultreview.BulkUpdateOptions{DryRun: *dryRun, Max: *maxUpdates, RequireCount: *requireCount, BackupDir: *backupDir})
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "bulk update failed")
	}
	printUpdateResults(results)
	return nil
}

func runApplyCorrections(args []string) error {
	fs := flag.NewFlagSet("apply-corrections", flag.ContinueOnError)
	vault := fs.String("vault", "", "Vault path")
	dryRun := fs.Bool("dry-run", false, "Show exact frontmatter changes without writing")
	maxUpdates := fs.Int("max", 0, "Refuse when corrections exceed this count; 0 disables")
	requireCount := fs.Int("require-count", 0, "Refuse unless corrections.yaml contains exactly this many entries; 0 disables")
	backupDir := fs.String("backup-dir", "", "Directory for undo backups before writes; default .symingest-backups next to each note")
	configureUsage(fs, "apply-corrections [flags] <corrections.yaml>", "Apply YAML corrections keyed by paperless_id.")
	help, err := parseFlags(fs, args, "invalid apply-corrections flags")
	if help || err != nil {
		return err
	}
	if *vault == "" || fs.NArg() != 1 {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "--vault and corrections.yaml are required")
	}
	results, err := vaultreview.ApplyCorrectionsFileWithOptions(*vault, fs.Arg(0), vaultreview.ApplyOptions{DryRun: *dryRun, Max: *maxUpdates, RequireCount: *requireCount, BackupDir: *backupDir})
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "apply corrections failed")
	}
	printUpdateResults(results)
	return nil
}
