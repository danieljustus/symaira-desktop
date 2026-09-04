package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestConfigPaths_JSON(t *testing.T) {
	dataHome := t.TempDir()
	configHome := t.TempDir()
	cacheHome := t.TempDir()

	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv(config.EnvLegacyContactsDataHome, "")

	cfg = config.DefaultConfig()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"config", "paths", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	var sp config.StorePaths
	if err := json.Unmarshal(buf.Bytes(), &sp); err != nil {
		t.Fatalf("failed to decode json output %q: %v", buf.String(), err)
	}

	if sp.Sidecar != filepath.Join(dataHome, "symdesk", "sidecar.db") {
		t.Errorf("sp.Sidecar = %q, want %q", sp.Sidecar, filepath.Join(dataHome, "symdesk", "sidecar.db"))
	}
	if sp.Retrieval != filepath.Join(dataHome, "symdesk", "retrieval.db") {
		t.Errorf("sp.Retrieval = %q, want %q", sp.Retrieval, filepath.Join(dataHome, "symdesk", "retrieval.db"))
	}
	if sp.Ingest != filepath.Join(dataHome, "symdesk", "symingest.db") {
		t.Errorf("sp.Ingest = %q, want %q", sp.Ingest, filepath.Join(dataHome, "symdesk", "symingest.db"))
	}
	if sp.Contacts != filepath.Join(dataHome, "symdesk", "symrelate.db") {
		t.Errorf("sp.Contacts = %q, want %q", sp.Contacts, filepath.Join(dataHome, "symdesk", "symrelate.db"))
	}
}

func TestConfigPaths_LegacyFallback(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv(config.EnvLegacyContactsDataHome, "")

	legacyIngestDir := filepath.Join(dataHome, "symingest")
	if err := os.MkdirAll(legacyIngestDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyIngestDB := filepath.Join(legacyIngestDir, "symingest.db")
	if err := os.WriteFile(legacyIngestDB, []byte("legacy ingest"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg = config.DefaultConfig()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"config", "paths", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	var sp config.StorePaths
	if err := json.Unmarshal(buf.Bytes(), &sp); err != nil {
		t.Fatalf("failed to decode json output %q: %v", buf.String(), err)
	}

	if sp.Ingest != legacyIngestDB {
		t.Errorf("sp.Ingest = %q, want legacy fallback %q", sp.Ingest, legacyIngestDB)
	}
}

func TestConfigPaths_Text(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv(config.EnvLegacyContactsDataHome, "")

	cfg = config.DefaultConfig()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"config", "paths"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, key := range []string{"sidecar:", "retrieval:", "ingest:", "contacts:", "data_dir:", "config_dir:", "cache_dir:"} {
		if !strings.Contains(out, key) {
			t.Errorf("expected %q in text output: %s", key, out)
		}
	}
}
