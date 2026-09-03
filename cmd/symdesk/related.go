package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// newRelatedSubcommand builds the "related entities and notes for a file"
// command body shared between the retired top-level `related` command and
// the `relations related` subcommand it was folded into (#467).
func newRelatedSubcommand() *cobra.Command {
	return &cobra.Command{
		Use:   "related [file]",
		Short: "Get related entities and notes for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			results, err := svc.Related(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
}

// newRelatedCmd is the retired `related` command (#467, folded into
// `relations related`). It is registered hidden so `symdesk related <file>`
// keeps working for existing scripts and MCP callers without appearing in
// `symdesk --help`.
func newRelatedCmd() *cobra.Command {
	cmd := newRelatedSubcommand()
	cmd.Hidden = true
	return cmd
}
