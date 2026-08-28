package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search files in the vault",
		Long:  "Search files using full-text terms and filters. Filters include path:, tag:, type:, status:, filename:, filetype:, created: and modified:. Filetype accepts comma-separated extensions (for example pdf,epub); dates accept YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD and last day/week/month/year. Filters use AND semantics and support -negation.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("search query is required")
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			svc := service.New(vRoot, db)

			results, err := svc.SearchWithMeta(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	cmd.AddCommand(newSearchExportCmd(), newSearchNotebookCmd())
	return cmd
}

func newSearchExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "export [query]",
		Aliases: []string{"save-note"},
		Short:   "Export a search result set to a Markdown note or PDF",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, _ := cmd.Flags().GetString("format")
			title, _ := cmd.Flags().GetString("title")
			outputPath, _ := cmd.Flags().GetString("output")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			result, err := service.New(vRoot, db).ExportSearch(args[0], title, outputPath, format)
			if err != nil {
				return err
			}
			return outputResult(result)
		},
	}
	cmd.Flags().String("format", "markdown", "markdown or pdf")
	cmd.Flags().String("title", "", "title for the exported result set")
	cmd.Flags().String("output", "", "output path; Markdown stays inside the vault")
	return cmd
}

func newSearchNotebookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "notebook [query]",
		Aliases: []string{"keep", "save"},
		Short:   "Keep a search result set as a Markdown-backed notebook",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title, _ := cmd.Flags().GetString("title")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			nb, err := service.New(vRoot, db).SearchNotebook(args[0], title)
			if err != nil {
				return err
			}
			return outputResult(nb)
		},
	}
	cmd.Flags().String("title", "", "title for the working-set notebook")
	return cmd
}
