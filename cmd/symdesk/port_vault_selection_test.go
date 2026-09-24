package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const vaultSelectionFixture = "testdata/port/cli/config-vault-selection.json"

type vaultSelectionCase struct {
	ID        string `json:"id"`
	Flag      string `json:"flag,omitempty"`
	EmptyFlag bool   `json:"empty_flag,omitempty"`
	Env       string `json:"env,omitempty"`
	Selected  string `json:"selected"`
}

type vaultSelectionFixtureData struct {
	SchemaVersion int                  `json:"schema_version"`
	OracleCommit  string               `json:"oracle_commit"`
	Cases         []vaultSelectionCase `json:"cases"`
}

func TestPortVaultSelectionCLIContract(t *testing.T) {
	fixture := observeVaultSelection(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../", vaultSelectionFixture)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("vault selection fixture is stale; regenerate through portgen")
	}
}

func observeVaultSelection(t *testing.T) vaultSelectionFixtureData {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	for _, path := range []string{home, configHome, dataHome} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	paths := map[string]string{}
	for _, alias := range []string{"toml", "env", "flag"} {
		vault := filepath.Join(root, alias)
		if err := os.MkdirAll(vault, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vault, alias+".md"), []byte("# "+alias+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[alias] = vault
	}
	configDir := filepath.Join(configHome, "symdesk")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("vault = "+strconv.Quote(paths["toml"])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []vaultSelectionCase{
		{ID: "toml-vault-alone", Selected: "toml"},
		{ID: "environment-over-toml", Env: "env", Selected: "env"},
		{ID: "flag-over-environment-and-toml", Flag: "flag", Env: "env", Selected: "flag"},
		{ID: "empty-flag-falls-back-to-environment", EmptyFlag: true, Env: "env", Selected: "env"},
	}
	for _, testCase := range cases {
		args := []string{}
		if testCase.Flag != "" {
			args = append(args, "--vault", paths[testCase.Flag])
		} else if testCase.EmptyFlag {
			args = append(args, "--vault=")
		}
		args = append(args, "ls", "--json")
		commandArgs := append([]string{"-test.run=^TestVaultSelectionHelper$", "--"}, args...)
		command := exec.Command(os.Args[0], commandArgs...) //nolint:gosec // reruns this test binary with fixed test arguments
		command.Env = cleanVaultSelectionEnv(os.Environ())
		command.Env = append(command.Env,
			"SYMDESK_PORT_VAULT_HOME="+home,
			"SYMDESK_PORT_VAULT_CONFIG_HOME="+configHome,
			"SYMDESK_PORT_VAULT_DATA_HOME="+dataHome,
			"SYMDESK_PORT_VAULT_HELPER=1",
		)
		if testCase.Env != "" {
			command.Env = append(command.Env, "SYMDESK_PORT_VAULT_ENV="+paths[testCase.Env])
		}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("case %s: Go CLI failed: %v\n%s", testCase.ID, err, output)
		}
		if !strings.Contains(string(output), testCase.Selected+".md") {
			t.Fatalf("case %s: output %s does not identify selected vault %q", testCase.ID, output, testCase.Selected)
		}
	}
	return vaultSelectionFixtureData{SchemaVersion: 1, OracleCommit: "e023816a9db2b3d71514049195886fe1b9766a5a", Cases: cases}
}

func TestVaultSelectionHelper(t *testing.T) {
	if os.Getenv("SYMDESK_PORT_VAULT_HELPER") != "1" {
		return
	}
	for name, value := range map[string]string{
		"HOME":            os.Getenv("SYMDESK_PORT_VAULT_HOME"),
		"USERPROFILE":     os.Getenv("SYMDESK_PORT_VAULT_HOME"),
		"XDG_CONFIG_HOME": os.Getenv("SYMDESK_PORT_VAULT_CONFIG_HOME"),
		"XDG_DATA_HOME":   os.Getenv("SYMDESK_PORT_VAULT_DATA_HOME"),
		"SYMDESK_VAULT":   os.Getenv("SYMDESK_PORT_VAULT_ENV"),
	} {
		if err := os.Setenv(name, value); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	cobra.OnInitialize(initConfig)
	cmd := newRootCmd()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func cleanVaultSelectionEnv(environment []string) []string {
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "SYMDESK_VAULT", "SYMDESK_PORT_VAULT_HELPER", "SYMDESK_PORT_VAULT_HOME", "SYMDESK_PORT_VAULT_CONFIG_HOME", "SYMDESK_PORT_VAULT_DATA_HOME", "SYMDESK_PORT_VAULT_ENV":
			continue
		}
		clean = append(clean, item)
	}
	return clean
}
