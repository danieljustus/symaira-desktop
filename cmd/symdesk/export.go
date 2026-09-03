package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a note or view to PDF or HTML",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			relPath, _ := cmd.Flags().GetString("note")
			viewID, _ := cmd.Flags().GetString("view")
			outputPath, _ := cmd.Flags().GetString("output")
			format, _ := cmd.Flags().GetString("format")
			profile, _ := cmd.Flags().GetString("profile")

			res, err := svc.Export(relPath, viewID, outputPath, format, profile)
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	cmd.Flags().String("note", "", "vault-relative note path")
	cmd.Flags().String("view", "", "view id")
	cmd.Flags().String("output", "", "output file path")
	cmd.Flags().String("format", "pdf", "pdf or html")
	cmd.Flags().String("profile", "", "symprint profile for PDF")
	cmd.MarkFlagsMutuallyExclusive("note", "view")
	cmd.AddCommand(newExportProfilesCmd())
	return cmd
}

// newExportProfilesCmd lists the PDF profiles `export --profile` accepts. The
// app builds its profile picker from this, so the picker cannot drift from
// print/.
func newExportProfilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List the symprint profiles available for PDF export",
		RunE: func(cmd *cobra.Command, args []string) error {
			return outputResult(service.ExportProfiles())
		},
	}
}
