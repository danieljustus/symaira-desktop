package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportSearchWritesRankedVaultNoteAndChoosesUniqueName(t *testing.T) {
	svc := newTestService(t)
	allowedPath, err := svc.NoteNew("Exportable Note", "quarterly-export-marker is the matching vault passage", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NoteNew("Unrelated Note", "this unrelated material must not enter the export", ""); err != nil {
		t.Fatal(err)
	}

	first, err := svc.ExportSearch("  quarterly-export-marker  ", "", "", "md")
	if err != nil {
		t.Fatalf("ExportSearch: %v", err)
	}
	if first.Format != "markdown" || first.Query != "quarterly-export-marker" || first.Title != "Search results: quarterly-export-marker" || first.Count != 1 {
		t.Fatalf("first export result = %#v", first)
	}
	if !strings.HasPrefix(first.Path, "search-results/") || !strings.HasSuffix(first.Path, ".md") {
		t.Fatalf("first export path = %q, want generated Markdown note path", first.Path)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, filepath.FromSlash(first.Path)))
	if err != nil {
		t.Fatalf("read exported note: %v", err)
	}
	if !strings.Contains(string(content), "quarterly-export-marker") || !strings.Contains(string(content), "[["+allowedPath+"]]") {
		t.Fatalf("export omitted the query or matching source: %s", content)
	}
	if strings.Contains(string(content), "Unrelated Note") || strings.Contains(string(content), "unrelated material") {
		t.Fatalf("export included an unrelated vault note: %s", content)
	}

	second, err := svc.ExportSearch("quarterly-export-marker", "", "", "markdown")
	if err != nil {
		t.Fatalf("second ExportSearch: %v", err)
	}
	if second.Path == first.Path || !strings.HasSuffix(second.Path, "-2.md") {
		t.Fatalf("second export path = %q, want collision-safe -2 suffix after %q", second.Path, first.Path)
	}
}

func TestExportSearchRejectsBlankQueryAndUnsupportedFormat(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.ExportSearch(" \t ", "title", "", "markdown"); err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("blank query error = %v, want required-query error", err)
	}
	if _, err := svc.ExportSearch("valid-query", "title", "", "html"); err == nil || !strings.Contains(err.Error(), "unsupported search export format") {
		t.Fatalf("unsupported format error = %v, want unsupported-format error", err)
	}
}

func TestExportSearchResultsNormalizesRelativePathAndRejectsVaultEscape(t *testing.T) {
	svc := newTestService(t)
	result, err := svc.ExportSearchResults("relative-output-query", "Relative Output", "reports/answer", "  MARKDOWN ", []SearchResult{{
		Path: "source.md", Title: "Source", Snippet: "relative-output-query evidence", Score: 0.75,
	}})
	if err != nil {
		t.Fatalf("ExportSearchResults: %v", err)
	}
	if result.Path != "reports/answer.md" || result.Format != "markdown" || result.Count != 1 {
		t.Fatalf("normalized export result = %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, filepath.FromSlash(result.Path)))
	if err != nil {
		t.Fatalf("read normalized export: %v", err)
	}
	if !strings.Contains(string(content), "relative-output-query evidence") || !strings.Contains(string(content), "[[source.md]]") {
		t.Fatalf("normalized export omitted supplied result data: %s", content)
	}

	for _, path := range []string{"../outside.md", ".symdesk/private.md"} {
		if _, err := svc.ExportSearchResults("q", "title", path, "markdown", nil); err == nil || !strings.Contains(err.Error(), "inside the vault") {
			t.Errorf("export to %q error = %v, want vault-boundary rejection", path, err)
		}
	}
}

func TestSearchNotebookPromotesOnlyMatchingSources(t *testing.T) {
	svc := newTestService(t)
	firstPath, err := svc.NoteNew("Promoted First", "working-set-promotion-marker first source", "")
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := svc.NoteNew("Promoted Second", "working-set-promotion-marker second source", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NoteNew("Not Promoted", "unrelated note stays outside this working set", ""); err != nil {
		t.Fatal(err)
	}

	nb, err := svc.SearchNotebook(" working-set-promotion-marker ", "")
	if err != nil {
		t.Fatalf("SearchNotebook: %v", err)
	}
	if nb.Title != "Search: working-set-promotion-marker" || nb.Query != "working-set-promotion-marker" {
		t.Fatalf("promoted notebook title/query = %q/%q", nb.Title, nb.Query)
	}
	sourceSet := make(map[string]bool, len(nb.Sources))
	for _, path := range nb.Sources {
		sourceSet[path] = true
	}
	if len(sourceSet) != 2 || !sourceSet[firstPath] || !sourceSet[secondPath] {
		t.Fatalf("promoted sources = %v, want only [%s %s]", nb.Sources, firstPath, secondPath)
	}
	if sourceSet["Not_Promoted.md"] {
		t.Fatal("unrelated note was added to the search notebook")
	}

	loaded, err := svc.NotebookGet(nb.ID)
	if err != nil {
		t.Fatalf("reopen promoted notebook: %v", err)
	}
	if loaded.Query != nb.Query || len(loaded.Sources) != 2 {
		t.Fatalf("reopened notebook = query %q sources %v, want promoted query and two sources", loaded.Query, loaded.Sources)
	}
}
