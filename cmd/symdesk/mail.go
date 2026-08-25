package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
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
	cmd.AddCommand(newMailRulesCmd())

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
			defer func() { _ = db.Close() }()

			svc := service.New(vRoot, db)
			configPath := filepath.Join(os.Getenv("HOME"), ".config", "symingest", "config.toml")

			w, err := mail.NewWithInterval(configPath, svc, 0)
			if err != nil {
				return fmt.Errorf("failed to create mail watcher: %w", err)
			}

			// The status command doesn't start the watcher — it just
			// surfaces the list of configured accounts and their current
			// state by reading the configured mail accounts once.
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
			defer func() { _ = db.Close() }()

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

// newMailRulesCmd implements the mail-account CLI surface absorbed from
// symingest (issue #609): symdesk mail rules list|create|update|delete. It
// is the config-management counterpart to `mail status`/`mail fetch`.
func newMailRulesCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Manage configured IMAP mail accounts",
	}
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "mail configuration file (defaults to .symingest.toml or the global config)")

	cmd.AddCommand(newMailRulesListCmd(&configPath))
	cmd.AddCommand(newMailRulesCreateCmd(&configPath))
	cmd.AddCommand(newMailRulesUpdateCmd(&configPath))
	cmd.AddCommand(newMailRulesDeleteCmd(&configPath))
	return cmd
}

func newMailRulesListCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured IMAP mail accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := ingest.ListMailAccounts(*configPath)
			if err != nil {
				return err
			}
			return outputMailRulesResult("list", result)
		},
	}
}

func newMailRulesCreateCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "create",
		Short: "Add an IMAP mail account, reading its JSON from stdin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var account ingest.MailAccountRecord
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&account); err != nil {
				return fmt.Errorf("read account JSON from stdin: %w", err)
			}
			result, err := ingest.CreateMailAccount(*configPath, account)
			if err != nil {
				return err
			}
			return outputMailRulesResult("create", result)
		},
	}
}

func newMailRulesUpdateCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "update <id>",
		Short: "Update an IMAP mail account, reading its JSON from stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var account ingest.MailAccountRecord
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&account); err != nil {
				return fmt.Errorf("read account JSON from stdin: %w", err)
			}
			result, err := ingest.UpdateMailAccount(*configPath, args[0], account)
			if err != nil {
				return err
			}
			return outputMailRulesResult("update", result)
		},
	}
}

func newMailRulesDeleteCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an IMAP mail account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ingest.DeleteMailAccount(*configPath, args[0])
			if err != nil {
				return err
			}
			return outputMailRulesResult("delete", result)
		},
	}
}

// outputMailRulesResult prints a mail-account result. The JSON path marshals
// here rather than through outputResult so the CodeQL suppression below stays
// scoped to this one command group instead of blanketing every command's
// output. Every account has come back through ingest's masking view, so a
// stored plaintext password reads as the mask and only a non-secret reference
// (symvault://…) is ever printed verbatim — which the app needs in order to
// tell a configured account from an unconfigured one.
func outputMailRulesResult(operation string, result *ingest.MailConfigurationResult) error {
	if jsonFlag {
		encoded, err := json.Marshal(mailRulesResponse{
			SchemaVersion:           ingest.SchemaVersion,
			Operation:               operation,
			MailConfigurationResult: *result,
		})
		if err != nil {
			return err
		}
		// codeql[go/clear-text-logging]
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("%s: %d account(s) at %s\n", operation, len(result.Accounts), result.ConfigPath)
	for _, w := range result.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	for _, a := range result.Accounts {
		fmt.Printf("  %s\t%s@%s:%d/%s\n", a.ID, a.Username, a.Host, a.Port, a.Folder)
	}
	return nil
}

type mailRulesResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Operation     string `json:"operation"`
	ingest.MailConfigurationResult
}
