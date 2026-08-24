package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
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

	viewsExportCSVCmd := &cobra.Command{
		Use:   "export-csv [id]",
		Short: "Export visible and computed view rows to standard CSV",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			data, err := svc.ViewsExportCSV(args[0])
			if err != nil {
				return err
			}
			outputPath, _ := cmd.Flags().GetString("output")
			if outputPath != "" {
				if err := os.WriteFile(outputPath, data, 0644); err != nil {
					return fmt.Errorf("write csv output: %w", err)
				}
				return outputResult(map[string]string{"status": "exported", "output": outputPath})
			}
			fmt.Print(string(data))
			return nil
		},
	}
	viewsExportCSVCmd.Flags().String("output", "", "output file path for CSV")
	viewsCmd.AddCommand(viewsExportCSVCmd)

	viewsImportCSVCmd := &cobra.Command{
		Use:   "import-csv [file]",
		Short: "One-way import of CSV records into vault notes (dry-run default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			// #nosec G304 -- args[0] is user-provided CLI file input.
			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open csv file: %w", err)
			}
			defer f.Close()

			apply, _ := cmd.Flags().GetBool("apply")
			folder, _ := cmd.Flags().GetString("folder")
			mapStr, _ := cmd.Flags().GetString("map")
			titleCol, _ := cmd.Flags().GetString("title-col")
			baseName, _ := cmd.Flags().GetString("base")
			onCollision, _ := cmd.Flags().GetString("on-collision")

			mapping := make(map[string]string)
			if mapStr != "" {
				for _, pair := range strings.Split(mapStr, ",") {
					parts := strings.SplitN(pair, "=", 2)
					if len(parts) == 2 {
						mapping[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
					}
				}
			}

			opts := service.CSVImportOptions{
				CSVData:       f,
				Apply:         apply,
				Folder:        folder,
				ColumnMapping: mapping,
				TitleColumn:   titleCol,
				BaseName:      baseName,
				OnCollision:   onCollision,
			}

			report, err := svc.CSVImport(opts)
			if err != nil {
				return err
			}
			return outputResult(report)
		},
	}
	viewsImportCSVCmd.Flags().Bool("apply", false, "execute writes (default is dry-run preview)")
	viewsImportCSVCmd.Flags().String("folder", "", "target directory in vault")
	viewsImportCSVCmd.Flags().String("map", "", "column to property mappings (e.g. 'Col1=prop1,Col2=prop2')")
	viewsImportCSVCmd.Flags().String("title-col", "", "specific column name to use for note title")
	viewsImportCSVCmd.Flags().String("base", "", "optional base note name to create or update")
	viewsImportCSVCmd.Flags().String("on-collision", "suffix", "collision strategy: suffix|skip|error")
	viewsCmd.AddCommand(viewsImportCSVCmd)

	viewsEmbedCmd := &cobra.Command{
		Use:   "embed [spec-yaml-or-file]",
		Short: "Execute a symdesk-base embed specification",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			var specYAML string
			if len(args) > 0 {
				// #nosec G304 -- args[0] is user-provided CLI input.
				if data, err := os.ReadFile(args[0]); err == nil {
					specYAML = string(data)
				} else {
					specYAML = args[0]
				}
			} else {
				specFlag, _ := cmd.Flags().GetString("spec")
				baseFlag, _ := cmd.Flags().GetString("base")
				viewFlag, _ := cmd.Flags().GetString("view")
				limitFlag, _ := cmd.Flags().GetInt("limit")

				if specFlag != "" {
					specYAML = specFlag
				} else if baseFlag != "" {
					specYAML = fmt.Sprintf("base: %s\n", baseFlag)
					if viewFlag != "" {
						specYAML += fmt.Sprintf("view: %s\n", viewFlag)
					}
					if limitFlag > 0 {
						specYAML += fmt.Sprintf("limit: %d\n", limitFlag)
					}
				} else {
					return fmt.Errorf("provide spec YAML as argument or via --spec/--base flags")
				}
			}

			res, err := svc.ExecuteBaseEmbed(specYAML)
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(res)
			}
			fmt.Print(res.Markdown)
			return nil
		},
	}
	viewsEmbedCmd.Flags().String("spec", "", "raw YAML embed spec")
	viewsEmbedCmd.Flags().String("base", "", "base name or id")
	viewsEmbedCmd.Flags().String("view", "", "view name or id")
	viewsEmbedCmd.Flags().Int("limit", 0, "row cap limit")
	viewsCmd.AddCommand(viewsEmbedCmd)

	// Base note management subcommands
	baseCmd := &cobra.Command{
		Use:   "base",
		Short: "Manage base notes",
	}

	baseListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all base notes in vault",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			bases, err := svc.BaseList()
			if err != nil {
				return err
			}
			return outputResult(bases)
		},
	}
	baseCmd.AddCommand(baseListCmd)

	baseGetCmd := &cobra.Command{
		Use:   "get [ref]",
		Short: "Get a base note by id or path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			base, err := svc.BaseGet(args[0])
			if err != nil {
				return err
			}
			return outputResult(base)
		},
	}
	baseCmd.AddCommand(baseGetCmd)

	baseSaveCmd := &cobra.Command{
		Use:   "save [json]",
		Short: "Save a base note from JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			var base dbviews.Base
			if err := json.Unmarshal([]byte(args[0]), &base); err != nil {
				return fmt.Errorf("unmarshal base: %w", err)
			}
			if err := svc.BaseSave(&base); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "saved", "path": base.Path})
		},
	}
	baseCmd.AddCommand(baseSaveCmd)

	baseDeleteCmd := &cobra.Command{
		Use:   "delete [ref]",
		Short: "Delete a base note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			if err := svc.BaseDelete(args[0]); err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "deleted"})
		},
	}
	baseCmd.AddCommand(baseDeleteCmd)

	baseNewCmd := &cobra.Command{
		Use:   "new [title] [description]",
		Short: "Create a new base note",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			desc := ""
			if len(args) > 1 {
				desc = args[1]
			}
			base, err := svc.BaseNew(args[0], desc)
			if err != nil {
				return err
			}
			return outputResult(base)
		},
	}
	baseCmd.AddCommand(baseNewCmd)

	viewsCmd.AddCommand(baseCmd)

	return viewsCmd
}
