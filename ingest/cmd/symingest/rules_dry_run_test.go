package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestDryRunRuleAgainstDocumentsIsDeterministicAndOmitsBodies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "symingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	source := filepath.Join(dir, "invoice.txt")
	note := filepath.Join(vault, "invoice.txt.md")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("---\ntitle: Invoice 2026\n---\n\nSensitive invoice body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document, created, err := st.CreateOrGet(ctx, source, "hash-invoice", "text/plain")
	if err != nil || !created {
		t.Fatalf("CreateOrGet: document=%+v created=%v err=%v", document, created, err)
	}
	if err := st.SetVaultAndArchivePath(ctx, document.ID, note, filepath.Join(dir, "archive", "hash-invoice.txt"), "", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRule(ctx, "invoice", "category", "Finance"); err != nil {
		t.Fatal(err)
	}

	first, err := dryRunRuleAgainstDocuments(ctx, st, vault, "invoice", "tag", "Review")
	if err != nil {
		t.Fatalf("first dry-run: %v", err)
	}
	second, err := dryRunRuleAgainstDocuments(ctx, st, vault, "invoice", "tag", "Review")
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	if first.TotalDocuments != 1 || first.MatchedDocuments != 1 || len(first.Matches) != 1 {
		t.Fatalf("unexpected dry-run totals: %+v", first)
	}
	if first.Matches[0].DocumentID != document.ID || first.Matches[0].Title != "Invoice 2026" {
		t.Fatalf("unexpected match metadata: %+v", first.Matches[0])
	}
	if len(first.Matches[0].MatchedRuleIDs) != 1 {
		t.Fatalf("expected existing matching rule ID: %+v", first.Matches[0])
	}
	if first.Matches[0].NotePath != second.Matches[0].NotePath || first.Matches[0].Title != second.Matches[0].Title {
		t.Fatalf("dry-run result is not deterministic: first=%+v second=%+v", first, second)
	}
	if got := first.Matches[0].NotePath; got == "" || filepath.Base(got) != "invoice.txt.md" {
		t.Fatalf("unexpected note path: %q", got)
	}
}

func TestDryRunRejectsVaultOutsideOrInvalidRule(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "symingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := dryRunRuleAgainstDocuments(context.Background(), st, filepath.Join(t.TempDir(), "missing"), "", "tag", "x"); err == nil {
		t.Fatal("expected invalid proposed rule error")
	}
}
