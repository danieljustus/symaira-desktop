package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/history"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newHistoryCmd() *cobra.Command {
	historyCmd := &cobra.Command{
		Use:   "history [file]",
		Short: "List stored snapshots of a vault file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			entries, err := svc.HistoryList(args[0])
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(entries)
			}
			if len(entries) == 0 {
				fmt.Printf("no snapshots recorded for %s\n", args[0])
				return nil
			}
			for _, e := range entries {
				fmt.Printf("%s  %s  %6d bytes\n", e.ID[:12], e.Timestamp.Local().Format("2006-01-02 15:04:05"), e.Size)
			}
			return nil
		},
	}

	pruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Apply the retention policy to all stored snapshots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			maxPerFile, _ := cmd.Flags().GetInt("max-per-file")
			maxAgeDays, _ := cmd.Flags().GetInt("max-age-days")
			if !cmd.Flags().Changed("max-per-file") {
				maxPerFile = cfg.HistoryMaxPerFile
			}
			if !cmd.Flags().Changed("max-age-days") {
				maxAgeDays = cfg.HistoryMaxAgeDays
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			removed, err := svc.HistoryPrune(history.RetentionPolicy{
				MaxPerFile: maxPerFile,
				MaxAge:     time.Duration(maxAgeDays) * 24 * time.Hour,
			})
			if err != nil {
				return err
			}
			return outputResult(map[string]interface{}{"status": "pruned", "removed": removed})
		},
	}
	pruneCmd.Flags().Int("max-per-file", 0, "keep at most N snapshots per file (0 = unlimited, default from config)")
	pruneCmd.Flags().Int("max-age-days", 0, "drop snapshots older than N days, newest always kept (0 = unlimited, default from config)")
	historyCmd.AddCommand(pruneCmd)

	showCmd := &cobra.Command{
		Use:   "show <snapshot-id>",
		Short: "Print the stored content of a snapshot (for diff previews)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			content, err := service.New(vRoot, db).HistoryContent(args[0])
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(map[string]string{"id": args[0], "content": string(content)})
			}
			fmt.Print(string(content))
			return nil
		},
	}
	historyCmd.AddCommand(showCmd)

	return historyCmd
}

func newRestoreCmd() *cobra.Command {
	restoreCmd := &cobra.Command{
		Use:   "restore [file]",
		Short: "Restore a vault file from a snapshot (latest by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			at, _ := cmd.Flags().GetString("at")
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)
			entry, err := svc.HistoryRestore(args[0], at)
			if err != nil {
				return err
			}
			return outputResult(map[string]interface{}{
				"status":    "restored",
				"file":      args[0],
				"id":        entry.ID,
				"timestamp": entry.Timestamp,
			})
		},
	}
	restoreCmd.Flags().String("at", "", "snapshot id (or unique prefix) to restore; latest snapshot when omitted")
	return restoreCmd
}

func newTrashCmd() *cobra.Command {
	trashCmd := &cobra.Command{
		Use:   "trash",
		Short: "Manage soft-deleted vault files",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List soft-deleted files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			entries, err := service.New(vRoot, db).TrashList()
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(entries)
			}
			if len(entries) == 0 {
				fmt.Println("trash is empty")
				return nil
			}
			for _, e := range entries {
				fmt.Printf("%s  %s  (deleted %s)\n", e.Name, e.OriginalPath, e.DeletedAt.Local().Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
	trashCmd.AddCommand(listCmd)

	restoreCmd := &cobra.Command{
		Use:   "restore [name]",
		Short: "Restore a soft-deleted file to its original location",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			entry, err := service.New(vRoot, db).TrashRestore(args[0])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "restored", "file": entry.OriginalPath})
		},
	}
	trashCmd.AddCommand(restoreCmd)

	purgeCmd := &cobra.Command{
		Use:   "purge",
		Short: "Permanently remove old items from the trash",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			days, _ := cmd.Flags().GetInt("older-than-days")
			all, _ := cmd.Flags().GetBool("all")
			if !cmd.Flags().Changed("older-than-days") {
				days = cfg.TrashRetentionDays
			}
			maxAge := time.Duration(days) * 24 * time.Hour
			if all {
				maxAge = 0
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			purged, err := service.New(vRoot, db).TrashPurge(maxAge)
			if err != nil {
				return err
			}
			return outputResult(map[string]interface{}{"status": "purged", "removed": purged})
		},
	}
	purgeCmd.Flags().Int("older-than-days", 0, "purge items deleted more than N days ago (default from config)")
	purgeCmd.Flags().Bool("all", false, "purge everything in the trash")
	trashCmd.AddCommand(purgeCmd)

	return trashCmd
}

func newNoteDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete [file]",
		Short: "Move a note to the vault trash (restorable via 'symdesk trash restore')",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			entry, err := service.New(vRoot, db).NoteDelete(args[0])
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "trashed", "file": entry.OriginalPath, "trash_name": entry.Name})
		},
	}
}
