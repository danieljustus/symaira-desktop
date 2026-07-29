package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestSearchWithMetaOperators(t *testing.T) {
	svc := newTestService(t)
	for _, doc := range []*vault.Document{
		{
			Path: filepath.Join(svc.VaultRoot, "finance", "open.md"), Title: "Open invoice", SHA256: "open", Created: "2026-01-01T00:00:00Z",
			Status: "open", Body: "steuer invoice-2026",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice"}, "document_type": "invoice"},
		},
		{
			Path: filepath.Join(svc.VaultRoot, "finance", "paid.md"), Title: "Paid invoice", SHA256: "paid", Created: "2026-01-01T00:00:00Z",
			Status: "paid", Body: "steuer invoice-2026",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice"}, "document_type": "invoice"},
		},
		{
			Path: filepath.Join(svc.VaultRoot, "notes", "other.md"), Title: "Other", SHA256: "other", Created: "2026-01-01T00:00:00Z",
			Status: "open", Body: "steuer invoice-2026",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice"}, "document_type": "invoice"},
		},
	} {
		if err := svc.DB.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	response, err := svc.SearchWithMeta("tag:invoice path:finance type:invoice -status:paid steuer /invoice-2026/")
	if err != nil {
		t.Fatal(err)
	}
	if response.Hint != "" {
		t.Fatalf("unexpected hint: %q", response.Hint)
	}
	if len(response.Results) != 1 {
		t.Fatalf("expected one result, got %#v", response.Results)
	}
	if response.Results[0].Path != filepath.Join("finance", "open.md") {
		t.Errorf("path = %v, want finance/open.md", response.Results[0].Path)
	}
}

func TestSearchWithMetaInvalidSyntaxFallsBackWithHint(t *testing.T) {
	svc := newTestService(t)
	compose.ResetCache()
	t.Setenv("PATH", "/usr/bin:/bin")
	response, err := svc.SearchWithMeta(`"unterminated`)
	if err != nil {
		t.Fatal(err)
	}
	if response.Hint == "" {
		t.Fatal("expected invalid query hint")
	}
}

func TestSearchWithMetaEncodesNoResultsAsArray(t *testing.T) {
	svc := newTestService(t)
	compose.ResetCache()
	t.Setenv("PATH", "/usr/bin:/bin")

	response, err := svc.SearchWithMeta("nothing-matches")
	if err != nil {
		t.Fatal(err)
	}
	if response.Results == nil {
		t.Fatal("results must be an empty array, not nil")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"results":[]}` {
		t.Errorf("JSON = %s, want results array", encoded)
	}
}
