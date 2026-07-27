package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/mail"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/spf13/cobra"
)

func newMailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail",
		Short: "Manage IMAP mail ingestion status and configuration",
	}

	cmd.AddCommand(newMailStatusCmd())
	cmd.AddCommand(newMailFetchCmd())

	return cmd
}

func newMailStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current mail ingestion status for all configured accounts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()

			svc := service.New(vRoot, db)
			configPath := filepath.Join(os.Getenv("HOME"), ".config", "symingest", "config.toml")

			w, err := mail.NewWithInterval(configPath, svc, 0)
			if err != nil {
				return fmt.Errorf("failed to create mail watcher: %w", err)
			}

			// The status command doesn't start the watcher — it just
			// surfaces the list of configured accounts and their current
			// state by running a one-shot symingest mail list.
			accounts, err := mail.ListConfiguredAccounts(configPath)
			if err != nil {
				return fmt.Errorf("failed to list mail accounts: %w", err)
			}

			statuses := w.Statuses()

			output := map[string]interface{}{
				"accounts": accounts,
				"statuses": statuses,
			}
			b, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func newMailFetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch",
		Short: "Trigger an immediate mail fetch for all configured accounts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()

			svc := service.New(vRoot, db)
			configPath := filepath.Join(os.Getenv("HOME"), ".config", "symingest", "config.toml")

			w, err := mail.NewWithInterval(configPath, svc, 0)
			if err != nil {
				return fmt.Errorf("failed to create mail watcher: %w", err)
			}

			// Run a single fetch cycle.
			mail.RunOnce(w, configPath, svc)

			statuses := w.Statuses()
			b, _ := json.MarshalIndent(statuses, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}
