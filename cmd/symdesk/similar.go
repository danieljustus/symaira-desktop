package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// newSimilarSubcommand builds the "near-duplicates of one file" command body
// shared between the retired top-level `similar` command and the
// `duplicates similar` subcommand it was folded into (#467). Both share the
// same reconciled default threshold, service.DefaultDuplicateThreshold (see
// duplicate_threshold_test.go, added for issue #452), so `duplicates`,
// `similar`/`duplicates similar`, and `vault health` never disagree about
// what counts as a near-duplicate.
func newSimilarSubcommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "similar [file]",
		Short: "Find near-duplicate documents by SimHash similarity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			threshold, _ := cmd.Flags().GetInt("threshold")
			results, err := svc.SimilarDocs(args[0], service.ResolveDuplicateThreshold(threshold))
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	cmd.Flags().Int("threshold", service.DefaultDuplicateThreshold, "minimum similarity percentage (0-100)")
	return cmd
}

// newSimilarCmd is the retired `similar` command (#467, folded into
// `duplicates similar`). It is registered hidden so `symdesk similar <file>`
// keeps working for existing scripts and MCP callers without appearing in
// `symdesk --help`.
func newSimilarCmd() *cobra.Command {
	cmd := newSimilarSubcommand()
	cmd.Hidden = true
	return cmd
}
