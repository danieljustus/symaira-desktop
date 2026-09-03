package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newNotebookCmd() *cobra.Command {
	notebookCmd := &cobra.Command{
		Use:   "notebook",
		Short: "Manage notebooks (bounded, named source sets used to scope AI grounding)",
	}

	newCmd := &cobra.Command{
		Use:   "new [title]",
		Short: "Create a new notebook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			description, _ := cmd.Flags().GetString("description")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			nb, err := svc.NotebookNew(args[0], description)
			if err != nil {
				return err
			}
			return outputResult(nb)
		},
	}
	newCmd.Flags().String("description", "", "optional description of the notebook's purpose")
	notebookCmd.AddCommand(newCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all notebooks in the vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			list, err := svc.NotebookList()
			if err != nil {
				return err
			}
			return outputResult(list)
		},
	}
	notebookCmd.AddCommand(listCmd)

	showCmd := &cobra.Command{
		Use:   "show [notebook]",
		Short: "Show a notebook and its resolved sources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			nb, err := svc.NotebookGet(args[0])
			if err != nil {
				return err
			}
			refs, err := nb.ResolveSources(vRoot)
			if err != nil {
				return err
			}
			return outputResult(map[string]interface{}{
				"id":          nb.ID,
				"path":        nb.Path,
				"title":       nb.Title,
				"description": nb.Description,
				"created":     nb.Created,
				"sources":     refs,
			})
		},
	}
	notebookCmd.AddCommand(showCmd)

	addSourceCmd := &cobra.Command{
		Use:   "add-source [notebook] [path]",
		Short: "Add a vault file to a notebook's source set",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			nb, err := svc.NotebookAddSource(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(nb)
		},
	}
	notebookCmd.AddCommand(addSourceCmd)

	removeSourceCmd := &cobra.Command{
		Use:   "remove-source [notebook] [path]",
		Short: "Remove a vault file from a notebook's source set (the file itself is untouched)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			nb, err := svc.NotebookRemoveSource(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(nb)
		},
	}
	notebookCmd.AddCommand(removeSourceCmd)

	deleteCmd := &cobra.Command{
		Use:   "delete [notebook]",
		Short: "Move a notebook to the vault trash (restorable via 'symdesk trash restore')",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			if err := svc.NotebookDelete(args[0]); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "trashed", "notebook": args[0]})
		},
	}
	notebookCmd.AddCommand(deleteCmd)

	generateCmd := &cobra.Command{
		Use:   "generate [notebook]",
		Short: "Generate a studio artifact (briefing, study-guide, faq, timeline, or a custom templates/notebook-<kind>.md kind) from a notebook's sources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, _ := cmd.Flags().GetString("kind")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			res, err := svc.NotebookGenerate(args[0], kind, dryRun)
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	generateCmd.Flags().String("kind", "", "artifact kind: briefing, study-guide, faq, timeline, or a custom templates/notebook-<kind>.md kind")
	generateCmd.Flags().Bool("dry-run", false, "generate and show the artifact without writing it to the vault")
	markFlagRequired(generateCmd, "kind")
	notebookCmd.AddCommand(generateCmd)

	return notebookCmd
}
