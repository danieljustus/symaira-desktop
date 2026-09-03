package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newConflictCmd() *cobra.Command {
	conflictCmd := &cobra.Command{
		Use:   "conflict",
		Short: "Manage iCloud/Obsidian sync conflicts",
	}

	conflictResolveCmd := &cobra.Command{
		Use:   "resolve [path]",
		Short: "Resolve a sync conflict file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conflictPath := args[0]
			action, _ := cmd.Flags().GetString("action")
			if action != "keep-mine" && action != "keep-theirs" {
				return fmt.Errorf("invalid action: must be keep-mine or keep-theirs")
			}

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)

			var absConflict string
			if filepath.IsAbs(conflictPath) {
				absConflict = conflictPath
			} else {
				absConflict = filepath.Join(vRoot, conflictPath)
			}

			if _, err := os.Stat(absConflict); err != nil {
				return fmt.Errorf("conflict file not found: %w", err)
			}

			originalPath := deriveOriginalPath(absConflict)

			switch action {
			case "keep-mine":
				if err := os.Remove(absConflict); err != nil {
					return fmt.Errorf("failed to remove conflict file: %w", err)
				}
			case "keep-theirs":
				// The conflict path is an explicit command argument, not an indirect
				// path assembled from untrusted document metadata.
				//nolint:gosec // documented CLI input path for conflict resolution
				content, err := os.ReadFile(absConflict)
				if err != nil {
					return fmt.Errorf("failed to read conflict file: %w", err)
				}
				// The destination is derived from the same explicit conflict path.
				//nolint:gosec // documented CLI input path for conflict resolution
				if err := os.WriteFile(originalPath, content, 0600); err != nil {
					return fmt.Errorf("failed to write original file: %w", err)
				}
				if err := os.Remove(absConflict); err != nil {
					return fmt.Errorf("failed to remove conflict file: %w", err)
				}
			}

			svc := service.New(vRoot, db)
			_ = svc.DeleteDocument(absConflict)

			if action == "keep-theirs" {
				if doc, err := vault.ParseFile(originalPath); err == nil {
					_ = svc.IndexDocument(doc)
				}
			}

			res := map[string]interface{}{
				"status":        "resolved",
				"action":        action,
				"original_path": originalPath,
				"conflict_path": absConflict,
			}
			return outputResult(res)
		},
	}
	conflictResolveCmd.Flags().String("action", "", "resolution action (keep-mine|keep-theirs)")
	markFlagRequired(conflictResolveCmd, "action")
	conflictCmd.AddCommand(conflictResolveCmd)

	return conflictCmd
}

func deriveOriginalPath(conflictPath string) string {
	dir := filepath.Dir(conflictPath)
	base := filepath.Base(conflictPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	if strings.HasSuffix(name, " conflicted copy") {
		name = strings.TrimSuffix(name, " conflicted copy")
	} else if strings.HasSuffix(name, " 2") {
		name = strings.TrimSuffix(name, " 2")
	}

	return filepath.Join(dir, name+ext)
}
