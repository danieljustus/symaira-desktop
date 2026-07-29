package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newSimilarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "similar [file]",
		Short: "Find near-duplicate documents by SimHash similarity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			threshold, _ := cmd.Flags().GetInt("threshold")
			if threshold <= 0 {
				threshold = 50
			}

			results, err := svc.SimilarDocs(args[0], threshold)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	cmd.Flags().Int("threshold", 50, "minimum similarity percentage (0-100)")
	return cmd
}
