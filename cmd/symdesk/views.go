package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newViewsCmd() *cobra.Command {
	viewsCmd := &cobra.Command{
		Use:   "views",
		Short: "Manage saved views",
	}

	viewsListCmd := &cobra.Command{
		Use:   "list",
		Short: "List saved views",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.ViewsList()
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	viewsCmd.AddCommand(viewsListCmd)

	viewsGetCmd := &cobra.Command{
		Use:   "get [id]",
		Short: "Get a specific view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.ViewsGet(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	viewsCmd.AddCommand(viewsGetCmd)

	viewsSaveCmd := &cobra.Command{
		Use:   "save [json]",
		Short: "Save a view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			err = svc.ViewsSave([]byte(args[0]))
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "saved"})
		},
	}
	viewsCmd.AddCommand(viewsSaveCmd)

	viewsDeleteCmd := &cobra.Command{
		Use:   "delete [id]",
		Short: "Delete a saved view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			if err := svc.ViewsDelete(args[0]); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "deleted"})
		},
	}
	viewsCmd.AddCommand(viewsDeleteCmd)

	viewsNewEntryCmd := &cobra.Command{
		Use:   "new-entry [id] [title]",
		Short: "Create a pre-filled note from a saved view",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			path, err := service.New(vRoot, db).ViewsNewEntry(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"path": path})
		},
	}
	viewsCmd.AddCommand(viewsNewEntryCmd)

	viewsSiblingsCmd := &cobra.Command{
		Use:   "siblings [id]",
		Short: "List saved views that share a source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := service.New(vRoot, db).ViewsSiblings(args[0])
			if err != nil {
				return err
			}
			return outputResult(result)
		},
	}
	viewsCmd.AddCommand(viewsSiblingsCmd)

	viewsExecCmd := &cobra.Command{
		Use:   "exec [id]",
		Short: "Execute a view and get results",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			res, err := svc.ViewsExec(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	viewsCmd.AddCommand(viewsExecCmd)

	return viewsCmd
}
