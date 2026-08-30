package sidecar

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestSearchGermanCompoundParts(t *testing.T) {
	db := setupTestDB(t)
	doc := &vault.Document{
		Path:   "/tmp/german-compound.md",
		Title:  "Beiträge",
		SHA256: "hash-german-compound",
		Body:   "Der Krankenversicherungsbeitrag wurde angepasst. Die Beitragsbemessungsgrenze steigt.",
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	for _, query := range []string{"Bemessungsgrenze", "Versicherungsbeitrag"} {
		results, err := db.Search(query)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(results) != 1 || results[0].Path != doc.Path {
			t.Fatalf("Search(%q) = %#v, want %s", query, results, doc.Path)
		}
	}
}
