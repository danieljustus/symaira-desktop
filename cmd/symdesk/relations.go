package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// newRelationsContactCmd resolves either a contact id or a correspondent name
// to the references and vault items that mention it. Name lookup remains the
// compatibility fallback; --id is identity-based and does not inspect names.
func newRelationsContactCmd() *cobra.Command {
	var byID bool
	contactCmd := &cobra.Command{
		Use:   "contact [name|id]",
		Short: "Resolve a contact id or correspondent name and list everything that mentions it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			svc := service.New(vRoot, db)

			var result *service.ContactReferences
			if byID {
				result, err = svc.ResolveContactReferencesByID(args[0])
			} else {
				result, err = svc.ResolveContactReferences(args[0])
			}
			if err != nil {
				return err
			}
			return outputResult(result)
		},
	}
	contactCmd.Flags().BoolVar(&byID, "id", false, "treat the argument as an opaque contact id")

	linkCmd := &cobra.Command{
		Use:   "link <vault-note> <contact-id>",
		Short: "Link an ordinary note to a reviewed contact reference",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			svc := service.New(vRoot, db)
			ref, err := svc.LinkNoteContact(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"path": args[0], "contact_id": ref.ID, "status": "linked"})
		},
	}
	contactCmd.AddCommand(linkCmd)

	unlinkCmd := &cobra.Command{
		Use:   "unlink <vault-note>",
		Short: "Remove an ordinary note's contact reference",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			svc := service.New(vRoot, db)
			if err := svc.UnlinkNoteContact(args[0]); err != nil {
				return err
			}
			return outputResult(map[string]string{"path": args[0], "status": "unlinked"})
		},
	}
	contactCmd.AddCommand(unlinkCmd)
	return contactCmd
}

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
	relationsCmd.AddCommand(newRelationsContactCmd())

	return relationsCmd
}
