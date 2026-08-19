package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newDuplicatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "duplicates",
		Short: "List groups of possible duplicate documents (SimHash)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			threshold, _ := cmd.Flags().GetInt("threshold")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			groups, err := service.New(vRoot, db).SimilarAll(threshold)
			if err != nil {
				return err
			}
			return outputResult(groups)
		},
	}
	cmd.Flags().Int("threshold", service.DefaultDuplicateThreshold, "minimum similarity percentage (0-100)")

	// `similar` named the same SimHash algorithm and read as an
	// indistinguishable sibling command (#467); fold it in as a subcommand
	// so `duplicates` (groups) and `duplicates similar <file>` (one file's
	// near-duplicates) live under a single, discoverable entry point. The
	// retired top-level `similar` command (similar.go) stays registered and
	// hidden for backward compatibility.
	cmd.AddCommand(newSimilarSubcommand())

	return cmd
}
