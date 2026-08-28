package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

type sidecarReport struct {
	Sidecars      []sidecar.SidecarEntry `json:"sidecars"`
	Count         int                    `json:"count"`
	TotalBytes    int64                  `json:"total_bytes"`
	OrphanCount   int                    `json:"orphan_count"`
	LegacyPath    string                 `json:"legacy_path,omitempty"`
	LegacyPresent bool                   `json:"legacy_present"`
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
	report := sidecarReport{Sidecars: entries, Count: len(entries)}
	for _, entry := range entries {
		report.TotalBytes += entry.Size
		if entry.Orphan {
			report.OrphanCount++
		}
	}
	root, err := sidecar.SidecarRoot()
	if err != nil {
		return err
	}
	report.LegacyPath = filepath.Join(filepath.Dir(root), "sidecar.db")
	if info, statErr := os.Stat(report.LegacyPath); statErr == nil {
		report.LegacyPresent = info.Mode().IsRegular()
	}
	if jsonFlag {
		return outputResult(report)
	}
	fmt.Printf("Sidecars: %d\nTotal size: %d bytes\nOrphaned: %d\n", report.Count, report.TotalBytes, report.OrphanCount)
	if report.LegacyPresent {
		fmt.Printf("Legacy sidecar: %s (not managed)\n", report.LegacyPath)
	}
	for _, entry := range entries {
		state := "live"
		if !entry.Metadata {
			state = "unknown (missing metadata)"
		} else if entry.Orphan {
			state = "orphan"
		}
		fmt.Printf("%s\t%s\t%d bytes\t%s\n", state, entry.VaultPath, entry.Size, entry.Path)
	}
	return nil
}
