package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

// newRulesCmd implements the classification-rule CLI surface absorbed from
// symingest (issue #609): symdesk rules list|add|update|delete|test|dry-run.
// It is the last piece the macOS app's Rules screen needs to stop shelling
// out to the disabled symingest binary (#610).
func newRulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rules",
		Short:   "Manage document classification rules",
		GroupID: groupDocuments,
	}
	cmd.AddCommand(newRulesListCmd())
	cmd.AddCommand(newRulesAddCmd())
	cmd.AddCommand(newRulesUpdateCmd())
	cmd.AddCommand(newRulesDeleteCmd())
	cmd.AddCommand(newRulesTestCmd())
	cmd.AddCommand(newRulesDryRunCmd())
	return cmd
}

// rulesIngestOptions routes the global --vault flag into the ingest facade,
// so rules dry-run reads notes from the same vault `symdesk ingest` writes
// into rather than symingest's separately configured one.
func rulesIngestOptions() ingest.Options {
	return ingest.Options{Vault: cfg.Vault}
}

func newRulesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List classification rules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rules, err := ingest.Rules(cmd.Context(), rulesIngestOptions())
			if err != nil {
				return err
			}
			if rules == nil {
				rules = []ingest.Rule{}
			}
			if jsonFlag {
				return outputResult(rulesListResponse{SchemaVersion: ingest.SchemaVersion, Rules: rules})
			}
			for _, r := range rules {
				fmt.Printf("%d\t%s\t%s\t%s\n", r.ID, r.Kind, r.Pattern, r.Value)
			}
			return nil
		},
	}
}

func newRulesAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <pattern> <kind> <value>",
		Short: "Add a classification rule (kind: category|tag|correspondent|document_type)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			rule, err := ingest.AddRule(cmd.Context(), rulesIngestOptions(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(ruleResponse{SchemaVersion: ingest.SchemaVersion, Rule: *rule})
			}
			fmt.Printf("added rule %d: %s %q -> %q\n", rule.ID, rule.Kind, rule.Pattern, rule.Value)
			return nil
		},
	}
}

func newRulesUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update <id> <pattern> <kind> <value>",
		Short: "Update a classification rule",
		Args:  cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid rule id %q: %w", args[0], err)
			}
			rule, err := ingest.UpdateRule(cmd.Context(), rulesIngestOptions(), id, args[1], args[2], args[3])
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(ruleResponse{SchemaVersion: ingest.SchemaVersion, Rule: *rule})
			}
			fmt.Printf("updated rule %d: %s %q -> %q\n", rule.ID, rule.Kind, rule.Pattern, rule.Value)
			return nil
		},
	}
}

func newRulesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a classification rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid rule id %q: %w", args[0], err)
			}
			if err := ingest.DeleteRule(cmd.Context(), rulesIngestOptions(), id); err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(rulesDeleteResponse{SchemaVersion: ingest.SchemaVersion, ID: id, Deleted: true})
			}
			fmt.Printf("deleted rule %d\n", id)
			return nil
		},
	}
}

func newRulesTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <text>",
		Short: "Show which classification rules match the given text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			matches, err := ingest.TestRules(cmd.Context(), rulesIngestOptions(), args[0])
			if err != nil {
				return err
			}
			if matches == nil {
				matches = []ingest.RuleMatch{}
			}
			if jsonFlag {
				return outputResult(rulesTestResponse{SchemaVersion: ingest.SchemaVersion, Matches: matches})
			}
			for _, m := range matches {
				fmt.Printf("%d\t%s\t%s\t%s\n", m.ID, m.Kind, m.Pattern, m.Value)
			}
			return nil
		},
	}
}

func newRulesDryRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dry-run <pattern> <kind> <value>",
		Short: "Report which ingested documents a proposed rule would match, without saving it",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ingest.DryRunRule(cmd.Context(), rulesIngestOptions(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(rulesDryRunResponse{
					SchemaVersion: ingest.SchemaVersion,
					Operation:     "dry_run",
					DryRunResult:  *result,
				})
			}
			fmt.Printf("dry-run: %d/%d documents matched (%d skipped)\n", result.MatchedDocuments, result.TotalDocuments, result.SkippedDocuments)
			for _, m := range result.Matches {
				fmt.Printf("  match: %d %s (%s)\n", m.DocumentID, m.Title, m.NotePath)
			}
			for _, s := range result.Skipped {
				fmt.Printf("  skip: %d %s (%s)\n", s.DocumentID, s.NotePath, s.Reason)
			}
			return nil
		},
	}
}

type rulesListResponse struct {
	SchemaVersion int           `json:"schema_version"`
	Rules         []ingest.Rule `json:"rules"`
}

type ruleResponse struct {
	SchemaVersion int         `json:"schema_version"`
	Rule          ingest.Rule `json:"rule"`
}

type rulesTestResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Matches       []ingest.RuleMatch `json:"matches"`
}

type rulesDeleteResponse struct {
	SchemaVersion int   `json:"schema_version"`
	ID            int64 `json:"id"`
	Deleted       bool  `json:"deleted"`
}

type rulesDryRunResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Operation     string `json:"operation"`
	ingest.DryRunResult
}
