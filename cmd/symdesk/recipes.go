package main

import (
	"context"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/recipes"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newRecipeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "recipe", Short: "Validate, run, and review vault automation recipes"}
	cmd.AddCommand(&cobra.Command{
		Use: "validate <recipe.yml>", Short: "Validate a declarative recipe without running it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := recipes.Load(args[0])
			if err != nil {
				return err
			}
			return outputResult(map[string]any{"status": "valid", "recipe": r})
		},
	})
	runCmd := &cobra.Command{
		Use: "run <recipe.yml>", Short: "Ask the configured runner for a reviewable change proposal", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trigger, _ := cmd.Flags().GetString("trigger")
			root, err := vault.ResolveVaultRoot("", cfg)
			if err != nil {
				return err
			}
			r, err := recipes.Load(args[0])
			if err != nil {
				return err
			}
			m, err := recipes.Start(context.Background(), root, r, trigger, cfg.RecipeRunner)
			if err != nil {
				return err
			}
			return outputResult(map[string]any{"status": m.Status, "run_id": m.Request.RunID, "changes": m.Response.Changes, "trace": filepath.Join(root, ".symdesk", "runs", m.Request.RunID, "trace.md")})
		},
	}
	runCmd.Flags().String("trigger", "manual", "event trigger for this run")
	cmd.AddCommand(runCmd)
	for _, action := range []string{"diff", "accept", "reject"} {
		action := action
		cmd.AddCommand(&cobra.Command{Use: action + " <run-id>", Short: "Review or " + action + " a pending recipe run", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			root, err := vault.ResolveVaultRoot("", cfg)
			if err != nil {
				return err
			}
			switch action {
			case "diff":
				changes, err := recipes.PendingDiff(root, args[0])
				if err != nil {
					return err
				}
				return outputResult(changes)
			case "accept":
				err = recipes.Accept(root, args[0])
			case "reject":
				err = recipes.Reject(root, args[0])
			}
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": action + "ed", "run_id": args[0]})
		}})
	}
	return cmd
}
