package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

func newIndexMaintenanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "location", Short: "Show the shared retrieval index location", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := retrieval.IndexLocation()
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"index_location": path})
		}}
	backup := &cobra.Command{Use: "backup", Short: "Back up the derived retrieval index", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("output")
			if err != nil {
				return err
			}
			if path == "" {
				return fmt.Errorf("--output is required")
			}
			if err := retrieval.BackupIndex(path); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "backup": path})
		}}
	backup.Flags().String("output", "", "Destination backup file")
	restore := &cobra.Command{Use: "restore", Short: "Restore the derived retrieval index from a backup", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("input")
			if err != nil {
				return err
			}
			if path == "" {
				return fmt.Errorf("--input is required")
			}
			if err := retrieval.RestoreIndex(path); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "restored_from": path})
		}}
	restore.Flags().String("input", "", "Source backup file")
	relocate := &cobra.Command{Use: "relocate", Short: "Move the derived retrieval index to a new location", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("output")
			if err != nil {
				return err
			}
			if path == "" {
				return fmt.Errorf("--output is required")
			}
			if err := retrieval.RelocateIndex(path); err != nil {
				return err
			}
			location, err := retrieval.IndexLocation()
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "index_location": location})
		}}
	relocate.Flags().String("output", "", "New index file location")
	root := &cobra.Command{Use: "maintenance", Short: "Safely back up or restore the derived retrieval index"}
	root.AddCommand(cmd, backup, restore, relocate)
	return root
}
