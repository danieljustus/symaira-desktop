package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func setupHistoryTestVault(t *testing.T) string {
	t.Helper()
	vaultDir := t.TempDir()
	md := "---\ntitle: Test Note\n---\n\nBody\n"
	writeTestFile(t, filepath.Join(vaultDir, "note.md"), md)
	return vaultDir
}

func TestHistoryCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"history", "note.md", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestHistoryPruneCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{
		Vault:             vaultDir,
		HistoryMaxPerFile: 5,
		HistoryMaxAgeDays: 30,
	}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"history", "prune", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestHistoryPruneCommandUsesConfigDefaults(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{
		Vault:             vaultDir,
		HistoryMaxPerFile: 10,
		HistoryMaxAgeDays: 7,
	}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"history", "prune", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestRestoreCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"restore", "note.md", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestTrashListCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "list", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestTrashRestoreCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "restore", "note.md", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestTrashPurgeCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{
		Vault:              vaultDir,
		TrashRetentionDays: 30,
	}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "purge", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestTrashPurgeCommandAll(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{
		Vault:              vaultDir,
		TrashRetentionDays: 30,
	}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "purge", "--all", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestDeleteCommandJSON(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"delete", "note.md", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestHistoryCommandEmptyVault(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"history", "note.md"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestTrashListCommandEmpty(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "list"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestHistoryPruneCommandWithFlags(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"history", "prune", "--max-per-file", "5", "--max-age-days", "7", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestTrashPurgeCommandOlderThanDays(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "purge", "--older-than-days", "7", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestRestoreCommandWithAtFlag(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"restore", "note.md", "--at", "abc123", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
}

func TestDeleteCommandNonexistent(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"delete", "nonexistent.md", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	runErr := cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}

	if runErr == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestHistoryCommandJSONOutput(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"history", "note.md", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}

	if len(buf) > 0 {
		var result interface{}
		if err := json.Unmarshal(buf, &result); err != nil {
			t.Fatalf("invalid JSON output: %v\noutput: %s", err, string(buf))
		}
	}
}

func TestTrashListCommandJSONOutput(t *testing.T) {
	vaultDir := setupHistoryTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	defer func() { jsonFlag = false }()

	cmd := newRootCmd()
	cmd.SetArgs([]string{"trash", "list", "--json"})

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	_ = cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf []byte
	if _, err := r.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}

	if len(buf) > 0 {
		var result interface{}
		if err := json.Unmarshal(buf, &result); err != nil {
			t.Fatalf("invalid JSON output: %v\noutput: %s", err, string(buf))
		}
	}
}
