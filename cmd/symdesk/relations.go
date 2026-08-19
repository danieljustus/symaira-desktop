package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newRelationsCmd() *cobra.Command {
	relationsCmd := &cobra.Command{
		Use:   "relations",
		Short: "Inspect typed relations between notes",
	}

	relationsInverseCmd := &cobra.Command{
		Use:   "inverse [file]",
		Short: "List notes that reference a file via frontmatter properties or wikilinks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			results, err := svc.RelationsInverse(args[0])
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	relationsCmd.AddCommand(relationsInverseCmd)

	// `related` named a near-synonym concept ("related entities and notes
	// for a file" vs. "typed relations between notes") and read as an
	// indistinguishable sibling command (#467); fold it in as a subcommand
	// so both live under one discoverable entry point. The retired
	// top-level `related` command (related.go) stays registered and hidden
	// for backward compatibility.
	relationsCmd.AddCommand(newRelatedSubcommand())

	return relationsCmd
}
