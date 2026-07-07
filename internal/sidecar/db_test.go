package sidecar

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestFTSQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"simple", `"simple"*`},
		{"e-mail test", `"e-mail"* "test"*`},
		{`foo "bar"`, `"foo"* """bar"""*`},
		{"multiple   spaces", `"multiple"* "spaces"*`},
	}

	for _, tt := range tests {
		got := ftsQuote(tt.input)
		if got != tt.expected {
			t.Errorf("ftsQuote(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func setupTestDB(t *testing.T) *DB {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestIndexAndDeleteDocument(t *testing.T) {
	db := setupTestDB(t)

	doc := &vault.Document{
		Path:    "/tmp/test.md",
		Title:   "Test Document",
		Created: time.Now().Format(time.RFC3339),
		SHA256:  "abc123hash",
		Body:    "This is a test document with an e-mail.",
		Frontmatter: map[string]interface{}{
			"status": "active",
		},
		Links: []string{"other_note"},
	}

	err := db.IndexDocument(doc)
	if err != nil {
		t.Fatalf("IndexDocument failed: %v", err)
	}

	indexed, err := db.IsIndexed(doc.Path, doc.SHA256)
	if err != nil {
		t.Fatalf("IsIndexed failed: %v", err)
	}
	if !indexed {
		t.Errorf("expected document to be indexed")
	}

	// Verify Search
	results, err := db.Search("e-mail")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Test Document" {
		t.Errorf("expected 1 search result, got %v", len(results))
	}

	// Verify Properties
	props, err := db.GetProperties(doc.Path)
	if err != nil {
		t.Fatalf("GetProperties failed: %v", err)
	}
	if props["status"] != "active" {
		t.Errorf("expected property status=active, got %v", props["status"])
	}

	// Re-index to test update path
	doc.Title = "Updated Document"
	doc.SHA256 = "def456hash"
	err = db.IndexDocument(doc)
	if err != nil {
		t.Fatalf("IndexDocument update failed: %v", err)
	}

	results, _ = db.Search("test")
	if len(results) == 1 && results[0].Title != "Updated Document" {
		t.Errorf("expected updated title, got %s", results[0].Title)
	}

	// Verify Backlinks
	backlinks, err := db.GetBacklinks("other_note")
	if err != nil {
		t.Fatalf("GetBacklinks failed: %v", err)
	}
	if len(backlinks) != 1 || backlinks[0] != doc.Path {
		t.Errorf("expected 1 backlink to other_note, got %v", backlinks)
	}

	// Test DeleteDocument
	err = db.DeleteDocument(doc.Path)
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}

	indexed, _ = db.IsIndexed(doc.Path, doc.SHA256)
	if indexed {
		t.Errorf("expected document to be deleted")
	}
}
