package service

import (
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestDocsListReturnsRelativePaths(t *testing.T) {
	svc := newTestService(t)

	doc := &vault.Document{
		Path:         filepath.Join(svc.VaultRoot, "docs", "invoice.md"),
		Title:        "Invoice",
		Created:      "2026-01-01T00:00:00Z",
		SHA256:       "aaa",
		Body:         "invoice body",
		Person:       "Alice",
		Status:       "open",
		DocumentDate: "2026-07-01",
		Frontmatter: map[string]interface{}{
			"person":  "Alice",
			"status":  "open",
			"document_date": "2026-07-01",
		},
	}
	if err := svc.DB.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	results, err := svc.DocsList(sidecar.DocsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Path != filepath.Join("docs", "invoice.md") {
		t.Errorf("expected relative path 'docs/invoice.md', got '%s'", results[0].Path)
	}
	if results[0].Person != "Alice" {
		t.Errorf("expected person 'Alice', got '%s'", results[0].Person)
	}
}

func TestDocsListWithFilters(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path: filepath.Join(svc.VaultRoot, "a.md"), Title: "A", Created: "2026-01-01T00:00:00Z",
			SHA256: "a1", Body: "a", Person: "Alice", Status: "open",
			Frontmatter: map[string]interface{}{"person": "Alice", "status": "open"},
		},
		{
			Path: filepath.Join(svc.VaultRoot, "b.md"), Title: "B", Created: "2026-01-01T00:00:00Z",
			SHA256: "b1", Body: "b", Person: "Bob", Status: "paid",
			Frontmatter: map[string]interface{}{"person": "Bob", "status": "paid"},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.DocsList(sidecar.DocsFilter{Person: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "A" {
		t.Errorf("expected 1 result for person=Alice, got %v", results)
	}

	results, err = svc.DocsList(sidecar.DocsFilter{Status: "paid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "B" {
		t.Errorf("expected 1 result for status=paid, got %v", results)
	}
}
