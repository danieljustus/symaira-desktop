package main

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/spf13/cobra"
)

func TestOutputResultJSON(t *testing.T) {
	jsonFlag = true
	defer func() { jsonFlag = false }()

	data := map[string]string{"status": "ok"}
	err := outputResult(data)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutputResultPrettyPrint(t *testing.T) {
	jsonFlag = false
	data := map[string]string{"status": "ok"}
	err := outputResult(data)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutputStreamJSON(t *testing.T) {
	jsonFlag = true
	defer func() { jsonFlag = false }()

	ch := make(chan interface{}, 2)
	ch <- map[string]string{"a": "1"}
	ch <- map[string]string{"b": "2"}
	close(ch)

	err := outputStream(ch)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutputStreamPrettyPrint(t *testing.T) {
	jsonFlag = false
	ch := make(chan interface{}, 1)
	ch <- "hello"
	close(ch)

	err := outputStream(ch)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInitServiceDepsNoVault(t *testing.T) {
	origCfg := cfg
	cfg = &config.Config{Vault: ""}
	defer func() { cfg = origCfg }()

	_, _, err := initServiceDeps()
	if err == nil {
		t.Error("expected error for unconfigured vault")
	}
}

func TestInitServiceDepsWithVault(t *testing.T) {
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	defer func() { cfg = origCfg }()

	vRoot, db, err := initServiceDeps()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer closeWithWarning("sidecar database", db.Close)

	if vRoot != vaultDir {
		t.Errorf("expected vault root %q, got %q", vaultDir, vRoot)
	}
}

func TestRegisterCommandsStructures(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)

	expectedCommands := []string{"docs", "doc", "similar", "demo", "recipe"}
	for _, name := range expectedCommands {
		found := false
		for _, cmd := range rootCmd.Commands() {
			if cmd.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected command %q to be registered", name)
		}
	}
}

func TestDocsListCommandFlagParsing(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)

	var docsListCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "docs" {
			for _, sub := range cmd.Commands() {
				if sub.Name() == "list" {
					docsListCmd = sub
					break
				}
			}
			break
		}
	}
	if docsListCmd == nil {
		t.Fatal("docs list command not found")
	}

	flags := docsListCmd.Flags()
	typeFlag, _ := flags.GetString("type")
	if typeFlag != "" {
		t.Errorf("expected empty type flag, got %q", typeFlag)
	}
	if asn, _ := flags.GetInt("asn"); asn != 0 {
		t.Errorf("expected ASN filter default 0, got %d", asn)
	}

	var docCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "doc" {
			docCmd = cmd
			break
		}
	}
	if docCmd == nil {
		t.Fatal("doc command not found")
	}
	if _, _, err := docCmd.Find([]string{"asn"}); err != nil {
		t.Fatalf("expected doc asn command: %v", err)
	}
}

func TestSimilarCommandFlagDefaults(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)

	var similarCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "similar" {
			similarCmd = cmd
			break
		}
	}
	if similarCmd == nil {
		t.Fatal("similar command not found")
	}

	threshold, _ := similarCmd.Flags().GetInt("threshold")
	if threshold != service.DefaultDuplicateThreshold {
		t.Errorf("expected default threshold %d, got %d", service.DefaultDuplicateThreshold, threshold)
	}
}

func TestDemoInitCommandRegistration(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	registerCommands(rootCmd)

	var demoCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "demo" {
			demoCmd = cmd
			break
		}
	}
	if demoCmd == nil {
		t.Fatal("demo command not found")
	}

	found := false
	for _, sub := range demoCmd.Commands() {
		if sub.Name() == "init" {
			found = true
			break
		}
	}
	if !found {
		t.Error("demo init subcommand not found")
	}
}
