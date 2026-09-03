package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/health"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newVaultAdoptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt [<dir>]",
		Short: "Bring an existing Markdown or Obsidian vault up to the contract in place",
		Long: `Scan an existing Markdown vault and backfill required frontmatter fields
(title, created, tags) in place. Existing frontmatter keys and unknown fields
are preserved byte-for-byte. Pre-adoption versions are captured in the version
history safety net (.symdesk/history/) before any file is written.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, err := cmd.Flags().GetBool("dry-run")
			if err != nil {
				return err
			}

			flagPath := ""
			if len(args) > 0 {
				flagPath = args[0]
			}
			vRoot, err := vault.ResolveVaultRoot(flagPath, cfg)
			if err != nil {
				return err
			}

			var db *sidecar.DB
			if !dryRun {
				d, err := sidecar.OpenForVault(vRoot)
				if err == nil {
					db = d
					defer closeWithWarning("sidecar database", db.Close)
				}
			}

			report, err := health.Adopt(health.AdoptOptions{
				VaultRoot: vRoot,
				DryRun:    dryRun,
				DB:        db,
			})
			if err != nil {
				return err
			}

			if jsonFlag {
				return outputResult(report)
			}

			return printAdoptReport(report)
		},
	}

	cmd.Flags().Bool("dry-run", false, "list what would change without modifying files on disk")
	return cmd
}

func printAdoptReport(report *health.AdoptReport) error {
	modeStr := ""
	if report.DryRun {
		modeStr = " (dry-run)"
	}
	fmt.Printf("Vault adoption%s\n", modeStr)
	fmt.Printf("  Vault:    %s\n", report.Vault)
	fmt.Printf("  Total:    %d\n", report.Total)
	fmt.Printf("  Adopted:  %d\n", report.Adopted)
	fmt.Printf("  Skipped:  %d\n", report.Skipped)
	fmt.Printf("  Failed:   %d\n", report.Failed)

	if len(report.Documents) > 0 {
		hasChanges := false
		for _, doc := range report.Documents {
			switch doc.Status {
			case "adopted":
				if !hasChanges {
					fmt.Println("\nChanges:")
					hasChanges = true
				}
				fmt.Printf("  %s:\n", doc.Path)
				if doc.Title != "" {
					fmt.Printf("    + title: %q\n", doc.Title)
				}
				if doc.Created != "" {
					fmt.Printf("    + created: %q\n", doc.Created)
				}
				if len(doc.Tags) == 0 {
					fmt.Printf("    + tags: []\n")
				} else {
					fmt.Printf("    + tags: %v\n", doc.Tags)
				}
			case "failed":
				fmt.Printf("  %s: error: %s\n", doc.Path, doc.Error)
			}
		}
	}

	for _, w := range report.Warnings {
		fmt.Printf("  warning:  %s\n", w)
	}

	return nil
}
