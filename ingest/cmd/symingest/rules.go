package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

func runRules(args []string) error {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output rules in JSON format")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "rules [flags] [command]", "Manage classification rules. Patterns are case-insensitive substrings matched against extracted document text, not filename globs.\n\nCommands:\n  list                                  List all classification rules\n  add <pattern> <kind> <value>          Add a classification rule\n  update <id> <pattern> <kind> <value>  Update a classification rule\n  test <text>                           Test rules against text\n  dry-run <pattern> <kind> <value>      Test a proposed rule against existing notes\n  delete <id>                           Delete a classification rule by ID\n\nKinds for add/update command: category, tag, correspondent, document_type")
	help, err := parseFlags(fs, args, "invalid rules flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		fs.Usage()
		return nil
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to open document store")
	}
	defer st.Close()

	ctx := context.Background()

	switch remaining[0] {
	case "list":
		return listRules(ctx, st, *jsonFlag)
	case "add":
		return addRule(ctx, st, remaining[1:], *jsonFlag)
	case "update":
		return updateRule(ctx, st, remaining[1:], *jsonFlag)
	case "test":
		return testRules(ctx, st, remaining[1:], *jsonFlag)
	case "dry-run":
		if len(remaining) < 4 {
			return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
				"missing arguments; usage: symingest rules dry-run <pattern> <kind> <value>")
		}
		if cfg.vault == "" {
			return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig,
				"no vault configured; use --vault or set vault in config")
		}
		response, err := dryRunRuleAgainstDocuments(ctx, st, cfg.vault, remaining[1], remaining[2], remaining[3])
		if err != nil {
			return err
		}
		if *jsonFlag {
			return printRulesJSON(response, "failed to marshal rules dry-run result")
		}
		formatDryRunHuman(response)
		return nil
	case "delete":
		return deleteRule(ctx, st, remaining[1:], *jsonFlag)
	default:
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation,
			"unknown rules subcommand %q", remaining[0])
	}
}

func printRulesUsage() error {
	fmt.Fprintln(stdout, `Usage: symingest rules [flags] [command]

Commands:
  list                                  List all classification rules
  add <pattern> <kind> <value>          Add a classification rule
  update <id> <pattern> <kind> <value>  Update a classification rule
  test <text>                           Test rules against text
  dry-run <pattern> <kind> <value>      Test a proposed rule against existing notes
  delete <id>                           Delete a classification rule by ID

Patterns are case-insensitive substrings matched against extracted document text, not filename globs.
Kinds for add command: category, tag, correspondent, document_type`)
	return nil
}

func listRules(ctx context.Context, st *store.Store, outputJSON bool) error {
	rules, err := st.ListRules(ctx)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to list rules")
	}

	if outputJSON {
		if rules == nil {
			rules = []*store.ClassificationRule{}
		}
		return printRulesJSON(rulesListJSONResponse{
			SchemaVersion: rulesJSONSchemaVersion,
			Rules:         rules,
		}, "failed to marshal rules to JSON")
	}

	if len(rules) == 0 {
		fmt.Fprintln(stdout, "No classification rules defined.")
		return nil
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tPATTERN\tKIND\tVALUE\tCREATED AT")
	for _, r := range rules {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
			r.ID, r.Pattern, r.Kind, r.Value, r.CreatedAt)
	}
	w.Flush()
	return nil
}

func addRule(ctx context.Context, st *store.Store, args []string, outputJSON bool) error {
	if len(args) < 3 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"missing arguments; usage: symingest rules add <pattern> <kind> <value>")
	}

	pattern := args[0]
	kind := args[1]
	value := args[2]

	rule, err := st.AddRule(ctx, pattern, kind, value)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to add rule")
	}

	if outputJSON {
		return printRulesJSON(rulesRuleJSONResponse{
			SchemaVersion: rulesJSONSchemaVersion,
			Rule:          rule,
		}, "failed to marshal added rule to JSON")
	}
	fmt.Fprintf(stdout, "Added classification rule %d: pattern=%q, kind=%q, value=%q\n",
		rule.ID, rule.Pattern, rule.Kind, rule.Value)
	return nil
}

func updateRule(ctx context.Context, st *store.Store, args []string, outputJSON bool) error {
	if len(args) < 4 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"missing arguments; usage: symingest rules update <id> <pattern> <kind> <value>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return exitcodes.Wrapf(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid rule ID %q; must be an integer", args[0])
	}
	rule, err := st.UpdateRule(ctx, id, args[1], args[2], args[3])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to update rule")
	}
	if outputJSON {
		return printRulesJSON(rulesRuleJSONResponse{
			SchemaVersion: rulesJSONSchemaVersion,
			Rule:          rule,
		}, "failed to marshal updated rule to JSON")
	}
	fmt.Fprintf(stdout, "Updated classification rule %d: pattern=%q, kind=%q, value=%q\n",
		rule.ID, rule.Pattern, rule.Kind, rule.Value)
	return nil
}

type ruleTestMatch struct {
	ID      int64  `json:"id"`
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

func testRules(ctx context.Context, st *store.Store, args []string, outputJSON bool) error {
	if len(args) < 1 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"missing text; usage: symingest rules test <text>")
	}
	text := strings.ToLower(strings.Join(args, " "))
	rules, err := st.ListRules(ctx)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to list rules")
	}
	var matches []ruleTestMatch
	for _, r := range rules {
		if strings.Contains(text, strings.ToLower(r.Pattern)) {
			matches = append(matches, ruleTestMatch{ID: r.ID, Pattern: r.Pattern, Kind: r.Kind, Value: r.Value})
		}
	}
	if outputJSON {
		if matches == nil {
			matches = []ruleTestMatch{}
		}
		return printRulesJSON(rulesTestJSONResponse{
			SchemaVersion: rulesJSONSchemaVersion,
			Matches:       matches,
		}, "failed to marshal rule test result")
	}
	if len(matches) == 0 {
		fmt.Fprintln(stdout, "No matching classification rules.")
		return nil
	}
	for _, m := range matches {
		fmt.Fprintf(stdout, "match rule %d: pattern=%q kind=%q value=%q\n", m.ID, m.Pattern, m.Kind, m.Value)
	}
	return nil
}

func deleteRule(ctx context.Context, st *store.Store, args []string, outputJSON bool) error {
	if len(args) < 1 {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
			"missing rule ID; usage: symingest rules delete <id>")
	}

	idStr := args[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return exitcodes.Wrapf(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid rule ID %q; must be an integer", idStr)
	}

	if err := st.DeleteRule(ctx, id); err != nil {
		return exitcodes.Wrapf(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"failed to delete rule %d", id)
	}

	if outputJSON {
		return printRulesJSON(rulesDeleteJSONResponse{
			SchemaVersion: rulesJSONSchemaVersion,
			ID:            id,
			Deleted:       true,
		}, "failed to marshal deleted rule result to JSON")
	}
	fmt.Fprintf(stdout, "Deleted classification rule %d.\n", id)
	return nil
}
