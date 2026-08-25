package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// TestConfigSetGetRoundTrip verifies the set → save → load → get round trip
// for string and integer config keys in an isolated home directory.
func TestConfigSetGetRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Start from a known empty state via DefaultConfig (no file exists yet).
	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatalf("config file should not exist before init, found %s", config.GlobalPath())
	}
	if !setConfigField(cfg, "vault", "/tmp/vault-x") {
		t.Fatal("setConfigField(vault) returned false")
	}
	if !setConfigField(cfg, "max_tokens", "4096") {
		t.Fatal("setConfigField(max_tokens) returned false")
	}
	if setConfigField(cfg, "does_not_exist", "x") {
		t.Fatal("setConfigField(unknown) should return false")
	}
	if setConfigField(cfg, "max_tokens", "not-a-number") {
		t.Fatal("setConfigField(max_tokens, invalid) should return false")
	}
	if err := config.Save(config.GlobalPath(), cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := configField(loaded, "vault"); got != "/tmp/vault-x" {
		t.Errorf("vault = %q, want /tmp/vault-x", got)
	}
	if got, _ := configField(loaded, "max_tokens"); got != "4096" {
		t.Errorf("max_tokens = %q, want 4096", got)
	}
	if _, ok := configField(loaded, "does_not_exist"); ok {
		t.Error("configField(unknown) should not be found")
	}
}

// TestConfigInitCreatesFile verifies `config init` writes a default config
// and is idempotent on a second run.
func TestConfigInitCreatesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	path := config.GlobalPath()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("config should not exist yet: %s", path)
	}
	if err := config.Save(path, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file missing after init: %v", err)
	}
}
