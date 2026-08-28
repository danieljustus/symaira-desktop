package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

func TestExportSearchResultsWritesVaultMarkdownNoteAndIndexesIt(t *testing.T) {
	svc := newTestService(t)
	anchor := &retrieval.LocationAnchor{Kind: "section", Value: "Totals"}
	result, err := svc.ExportSearchResults(
		`invoice | 2026`,
		`Invoices [2026]`,
		"",
		"markdown",
		[]SearchResult{{Path: "invoice.md", Title: "Invoice", Snippet: "42 EUR", Score: 0.5, Anchor: anchor}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != "markdown" || result.Count != 1 || !strings.HasPrefix(result.Path, "search-results/") {
		t.Fatalf("unexpected export result: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, filepath.FromSlash(result.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "[[invoice.md#section:Totals]]") {
		t.Fatalf("attribution missing from note: %s", content)
	}

	response, err := svc.SearchWithMeta("42 EUR")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Path != result.Path {
		t.Fatalf("export note was not indexed: %#v", response.Results)
	}
}

func TestExportSearchResultsPDFUsesTheExistingRenderer(t *testing.T) {
	calls := withStubRenderer(t)
	svc := newTestService(t)
	output := filepath.Join(t.TempDir(), "results.pdf")
	result, err := svc.ExportSearchResults("budget", "Budget", output, "pdf", []SearchResult{{
		Path: "budget.md", Title: "Budget", Snippet: "42 EUR",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != "pdf" || result.Path != output || result.Count != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(*calls) != 1 || !strings.Contains(string((*calls)[0].Source), "[[budget.md]]") {
		t.Fatalf("PDF renderer did not receive the search attribution: %#v", *calls)
	}
}

func TestExportSearchResultsRejectsOutsideVaultMarkdownPath(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.ExportSearchResults("q", "title", filepath.Join(t.TempDir(), "outside.md"), "markdown", nil)
	if err == nil || !strings.Contains(err.Error(), "inside the vault") {
		t.Fatalf("expected outside-vault rejection, got %v", err)
	}
}

func TestNotebookQueryRoundTrips(t *testing.T) {
	svc := newTestService(t)
	nb, err := svc.NotebookNew("Query Note", "description")
	if err != nil {
		t.Fatal(err)
	}
	if nb.Query != "" {
		t.Fatalf("ordinary notebook unexpectedly has query %q", nb.Query)
	}
	searchNotebook, err := svc.SearchNotebook("query", "Query Working Set")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.NotebookGet(searchNotebook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Query != "query" {
		t.Fatalf("query = %q, want query", loaded.Query)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, filepath.FromSlash(loaded.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "query: query") {
		t.Fatalf("query metadata missing from notebook note: %s", content)
	}
}
