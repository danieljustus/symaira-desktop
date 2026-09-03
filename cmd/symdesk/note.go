package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newNoteCmd() *cobra.Command {
	noteCmd := &cobra.Command{
		Use:   "note",
		Short: "Manage notes",
	}

	noteNewCmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new note",
		RunE: func(cmd *cobra.Command, args []string) error {
			title, _ := cmd.Flags().GetString("title")
			templateName, _ := cmd.Flags().GetString("template")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			// Simple content logic
			content := ""
			if len(args) > 0 {
				content = args[0]
			}

			path, err := svc.NoteNew(title, content, templateName)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "created", "path": path})
		},
	}
	noteNewCmd.Flags().String("title", "", "title of the new note")
	noteNewCmd.Flags().String("template", "", "template name to use (optional)")
	markFlagRequired(noteNewCmd, "title")
	noteCmd.AddCommand(noteNewCmd)

	noteDailyCmd := &cobra.Command{
		Use:   "daily",
		Short: "Create or open today's daily note",
		RunE: func(cmd *cobra.Command, args []string) error {
			dateStr, _ := cmd.Flags().GetString("date")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			path, err := svc.NoteDaily(dateStr)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "path": path})
		},
	}
	noteDailyCmd.Flags().String("date", "", "optional date (YYYY-MM-DD)")
	noteCmd.AddCommand(noteDailyCmd)

	noteMoveCmd := &cobra.Command{
		Use:   "move [oldPath] [newPath]",
		Short: "Move/Rename a note",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			err = svc.NoteMove(args[0], args[1])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "moved", "from": args[0], "to": args[1]})
		},
	}
	noteCmd.AddCommand(noteMoveCmd)
	noteCmd.AddCommand(newNoteDeleteCmd())

	return noteCmd
}
