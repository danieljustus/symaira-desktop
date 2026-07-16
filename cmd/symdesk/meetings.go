package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newMeetingCmd() *cobra.Command {
	meetingCmd := &cobra.Command{
		Use:   "meeting",
		Short: "Import and review SymMeet meeting artifacts",
	}

	importCmd := &cobra.Command{
		Use:   "import <meeting_id>",
		Short: "Import a reviewed SymMeet meeting into the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			path, err := svc.MeetingImport(args[0])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"path": path, "status": "imported"})
		},
	}
	meetingCmd.AddCommand(importCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List imported meeting notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			results, err := svc.MeetingList()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	meetingCmd.AddCommand(listCmd)

	availableCmd := &cobra.Command{
		Use:   "available",
		Short: "List SymMeet meetings that have not yet been imported",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			results, err := svc.AvailableMeetings()
			if err != nil {
				return err
			}
			return outputResult(results)
		},
	}
	meetingCmd.AddCommand(availableCmd)

	showCmd := &cobra.Command{
		Use:   "show <vault-note>",
		Short: "Show one imported meeting note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			doc, err := svc.MeetingShow(args[0])
			if err != nil {
				return err
			}
			return outputResult(doc)
		},
	}
	meetingCmd.AddCommand(showCmd)

	refreshCmd := &cobra.Command{
		Use:   "refresh <vault-note>",
		Short: "Preview (or apply) a re-export of a meeting note's transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apply, _ := cmd.Flags().GetBool("apply")

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close() //nolint:errcheck // matches the existing CLI command pattern in this package
			svc := service.New(vRoot, db)

			result, err := svc.MeetingRefresh(args[0], apply)
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(result)
			}
			if !result.Changed {
				fmt.Println("No changes.")
				return nil
			}
			for _, line := range result.DiffLines {
				fmt.Println(line)
			}
			if result.Applied {
				fmt.Println("Applied.")
			} else {
				fmt.Println("Preview only; re-run with --apply to write these changes.")
			}
			return nil
		},
	}
	refreshCmd.Flags().Bool("apply", false, "write the refreshed transcript instead of only previewing it")
	meetingCmd.AddCommand(refreshCmd)

	return meetingCmd
}
