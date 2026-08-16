package sidecar

import (
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Search snippets carried SQLite FTS5's HTML highlight markers (`<b>`/`</b>`).
// Nothing renders them as HTML: the macOS command palette printed them as
// literal characters, and the same snippets are handed to the model as
// citations and as notebook source material, where the markup is pure noise
// and can end up in generated notes (issue #441).

// assertNoMarkup fails when a snippet still contains highlighter markup.
func assertNoMarkup(t *testing.T, where, snippet string) {
	t.Helper()
	for _, bad := range []string{"<b>", "</b>", "<", ">"} {
		if strings.Contains(snippet, bad) {
			t.Errorf("%s: snippet contains %q from the highlighter: %q", where, bad, snippet)
		}
	}
}

func indexSnippetFixture(t *testing.T, db *DB) {
	t.Helper()
	doc := &vault.Document{
		Path:    "/tmp/rechnung.md",
		Title:   "Rechnung Musterfirma",
		Created: time.Now().Format(time.RFC3339),
		SHA256:  "snippethash",
		Body:    "Regelmaessiger Rechnungsempfaenger. Die monatlichen Rechnungen fuer Webhosting und Domain.",
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatalf("IndexDocument failed: %v", err)
	}
}

func TestSearchSnippetHasNoHighlightMarkup(t *testing.T) {
	db := setupTestDB(t)
	indexSnippetFixture(t, db)

	docs, err := db.Search("Rechnungen")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected at least one search result")
	}
	for _, d := range docs {
		assertNoMarkup(t, "Search", d.Body)
	}
	// The matched term must survive: removing the markers must not remove text.
	if !strings.Contains(docs[0].Body, "Rechnung") {
		t.Errorf("expected the snippet to still contain the matched term, got %q", docs[0].Body)
	}
}

func TestSearchScopedSnippetHasNoHighlightMarkup(t *testing.T) {
	db := setupTestDB(t)
	indexSnippetFixture(t, db)

	docs, err := db.SearchScoped("Rechnungen", []string{"/tmp/rechnung.md"})
	if err != nil {
		t.Fatalf("SearchScoped failed: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected at least one scoped search result")
	}
	for _, d := range docs {
		assertNoMarkup(t, "SearchScoped", d.Body)
	}
}

func TestSearchPlanSnippetHasNoHighlightMarkup(t *testing.T) {
	db := setupTestDB(t)
	indexSnippetFixture(t, db)

	plan := searchquery.Plan{Terms: []searchquery.Term{{Value: "Rechnungen"}}}
	matches, err := db.SearchPlan(plan)
	if err != nil {
		t.Fatalf("SearchPlan failed: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one planned search match")
	}
	for _, m := range matches {
		assertNoMarkup(t, "SearchPlan", m.Snippet)
	}
}
