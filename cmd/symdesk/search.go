package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search [query]",
		Short: "Search files in the vault",
		Long:  "Search files using full-text terms and filters. Filters include path:, tag:, type:, status:, filename:, filetype:, created: and modified:. Filetype accepts comma-separated extensions (for example pdf,epub); dates accept YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD and last day/week/month/year. Filters use AND semantics and support -negation.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.SearchWithMeta(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
}
