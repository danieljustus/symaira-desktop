package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newTagsCmd() *cobra.Command {
	tagsCmd := &cobra.Command{
		Use:   "tags",
		Short: "Vault-wide tag management (rename, merge, delete)",
	}

	tagsCmd.AddCommand(&cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a tag in every file that carries it and re-index",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			results, err := service.New(vRoot, db).TagsRename(args[0], args[1])
			if err != nil {
				return err
			}
			return outputTagResults(cmd, results)
		},
	})

	tagsCmd.AddCommand(&cobra.Command{
		Use:   "merge <from> <into>",
		Short: "Merge a tag into another tag across the vault and re-index",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			results, err := service.New(vRoot, db).TagsMerge(args[0], args[1])
			if err != nil {
				return err
			}
			return outputTagResults(cmd, results)
		},
	})

	tagsCmd.AddCommand(&cobra.Command{
		Use:   "delete <tag>",
		Short: "Remove a tag from every file that carries it and re-index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			results, err := service.New(vRoot, db).TagsDelete(args[0])
			if err != nil {
				return err
			}
			return outputTagResults(cmd, results)
		},
	})

	return tagsCmd
}

func outputTagResults(cmd *cobra.Command, results []service.TagRenameResult) error {
	if jsonFlag {
		return outputResult(results)
	}
	updated, skipped := 0, 0
	for _, r := range results {
		switch r.Status {
		case "updated":
			updated++
		case "skipped":
			skipped++
		case "error":
			fmt.Printf("error: %s: %s\n", r.File, r.Error)
		}
	}
	fmt.Printf("Tag update complete. %d file(s) updated, %d skipped.\n", updated, skipped)
	return nil
}
