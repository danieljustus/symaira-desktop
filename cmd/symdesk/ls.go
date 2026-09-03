package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List files in the vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			dirPrefix, _ := cmd.Flags().GetString("dir")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			results, err := svc.Ls(dirPrefix)
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	cmd.Flags().String("dir", "", "directory prefix")
	return cmd
}
