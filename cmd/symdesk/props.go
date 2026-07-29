package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newPropsCmd() *cobra.Command {
	propsCmd := &cobra.Command{
		Use:   "props",
		Short: "Manage file properties",
	}

	propsGetCmd := &cobra.Command{
		Use:   "get [file]",
		Short: "Get properties for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.Props(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	propsCmd.AddCommand(propsGetCmd)

	propsEditCmd := &cobra.Command{
		Use:   "edit [file] [key] [value]",
		Short: "Edit a property",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			err = svc.PropsEdit(args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "updated"})
		},
	}
	propsCmd.AddCommand(propsEditCmd)

	return propsCmd
}
