package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/spf13/cobra"
)

// findSubcommand locates a direct child command by name, failing the test
// if it isn't registered.
func findSubcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("subcommand %q not found under %q", name, parent.Name())
	return nil
}

func withTestVault(t *testing.T) string {
	t.Helper()
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })
	return vaultDir
}

func TestNotebookCLI_NewListShowAddRemoveDelete(t *testing.T) {
	withTestVault(t)
	notebookCmd := newNotebookCmd()

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	newCmd := findSubcommand(t, notebookCmd, "new")
	newCmd.Flags().Set("description", "notes on X")
	out, err := runCommand(t, newCmd, []string{"Research X"})
	if err != nil {
		t.Fatalf("notebook new: %v (out=%s)", err, out)
	}
	var created map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &created); err != nil {
		t.Fatalf("unmarshal notebook new output: %v (out=%s)", err, out)
	}
	if created["id"] != "research-x" {
		t.Errorf("created id = %v, want research-x", created["id"])
	}

	listCmd := findSubcommand(t, notebookCmd, "list")
	out, err = runCommand(t, listCmd, nil)
	if err != nil {
		t.Fatalf("notebook list: %v", err)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &list); err != nil {
		t.Fatalf("unmarshal notebook list output: %v (out=%s)", err, out)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}

	addCmd := findSubcommand(t, notebookCmd, "add-source")
	out, err = runCommand(t, addCmd, []string{"research-x", "../../etc/passwd"})
	if err == nil {
		t.Fatalf("expected add-source to reject a traversal path, got output: %s", out)
	}

	showCmd := findSubcommand(t, notebookCmd, "show")
	out, err = runCommand(t, showCmd, []string{"research-x"})
	if err != nil {
		t.Fatalf("notebook show: %v", err)
	}
	var shown map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &shown); err != nil {
		t.Fatalf("unmarshal notebook show output: %v (out=%s)", err, out)
	}
	if shown["title"] != "Research X" {
		t.Errorf("shown title = %v, want Research X", shown["title"])
	}

	deleteCmd := findSubcommand(t, notebookCmd, "delete")
	out, err = runCommand(t, deleteCmd, []string{"research-x"})
	if err != nil {
		t.Fatalf("notebook delete: %v (out=%s)", err, out)
	}

	showCmd2 := findSubcommand(t, notebookCmd, "show")
	if _, err := runCommand(t, showCmd2, []string{"research-x"}); err == nil {
		t.Fatal("expected notebook show to fail after delete")
	}
}

func TestNotebookCLI_RegisteredOnRootCommand(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)

	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "notebook" {
			found = true
			expected := []string{"new", "list", "show", "add-source", "remove-source", "delete"}
			for _, name := range expected {
				findSubcommand(t, c, name)
			}
		}
	}
	if !found {
		t.Fatal("notebook command not registered on root")
	}
}

func TestNotebookCLI_RequiresVaultConfigured(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = origCfg })

	newCmd := findSubcommand(t, newNotebookCmd(), "new")
	if _, err := runCommand(t, newCmd, []string{"X"}); err == nil {
		t.Fatal("expected an error when no vault is configured")
	}
}

// verify the vault package's SecurePath rejection surfaces through the CLI
// error path rather than being silently swallowed.
func TestNotebookCLI_AddSourceErrorSurfacesToUser(t *testing.T) {
	withTestVault(t)
	notebookCmd := newNotebookCmd()
	jsonFlag = false

	newCmd := findSubcommand(t, notebookCmd, "new")
	if _, err := runCommand(t, newCmd, []string{"Vault Escape Test"}); err != nil {
		t.Fatal(err)
	}

	addCmd := findSubcommand(t, notebookCmd, "add-source")
	_, err := runCommand(t, addCmd, []string{"vault-escape-test", "../../../etc/somefile.md"})
	if err == nil {
		t.Fatal("expected an error adding a source path resolved outside the vault")
	}
	if !strings.Contains(err.Error(), "traversal") && !strings.Contains(err.Error(), "outside") {
		t.Errorf("expected a traversal/outside-vault error, got: %v", err)
	}
}
