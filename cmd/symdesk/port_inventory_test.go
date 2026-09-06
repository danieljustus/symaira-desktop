package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const symdeskTreeFixtureRel = "../../testdata/port/cli/symdesk-command-tree.json"

func TestSymdeskCobraInventory(t *testing.T) {
	root := newRootCmd()
	oracle := inventory.Oracle{
		Commit:  "ae86331930fdfa2b128b68ae5af7437091b9949a",
		Release: "v0.12.2",
	}
	doc := buildCobraDocument(root, oracle)

	nonRootCount := 0
	for _, cmd := range doc.Commands {
		if cmd.Path != "symdesk" {
			nonRootCount++
		}
	}
	if nonRootCount != 206 {
		t.Fatalf("expected 206 non-root commands, got %d (total %d)", nonRootCount, len(doc.Commands))
	}

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal cobra document: %v", err)
	}
	content = append(content, '\n')

	fixturePath := filepath.Clean(symdeskTreeFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
		if err := os.WriteFile(fixturePath, content, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("Wrote %s with %d commands (%d non-root)", fixturePath, len(doc.Commands), nonRootCount)
		return
	}

	existing, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v (run make port-fixtures-generate to generate)", fixturePath, err)
	}
	if !bytes.Equal(existing, content) {
		t.Fatalf("SymDesk Cobra inventory has drifted from %s; run make port-fixtures-generate", fixturePath)
	}
}

func buildCobraDocument(root *cobra.Command, meta inventory.Oracle) inventory.CobraTreeDocument {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	groups := make([]inventory.Group, 0, len(root.Groups()))
	for _, g := range root.Groups() {
		groups = append(groups, inventory.Group{ID: g.ID, Title: g.Title})
	}
	commands := make([]inventory.CommandSpec, 0)
	collectCommands(root, &commands)
	sort.Slice(commands, func(i, j int) bool { return commands[i].Path < commands[j].Path })
	return inventory.CobraTreeDocument{
		SchemaVersion: 1,
		Oracle:        meta,
		Groups:        groups,
		Commands:      commands,
	}
}

func collectCommands(current *cobra.Command, commands *[]inventory.CommandSpec) {
	current.InitDefaultHelpFlag()
	current.SetOut(io.Discard)
	current.SetErr(io.Discard)

	spec := inventory.CommandSpec{
		Path:               current.CommandPath(),
		Name:               current.Name(),
		Use:                current.Use,
		Short:              current.Short,
		Long:               current.Long,
		Example:            current.Example,
		Aliases:            cloneStrings(current.Aliases),
		SuggestFor:         cloneStrings(current.SuggestFor),
		ValidArgs:          cloneStrings(current.ValidArgs),
		ArgAliases:         cloneStrings(current.ArgAliases),
		Hidden:             current.Hidden,
		Deprecated:         current.Deprecated,
		GroupID:            current.GroupID,
		Annotations:        cloneStringMap(current.Annotations),
		DisableFlagParsing: current.DisableFlagParsing,
		TraverseChildren:   current.TraverseChildren,
		Runnable:           current.Runnable(),
		HasSubcommands:     current.HasSubCommands(),
		LocalFlags:         collectFlags(current.LocalNonPersistentFlags()),
		PersistentFlags:    collectFlags(current.PersistentFlags()),
	}
	if current.Args != nil {
		spec.ArgumentCountProbes = probeArgumentCounts(current)
	}
	*commands = append(*commands, spec)
	for _, child := range current.Commands() {
		collectCommands(child, commands)
	}
}

func probeArgumentCounts(command *cobra.Command) []inventory.ArgProbe {
	probes := make([]inventory.ArgProbe, 0, 10)
	for count := 0; count <= 9; count++ {
		args := make([]string, count)
		for i := range args {
			args[i] = fmt.Sprintf("arg-%d", i+1)
		}
		probe := inventory.ArgProbe{Count: count}
		if err := command.Args(command, args); err != nil {
			probe.Error = err.Error()
		} else {
			probe.Accepted = true
		}
		probes = append(probes, probe)
	}
	return probes
}

func collectFlags(set *pflag.FlagSet) []inventory.FlagSpec {
	if set == nil {
		return nil
	}
	flags := make([]inventory.FlagSpec, 0, set.NFlag())
	set.VisitAll(func(item *pflag.Flag) {
		flags = append(flags, inventory.FlagSpec{
			Name:                item.Name,
			Shorthand:           item.Shorthand,
			Usage:               item.Usage,
			Type:                item.Value.Type(),
			Default:             item.DefValue,
			NoOptDefault:        item.NoOptDefVal,
			Hidden:              item.Hidden,
			Deprecated:          item.Deprecated,
			ShorthandDeprecated: item.ShorthandDeprecated,
			Annotations:         cloneStringSliceMap(item.Annotations),
		})
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneStringSliceMap(values map[string][]string) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string][]string, len(values))
	for key, value := range values {
		result[key] = cloneStrings(value)
	}
	return result
}

func TestBuildCobraDocumentIsDeterministic(t *testing.T) {
	oracle := inventory.Oracle{
		Commit:  "test-commit",
		Release: "v0.0.0",
	}
	first, err := json.Marshal(buildCobraDocument(newRootCmd(), oracle))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(buildCobraDocument(newRootCmd(), oracle))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("SymDesk Cobra document generation is not deterministic")
	}
}
