package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// sidecarReportEntry adds the sibling per-vault retrieval index to each
// sidecar entry: retrieval.db lives right next to sidecar.db under the same
// <SidecarRoot>/<hash> directory (#756), so it can be derived directly from
// the sidecar's own path without opening the retrieval database.
type sidecarReportEntry struct {
	sidecar.SidecarEntry
	RetrievalIndexPath    string `json:"retrieval_index_path"`
	RetrievalIndexPresent bool   `json:"retrieval_index_present"`
}

type sidecarReport struct {
	Sidecars                    []sidecarReportEntry `json:"sidecars"`
	Count                       int                  `json:"count"`
	TotalBytes                  int64                `json:"total_bytes"`
	OrphanCount                 int                  `json:"orphan_count"`
	LegacyPath                  string               `json:"legacy_path,omitempty"`
	LegacyPresent               bool                 `json:"legacy_present"`
	LegacyRetrievalIndexPath    string               `json:"legacy_retrieval_index_path,omitempty"`
	LegacyRetrievalIndexPresent bool                 `json:"legacy_retrieval_index_present"`
}

func newSidecarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sidecar",
		Short: "Inspect and reclaim per-vault sidecar indexes",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List sidecars and identify orphaned vaults",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printSidecarReport()
		},
	})

	var confirm bool
	pruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove sidecars whose vault no longer exists",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirm {
				return fmt.Errorf("refusing to remove sidecars without --confirm")
			}
			removed, err := sidecar.RemoveOrphanSidecars()
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(struct {
					Removed int `json:"removed"`
				}{Removed: removed})
			}
			fmt.Printf("Removed %d orphan sidecar(s).\n", removed)
			return nil
		},
	}
	pruneCmd.Flags().BoolVar(&confirm, "confirm", false, "confirm deletion of orphaned sidecars")
	cmd.AddCommand(pruneCmd)
	return cmd
}

func printSidecarReport() error {
	entries, err := sidecar.ListSidecars()
	if err != nil {
		return err
	}
	report := sidecarReport{Sidecars: make([]sidecarReportEntry, 0, len(entries)), Count: len(entries)}
	for _, entry := range entries {
		report.TotalBytes += entry.Size
		if entry.Orphan {
			report.OrphanCount++
		}
		reportEntry := sidecarReportEntry{SidecarEntry: entry}
		reportEntry.RetrievalIndexPath = filepath.Join(filepath.Dir(entry.Path), "retrieval.db")
		if entry.VaultPath != "" {
			if retrievalPath, retrievalErr := retrieval.IndexPathForVault(entry.VaultPath); retrievalErr == nil {
				reportEntry.RetrievalIndexPath = retrievalPath
			}
		}
		if info, statErr := os.Stat(reportEntry.RetrievalIndexPath); statErr == nil {
			reportEntry.RetrievalIndexPresent = info.Mode().IsRegular()
		}
		report.Sidecars = append(report.Sidecars, reportEntry)
	}
	root, err := sidecar.SidecarRoot()
	if err != nil {
		return err
	}
	report.LegacyPath = filepath.Join(filepath.Dir(root), "sidecar.db")
	if info, statErr := os.Stat(report.LegacyPath); statErr == nil {
		report.LegacyPresent = info.Mode().IsRegular()
	}
	if legacyRetrievalPath, retrievalErr := retrieval.IndexLocation(); retrievalErr == nil {
		report.LegacyRetrievalIndexPath = legacyRetrievalPath
		if info, statErr := os.Stat(legacyRetrievalPath); statErr == nil {
			report.LegacyRetrievalIndexPresent = info.Mode().IsRegular()
		}
	}
	if jsonFlag {
		return outputResult(report)
	}
	fmt.Printf("Sidecars: %d\nTotal size: %d bytes\nOrphaned: %d\n", report.Count, report.TotalBytes, report.OrphanCount)
	if report.LegacyPresent {
		fmt.Printf("Legacy sidecar: %s (not managed)\n", report.LegacyPath)
	}
	if report.LegacyRetrievalIndexPresent {
		fmt.Printf("Legacy retrieval index: %s (not managed)\n", report.LegacyRetrievalIndexPath)
	}
	for _, entry := range report.Sidecars {
		state := "live"
		if !entry.Metadata {
			state = "unknown (missing metadata)"
		} else if entry.Orphan {
			state = "orphan"
		}
		fmt.Printf("%s\t%s\t%d bytes\t%s\t%s\n", state, entry.VaultPath, entry.Size, entry.Path, entry.RetrievalIndexPath)
	}
	return nil
}
