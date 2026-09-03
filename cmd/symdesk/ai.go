package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "AI-assisted vault operations",
	}
	autofillCmd := &cobra.Command{
		Use:   "autofill",
		Short: "Autofill a property on notes matching a view",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			viewID, _ := cmd.Flags().GetString("view")
			property, _ := cmd.Flags().GetString("property")
			prompt, _ := cmd.Flags().GetString("prompt")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			res, err := svc.Autofill(viewID, property, prompt, dryRun)
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	autofillCmd.Flags().String("view", "", "view id of notes to process")
	autofillCmd.Flags().String("property", "", "frontmatter property to fill")
	autofillCmd.Flags().String("prompt", "", "extra prompt/instruction for the AI")
	autofillCmd.Flags().Bool("dry-run", false, "show what would be changed without writing")
	markFlagRequired(autofillCmd, "view")
	markFlagRequired(autofillCmd, "property")
	cmd.AddCommand(autofillCmd)
	return cmd
}
