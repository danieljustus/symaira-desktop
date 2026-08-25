package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

// isolateIngestConfig points the absorbed ingest pipeline's own configuration
// (~/.config/symingest, ~/.local/share/symingest) at a throwaway directory,
// so these tests never touch the developer's real document store.
func isolateIngestConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

func TestRulesAddListTestDeleteJSON(t *testing.T) {
	isolateIngestConfig(t)
	origCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	addCmd := newRulesAddCmd()
	addCmd.SetContext(context.Background())
	addOut, err := runCommand(t, addCmd, []string{"invoice", "category", "Invoices"})
	if err != nil {
		t.Fatalf("rules add failed: %v", err)
	}
	var addResp struct {
		SchemaVersion int         `json:"schema_version"`
		Rule          ingest.Rule `json:"rule"`
	}
	if err := json.Unmarshal([]byte(addOut), &addResp); err != nil {
		t.Fatalf("rules add output is not valid JSON: %v\noutput: %s", err, addOut)
	}
	if addResp.SchemaVersion != ingest.SchemaVersion || addResp.Rule.ID == 0 || addResp.Rule.CreatedAt == "" {
		t.Fatalf("unexpected rules add response: %+v", addResp)
	}

	listCmd := newRulesListCmd()
	listCmd.SetContext(context.Background())
	listOut, err := runCommand(t, listCmd, nil)
	if err != nil {
		t.Fatalf("rules list failed: %v", err)
	}
	var listResp struct {
		SchemaVersion int           `json:"schema_version"`
		Rules         []ingest.Rule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(listOut), &listResp); err != nil {
		t.Fatalf("rules list output is not valid JSON: %v\noutput: %s", err, listOut)
	}
	if len(listResp.Rules) != 1 || listResp.Rules[0].ID != addResp.Rule.ID {
		t.Fatalf("unexpected rules list response: %+v", listResp)
	}

	testCmd := newRulesTestCmd()
	testCmd.SetContext(context.Background())
	testOut, err := runCommand(t, testCmd, []string{"This is an INVOICE."})
	if err != nil {
		t.Fatalf("rules test failed: %v", err)
	}
	var testResp struct {
		Matches []map[string]interface{} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(testOut), &testResp); err != nil {
		t.Fatalf("rules test output is not valid JSON: %v\noutput: %s", err, testOut)
	}
	if len(testResp.Matches) != 1 {
		t.Fatalf("expected 1 match, got %+v", testResp.Matches)
	}
	if _, ok := testResp.Matches[0]["created_at"]; ok {
		t.Errorf("rules test matches must not carry created_at (the contract omits it): %+v", testResp.Matches[0])
	}

	deleteCmd := newRulesDeleteCmd()
	deleteCmd.SetContext(context.Background())
	idStr := strconv.FormatInt(addResp.Rule.ID, 10)
	deleteOut, err := runCommand(t, deleteCmd, []string{idStr})
	if err != nil {
		t.Fatalf("rules delete failed: %v", err)
	}
	var deleteResp rulesDeleteResponse
	if err := json.Unmarshal([]byte(deleteOut), &deleteResp); err != nil {
		t.Fatalf("rules delete output is not valid JSON: %v\noutput: %s", err, deleteOut)
	}
	if !deleteResp.Deleted || deleteResp.ID != addResp.Rule.ID {
		t.Fatalf("unexpected rules delete response: %+v", deleteResp)
	}
}

func TestRulesAddInvalidKind(t *testing.T) {
	isolateIngestConfig(t)
	origCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	addCmd := newRulesAddCmd()
	addCmd.SetContext(context.Background())
	if _, err := runCommand(t, addCmd, []string{"x", "not-a-kind", "y"}); err == nil {
		t.Error("expected error for invalid rule kind")
	}
}

func TestRulesUpdateUnknownID(t *testing.T) {
	isolateIngestConfig(t)
	origCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	updateCmd := newRulesUpdateCmd()
	updateCmd.SetContext(context.Background())
	if _, err := runCommand(t, updateCmd, []string{"9999", "x", "category", "y"}); err == nil {
		t.Error("expected error updating an unknown rule id")
	}
}

func TestRulesDeleteUnknownID(t *testing.T) {
	isolateIngestConfig(t)
	origCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	deleteCmd := newRulesDeleteCmd()
	deleteCmd.SetContext(context.Background())
	if _, err := runCommand(t, deleteCmd, []string{"9999"}); err == nil {
		t.Error("expected error deleting an unknown rule id")
	}
}

func TestRulesDryRunJSON(t *testing.T) {
	isolateIngestConfig(t)
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	// Seed one ingested document through the same facade the rules commands
	// use, so dry-run has something to match against.
	srcFile := filepath.Join(t.TempDir(), "invoice.txt")
	if err := os.WriteFile(srcFile, []byte("Invoice #1 from Acme."), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Ingest(context.Background(), srcFile, ingest.Options{Vault: vaultDir}); err != nil {
		t.Fatalf("seed ingest failed: %v", err)
	}

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	dryRunCmd := newRulesDryRunCmd()
	dryRunCmd.SetContext(context.Background())
	out, err := runCommand(t, dryRunCmd, []string{"invoice", "category", "Invoices"})
	if err != nil {
		t.Fatalf("rules dry-run failed: %v", err)
	}
	var resp struct {
		SchemaVersion    int    `json:"schema_version"`
		Operation        string `json:"operation"`
		VaultPath        string `json:"vault_path"`
		TotalDocuments   int    `json:"total_documents"`
		MatchedDocuments int    `json:"matched_documents"`
		SkippedDocuments int    `json:"skipped_documents"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("rules dry-run output is not valid JSON: %v\noutput: %s", err, out)
	}
	if resp.Operation != "dry_run" || resp.TotalDocuments != 1 || resp.MatchedDocuments != 1 || resp.SkippedDocuments != 0 {
		t.Fatalf("unexpected dry-run response: %+v", resp)
	}
	if resp.VaultPath != vaultDir {
		t.Errorf("VaultPath = %q, want %q", resp.VaultPath, vaultDir)
	}
}
