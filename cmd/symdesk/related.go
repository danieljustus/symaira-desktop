package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newRelatedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "related [file]",
		Short: "Get related entities and notes for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.Related(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
}
