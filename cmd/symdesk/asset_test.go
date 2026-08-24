package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func TestAssetStoreCLI(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vaultRoot := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultRoot}
	t.Cleanup(func() { cfg = origCfg })

	// Create a source file to store
	srcFile := filepath.Join(t.TempDir(), "diagram.svg")
	if err := os.WriteFile(srcFile, []byte("<svg></svg>"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := runRootCmd(t, []string{"asset", "store", srcFile, "--json"})
	if err != nil {
		t.Fatalf("asset store command failed: %v", err)
	}

	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("failed to decode JSON output: %v (raw: %s)", err, out)
	}

	if res["path"] != "assets/diagram.svg" {
		t.Errorf("expected assets/diagram.svg, got %q", res["path"])
	}
	if res["markdown_link"] != "![diagram.svg](assets/diagram.svg)" {
		t.Errorf("expected ![diagram.svg](assets/diagram.svg), got %q", res["markdown_link"])
	}

	// Verify file was actually written in the vault assets folder
	storedPath := filepath.Join(vaultRoot, "assets", "diagram.svg")
	if _, err := os.Stat(storedPath); err != nil {
		t.Errorf("expected file to exist at %s: %v", storedPath, err)
	}
}
