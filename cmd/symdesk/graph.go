package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newGraphCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "graph",
		Short: "Get graph data",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.Graph()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
}
