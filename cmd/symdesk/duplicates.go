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
			defer db.Close()
			groups, err := service.New(vRoot, db).SimilarAll(threshold)
			if err != nil {
				return err
			}
			return outputResult(groups)
		},
	}
	cmd.Flags().Int("threshold", 50, "minimum similarity percentage (0-100)")
	return cmd
}
