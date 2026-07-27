package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/paperless"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newPaperlessCmd() *cobra.Command {
	paperlessCmd := &cobra.Command{
		Use:   "paperless",
		Short: "Paperless-ngx migration tools",
	}

	importCmd := &cobra.Command{
		Use:   "import <export-dir>",
		Short: "Import documents from a Paperless-ngx export into the vault",
		Long: `Read a Paperless-ngx export directory (manifest.json + document files) and
create or update contract-v2 notes in the vault. The import is idempotent:
re-running against the same export will update existing notes rather than
creating duplicates, keyed on the Paperless document ID and checksum.

Each document is placed in paperless/ with its original file archived in
archive/paperless/. Metadata — correspondents, document types, tags, ASN,
document date — is preserved in the note frontmatter.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			exportDir := args[0]

			// Validate export directory
			info, err := os.Stat(exportDir)
			if err != nil {
				return fmt.Errorf("export directory not found: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("export path is not a directory: %s", exportDir)
			}

			dryRun, _ := cmd.Flags().GetBool("dry-run")

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			svc := service.New(vRoot, db)
			_ = svc

			opts := paperless.ImportOptions{
				VaultRoot: vRoot,
				ExportDir: exportDir,
				DryRun:    dryRun,
				DB:        db,
			}

			summary, err := paperless.Import(opts)
			if err != nil {
				return err
			}

			if jsonFlag {
				return outputResult(summary)
			}

			fmt.Printf("Paperless import %s\n", formatDryRun(dryRun))
			fmt.Printf("  Total:   %d\n", summary.Total)
			fmt.Printf("  Created: %d\n", summary.Created)
			fmt.Printf("  Updated: %d\n", summary.Updated)
			if summary.Skipped > 0 {
				fmt.Printf("  Skipped: %d\n", summary.Skipped)
			}
			if summary.Errors > 0 {
				fmt.Printf("  Errors:  %d\n", summary.Errors)
			}

			for _, r := range summary.Results {
				switch r.Action {
				case "error":
					fmt.Fprintf(os.Stderr, "  ✗ #%d %q: %s\n", r.PaperlessID, r.Title, r.Error)
				case "created":
					fmt.Printf("  + #%d %q → %s", r.PaperlessID, r.Title, r.NotePath)
					if r.ASN > 0 {
						fmt.Printf(" (ASN %d)", r.ASN)
					}
					fmt.Println()
				case "updated":
					fmt.Printf("  ↻ #%d %q → %s", r.PaperlessID, r.Title, r.NotePath)
					if r.ASN > 0 {
						fmt.Printf(" (ASN %d)", r.ASN)
					}
					fmt.Println()
				case "skipped_idempotent":
					fmt.Printf("  = #%d %q (unchanged)\n", r.PaperlessID, r.Title)
				}
			}

			return nil
		},
	}

	importCmd.Flags().Bool("dry-run", false, "report what would happen without making changes")
	paperlessCmd.AddCommand(importCmd)

	return paperlessCmd
}

func formatDryRun(dryRun bool) string {
	if dryRun {
		return "(dry-run)"
	}
	return ""
}
