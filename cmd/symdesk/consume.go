package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newConsumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consume",
		Short: "Manage the consume (watched inbox) folder configuration",
	}

	cmd.AddCommand(newConsumeStatusCmd())
	cmd.AddCommand(newConsumeSetPathCmd())

	return cmd
}

func newConsumeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the configured consume folder path and status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			vRoot, err := vault.ResolveVaultRoot("", cfg)
			if err != nil {
				return fmt.Errorf("failed to resolve vault root: %w", err)
			}

			inboxDir := cfg.Inbox
			if inboxDir == "" {
				inboxDir = filepath.Join(vRoot, "inbox_watch")
			}

			exists := false
			if _, err := os.Stat(inboxDir); err == nil {
				exists = true
			}

			status := map[string]interface{}{
				"inbox_path":      inboxDir,
				"configured_path": cfg.Inbox,
				"exists":          exists,
				"vault_path":      vRoot,
			}

			if jsonFlag {
				b, _ := json.Marshal(status)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Inbox path: %s\n", inboxDir)
				fmt.Printf("Configured path: %s\n", cfg.Inbox)
				fmt.Printf("Directory exists: %v\n", exists)
			}
			return nil
		},
	}
}

func newConsumeSetPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-path [path]",
		Short: "Set the consume/watched inbox folder path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			newPath := args[0]
			cfg.Inbox = newPath

			configPath := config.GlobalPath()
			if err := config.Save(configPath, cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			if jsonFlag {
				result := map[string]string{"status": "ok", "inbox_path": newPath}
				b, _ := json.Marshal(result)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Consume folder path set to: %s\n", newPath)
			}
			return nil
		},
	}
}
