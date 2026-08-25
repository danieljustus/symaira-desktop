package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRulesCRUD(t *testing.T) {
	tempDir := t.TempDir()
	opts := Options{DBPath: filepath.Join(tempDir, "rules.db")}
	ctx := context.Background()

	rules, err := Rules(ctx, opts)
	if err != nil {
		t.Fatalf("Rules failed: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected no rules, got %d", len(rules))
	}

	rule, err := AddRule(ctx, opts, "invoice", "category", "Invoices")
	if err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}
	if rule.ID == 0 || rule.CreatedAt == "" {
		t.Errorf("unexpected rule: %+v", rule)
	}

	if _, err := AddRule(ctx, opts, "bad", "not-a-kind", "value"); err == nil {
		t.Error("expected error for invalid rule kind")
	}

	updated, err := UpdateRule(ctx, opts, rule.ID, "invoice", "tag", "billing")
	if err != nil {
		t.Fatalf("UpdateRule failed: %v", err)
	}
	if updated.Kind != "tag" || updated.Value != "billing" {
		t.Errorf("unexpected updated rule: %+v", updated)
	}

	if _, err := UpdateRule(ctx, opts, 9999, "x", "tag", "y"); err == nil {
		t.Error("expected error updating an unknown rule id")
	}

	rules, err = Rules(ctx, opts)
	if err != nil {
		t.Fatalf("Rules failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	if err := DeleteRule(ctx, opts, rule.ID); err != nil {
		t.Fatalf("DeleteRule failed: %v", err)
	}
	if err := DeleteRule(ctx, opts, rule.ID); err == nil {
		t.Error("expected error deleting an already-deleted rule")
	}
}

func TestTestRules(t *testing.T) {
	tempDir := t.TempDir()
	opts := Options{DBPath: filepath.Join(tempDir, "rules.db")}
	ctx := context.Background()

	if _, err := AddRule(ctx, opts, "Invoice", "category", "Invoices"); err != nil {
		t.Fatal(err)
	}
	if _, err := AddRule(ctx, opts, "receipt", "category", "Receipts"); err != nil {
		t.Fatal(err)
	}

	matches, err := TestRules(ctx, opts, "This is an INVOICE for services rendered.")
	if err != nil {
		t.Fatalf("TestRules failed: %v", err)
	}
	if len(matches) != 1 || matches[0].Value != "Invoices" {
		t.Errorf("unexpected matches: %+v", matches)
	}

	matches, err = TestRules(ctx, opts, "nothing relevant here")
	if err != nil {
		t.Fatalf("TestRules failed: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %+v", matches)
	}
}

func TestDryRunRule(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	if err := os.MkdirAll(vaultDir, 0750); err != nil {
		t.Fatal(err)
	}
	opts := Options{Vault: vaultDir, Archive: filepath.Join(tempDir, "archive"), DBPath: filepath.Join(tempDir, "rules.db")}
	ctx := context.Background()

	srcFile := filepath.Join(tempDir, "invoice.txt")
	if err := os.WriteFile(srcFile, []byte("Invoice #123 from Acme Corp."), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Ingest(ctx, srcFile, opts)
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}

	result, err := DryRunRule(ctx, opts, "invoice", "category", "Invoices")
	if err != nil {
		t.Fatalf("DryRunRule failed: %v", err)
	}
	if result.TotalDocuments != 1 || result.MatchedDocuments != 1 || result.SkippedDocuments != 0 {
		t.Errorf("unexpected counts: %+v", result)
	}
	if len(result.Matches) != 1 || result.Matches[0].NotePath != res.VaultPath {
		t.Errorf("unexpected matches: %+v", result.Matches)
	}
	if result.VaultPath != vaultDir {
		t.Errorf("VaultPath = %q, want %q", result.VaultPath, vaultDir)
	}
	if result.ProposedRule != (ProposedRule{Pattern: "invoice", Kind: "category", Value: "Invoices"}) {
		t.Errorf("unexpected proposed rule: %+v", result.ProposedRule)
	}

	// A pattern absent from the note content matches nothing.
	result, err = DryRunRule(ctx, opts, "nonexistent-pattern", "category", "X")
	if err != nil {
		t.Fatalf("DryRunRule failed: %v", err)
	}
	if result.MatchedDocuments != 0 {
		t.Errorf("expected no matches, got %+v", result.Matches)
	}

	// Invalid proposed rule is rejected before touching the store.
	if _, err := DryRunRule(ctx, opts, "", "category", "X"); err == nil {
		t.Error("expected error for empty pattern")
	}
}

func TestDryRunRuleSkipsUnreadableNote(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	if err := os.MkdirAll(vaultDir, 0750); err != nil {
		t.Fatal(err)
	}
	opts := Options{Vault: vaultDir, Archive: filepath.Join(tempDir, "archive"), DBPath: filepath.Join(tempDir, "rules.db")}
	ctx := context.Background()

	srcFile := filepath.Join(tempDir, "invoice.txt")
	if err := os.WriteFile(srcFile, []byte("Invoice #123."), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Ingest(ctx, srcFile, opts)
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}

	// Remove the note file the store still references, so DryRunRule has to
	// report it in Skipped rather than silently dropping it (issue #609).
	if err := os.Remove(res.VaultPath); err != nil {
		t.Fatal(err)
	}

	result, err := DryRunRule(ctx, opts, "invoice", "category", "Invoices")
	if err != nil {
		t.Fatalf("DryRunRule failed: %v", err)
	}
	if result.SkippedDocuments != 1 || len(result.Skipped) != 1 {
		t.Fatalf("expected 1 skipped document, got %+v", result)
	}
	if result.Skipped[0].Reason == "" {
		t.Errorf("expected non-empty skip reason")
	}
	if result.MatchedDocuments != 0 {
		t.Errorf("expected no matches, got %+v", result.Matches)
	}
}
