package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/health"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newVaultCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "vault", Short: "Inspect and manage the document vault (Markdown workspace, not symvault)"}
	cmd.AddCommand(newVaultHealthCmd())
	cmd.AddCommand(newVaultAdoptCmd())
	return cmd
}

func newVaultHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Scan the document vault and emit a reviewable repair plan",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			threshold, err := cmd.Flags().GetInt("duplicate-threshold")
			if err != nil {
				return err
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches existing CLI command pattern
			report, err := health.Scan(vRoot, db, threshold)
			if err != nil {
				return err
			}
			return outputResult(report)
		},
	}
	cmd.Flags().Int("duplicate-threshold", service.DefaultDuplicateThreshold, "minimum similarity percentage for near-duplicate findings")
	return cmd
}
