package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search [query]",
		Short: "Search files in the vault",
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
