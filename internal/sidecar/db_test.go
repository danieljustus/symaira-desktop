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

func TestDocsListV2Metadata(t *testing.T) {
	db := setupTestDB(t)

	doc := &vault.Document{
		Path:         "/tmp/doc.md",
		Title:        "Test Doc",
		Created:      time.Now().Format(time.RFC3339),
		SHA256:       "abc123",
		Body:         "body",
		DocumentDate: "2026-07-01",
		Person:       "Alice",
		Status:       "open",
		DueDate:      "2026-08-01",
		Confidence:   85,
		Frontmatter: map[string]interface{}{
			"document_date": "2026-07-01",
			"person":        "Alice",
			"status":        "open",
			"due_date":      "2026-08-01",
			"confidence":    85,
		},
	}

	if err := db.IndexDocument(doc); err != nil {
		t.Fatalf("IndexDocument failed: %v", err)
	}

	results, err := db.DocsList(DocsFilter{})
	if err != nil {
		t.Fatalf("DocsList failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Person != "Alice" {
		t.Errorf("expected person 'Alice', got '%s'", r.Person)
	}
	if r.Status != "open" {
		t.Errorf("expected status 'open', got '%s'", r.Status)
	}
	if r.DocumentDate != "2026-07-01" {
		t.Errorf("expected document_date '2026-07-01', got '%s'", r.DocumentDate)
	}
	if r.DueDate != "2026-08-01" {
		t.Errorf("expected due_date '2026-08-01', got '%s'", r.DueDate)
	}
	if r.Confidence != 85 {
		t.Errorf("expected confidence 85, got %d", r.Confidence)
	}
}

func TestDocsListFilters(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "a",
			Body: "a", Person: "Alice", Status: "open",
			Frontmatter: map[string]interface{}{"person": "Alice", "status": "open"},
		},
		{
			Path: "/tmp/b.md", Title: "B", Created: "2026-01-01T00:00:00Z", SHA256: "b",
			Body: "b", Person: "Bob", Status: "paid",
			Frontmatter: map[string]interface{}{"person": "Bob", "status": "paid"},
		},
		{
			Path: "/tmp/c.md", Title: "C", Created: "2026-01-01T00:00:00Z", SHA256: "c",
			Body: "c", Person: "Alice", Status: "done",
			Frontmatter: map[string]interface{}{"person": "Alice", "status": "done"},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatalf("IndexDocument failed for %s: %v", d.Path, err)
		}
	}

	// Filter by person
	results, err := db.DocsList(DocsFilter{Person: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results for person=Alice, got %d", len(results))
	}

	// Filter by status
	results, err = db.DocsList(DocsFilter{Status: "paid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "B" {
		t.Errorf("expected 1 result for status=paid, got %v", results)
	}

	// Filter by person + status
	results, err = db.DocsList(DocsFilter{Person: "Alice", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "C" {
		t.Errorf("expected 1 result for person=Alice+status=done, got %v", results)
	}
}

func TestCorrespondentBacklink(t *testing.T) {
	db := setupTestDB(t)

	correspondent := &vault.Document{
		Path:    "/tmp/Power_Co.md",
		Title:   "Power Co",
		Created: "2026-01-01T00:00:00Z",
		SHA256:  "x",
		Body:    "Power Co is a utility company.",
	}
	if err := db.IndexDocument(correspondent); err != nil {
		t.Fatal(err)
	}

	doc := &vault.Document{
		Path:    "/tmp/invoice.md",
		Title:   "Invoice",
		Created: "2026-01-01T00:00:00Z",
		SHA256:  "y",
		Body:    "Invoice from Power Co.",
		Frontmatter: map[string]interface{}{
			"correspondent": "Power Co",
		},
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	backlinks, err := db.GetBacklinks("/tmp/Power_Co.md")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, bl := range backlinks {
		if bl == "/tmp/invoice.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected correspondent backlink from invoice.md to Power_Co.md, got %v", backlinks)
	}
}

func TestDocsCounts(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "a1",
			Body: "a", Person: "Alice", Status: "open",
			Frontmatter: map[string]interface{}{"person": "Alice", "status": "open"},
		},
		{
			Path: "/tmp/b.md", Title: "B", Created: "2026-01-01T00:00:00Z", SHA256: "b1",
			Body: "b", Person: "Alice", Status: "paid",
			Frontmatter: map[string]interface{}{"person": "Alice", "status": "paid"},
		},
		{
			Path: "/tmp/c.md", Title: "C", Created: "2026-01-01T00:00:00Z", SHA256: "c1",
			Body: "c", Person: "Bob", Status: "open",
			Frontmatter: map[string]interface{}{"person": "Bob", "status": "open"},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := db.DocsCounts(DocsFilter{Person: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total != 2 {
		t.Errorf("expected total 2, got %d", counts.Total)
	}
	if counts.Status["open"] != 1 || counts.Status["paid"] != 1 {
		t.Errorf("expected status counts open=1 paid=1, got %v", counts.Status)
	}
}

func TestReviewQueue(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/good.md", Title: "Good Doc", Created: "2026-01-01T00:00:00Z", SHA256: "g1",
			Body: "good", Confidence: 95, DocumentDate: "2026-07-01",
			Frontmatter: map[string]interface{}{"document_type": "bill", "confidence": 95, "document_date": "2026-07-01"},
		},
		{
			Path: "/tmp/low.md", Title: "Low Confidence", Created: "2026-01-01T00:00:00Z", SHA256: "l1",
			Body: "low conf", Confidence: 50, DocumentDate: "2026-07-01",
			Frontmatter: map[string]interface{}{"document_type": "bill", "confidence": 50, "document_date": "2026-07-01"},
		},
		{
			Path: "/tmp/notype.md", Title: "No Type", Created: "2026-01-01T00:00:00Z", SHA256: "n1",
			Body: "no type", Confidence: 90, DocumentDate: "2026-07-01",
			Frontmatter: map[string]interface{}{"confidence": 90, "document_date": "2026-07-01"},
		},
		{
			Path: "/tmp/nodate.md", Title: "No Date", Created: "2026-01-01T00:00:00Z", SHA256: "d1",
			Body: "no date", Confidence: 88,
			Frontmatter: map[string]interface{}{"document_type": "notice", "confidence": 88},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.ReviewQueue(85)
	if err != nil {
		t.Fatal(err)
	}

	paths := make(map[string]bool)
	for _, r := range results {
		paths[r.Path] = true
	}

	if !paths["/tmp/low.md"] {
		t.Error("expected low confidence doc in review queue")
	}
	if !paths["/tmp/notype.md"] {
		t.Error("expected missing-type doc in review queue")
	}
	if !paths["/tmp/nodate.md"] {
		t.Error("expected missing-date doc in review queue")
	}
	if paths["/tmp/good.md"] {
		t.Error("good doc should NOT be in review queue")
	}
}

func TestSimilarDocs(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "sa",
			Body: "Monthly utility bill for Alice from Power Co. Amount due: $142.50.",
			Simhash: "a1b2c3d4e5f6a7b8",
			Frontmatter: map[string]interface{}{},
		},
		{
			Path: "/tmp/b.md", Title: "B", Created: "2026-01-01T00:00:00Z", SHA256: "sb",
			Body: "Monthly utility bill for Alice from Power Co. Amount due: $155.00.",
			Simhash: "a1b2c3d4e5f6a7b0",
			Frontmatter: map[string]interface{}{},
		},
		{
			Path: "/tmp/c.md", Title: "C", Created: "2026-01-01T00:00:00Z", SHA256: "sc",
			Body: "Completely different text about car insurance.",
			Simhash: "ffffffffffffffff",
			Frontmatter: map[string]interface{}{},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.SimilarDocs("a1b2c3d4e5f6a7b8", 80)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, r := range results {
		if r.Path == "/tmp/b.md" {
			found = true
			if r.Similarity < 80 {
				t.Errorf("expected similarity >= 80, got %d", r.Similarity)
			}
		}
	}
	if !found {
		t.Error("expected similar doc b.md in results")
	}

	for _, r := range results {
		if r.Path == "/tmp/c.md" {
			t.Error("dissimilar doc c.md should not be in results")
		}
	}
}
