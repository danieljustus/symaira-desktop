package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newBacklinksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backlinks [file|contact-target]",
		Short: "Get backlinks for a file or opaque contact reference target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			results, err := svc.Backlinks(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
}
