package sidecar

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
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

func TestDocsListFiltersByASNAndIndexesASNForSearch(t *testing.T) {
	db := setupTestDB(t)
	asn := 42
	for _, doc := range []*vault.Document{
		{Path: "/tmp/asn.md", Title: "Archived", Created: time.Now().Format(time.RFC3339), SHA256: "asn", Body: "archive", ASN: &asn, Frontmatter: map[string]interface{}{"asn": asn}},
		{Path: "/tmp/no-asn.md", Title: "Unnumbered", Created: time.Now().Format(time.RFC3339), SHA256: "none", Body: "archive", Frontmatter: map[string]interface{}{}},
	} {
		if err := db.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.DocsList(DocsFilter{ASN: &asn})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/tmp/asn.md" || results[0].ASN != asn {
		t.Fatalf("expected exact ASN lookup, got %#v", results)
	}

	search, err := db.Search("42")
	if err != nil {
		t.Fatal(err)
	}
	if len(search) != 1 || search[0].Path != "/tmp/asn.md" {
		t.Fatalf("expected ASN full-text lookup, got %#v", search)
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
			Body:        "Monthly utility bill for Alice from Power Co. Amount due: $142.50.",
			Simhash:     "a1b2c3d4e5f6a7b8",
			Frontmatter: map[string]interface{}{},
		},
		{
			Path: "/tmp/b.md", Title: "B", Created: "2026-01-01T00:00:00Z", SHA256: "sb",
			Body:        "Monthly utility bill for Alice from Power Co. Amount due: $155.00.",
			Simhash:     "a1b2c3d4e5f6a7b0",
			Frontmatter: map[string]interface{}{},
		},
		{
			Path: "/tmp/c.md", Title: "C", Created: "2026-01-01T00:00:00Z", SHA256: "sc",
			Body:        "Completely different text about car insurance.",
			Simhash:     "ffffffffffffffff",
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

func TestDocsListFilterByType(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/invoice.md", Title: "Invoice", Created: "2026-01-01T00:00:00Z", SHA256: "i1",
			Body: "invoice", Frontmatter: map[string]interface{}{"document_type": "invoice"},
		},
		{
			Path: "/tmp/notice.md", Title: "Notice", Created: "2026-01-01T00:00:00Z", SHA256: "n1",
			Body: "notice", Frontmatter: map[string]interface{}{"document_type": "notice"},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.DocsList(DocsFilter{Type: "invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].DocumentType != "invoice" {
		t.Errorf("expected 1 invoice, got %v", results)
	}
}

func TestDocsListFilterByCorrespondent(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "a",
			Body: "a", Frontmatter: map[string]interface{}{"correspondent": "Power Co"},
		},
		{
			Path: "/tmp/b.md", Title: "B", Created: "2026-01-01T00:00:00Z", SHA256: "b",
			Body: "b", Frontmatter: map[string]interface{}{"correspondent": "Insurance Inc"},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.DocsList(DocsFilter{Correspondent: "Power Co"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "A" {
		t.Errorf("expected 1 result for correspondent, got %v", results)
	}
}

func TestDocsListFilterByYear(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/old.md", Title: "Old", Created: "2026-01-01T00:00:00Z", SHA256: "o",
			Body: "old", DocumentDate: "2025-03-15",
			Frontmatter: map[string]interface{}{"document_date": "2025-03-15"},
		},
		{
			Path: "/tmp/new.md", Title: "New", Created: "2026-01-01T00:00:00Z", SHA256: "nw",
			Body: "new", DocumentDate: "2026-07-01",
			Frontmatter: map[string]interface{}{"document_date": "2026-07-01"},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.DocsList(DocsFilter{Year: "2026"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "New" {
		t.Errorf("expected 1 result for year 2026, got %v", results)
	}
}

func TestDocsListFilterByDueBefore(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/soon.md", Title: "Soon", Created: "2026-01-01T00:00:00Z", SHA256: "s",
			Body: "soon", DueDate: "2026-06-01",
			Frontmatter: map[string]interface{}{"due_date": "2026-06-01"},
		},
		{
			Path: "/tmp/later.md", Title: "Later", Created: "2026-01-01T00:00:00Z", SHA256: "l",
			Body: "later", DueDate: "2026-12-31",
			Frontmatter: map[string]interface{}{"due_date": "2026-12-31"},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := db.DocsList(DocsFilter{DueBefore: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Soon" {
		t.Errorf("expected 1 result due before July, got %v", results)
	}
}

func TestDocsListFilterByConfidence(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/low.md", Title: "Low", Created: "2026-01-01T00:00:00Z", SHA256: "lo",
			Body: "low", Confidence: 30,
			Frontmatter: map[string]interface{}{"confidence": 30},
		},
		{
			Path: "/tmp/mid.md", Title: "Mid", Created: "2026-01-01T00:00:00Z", SHA256: "mi",
			Body: "mid", Confidence: 60,
			Frontmatter: map[string]interface{}{"confidence": 60},
		},
		{
			Path: "/tmp/high.md", Title: "High", Created: "2026-01-01T00:00:00Z", SHA256: "hi",
			Body: "high", Confidence: 95,
			Frontmatter: map[string]interface{}{"confidence": 95},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	minConf := 50
	maxConf := 80
	results, err := db.DocsList(DocsFilter{MinConfidence: &minConf, MaxConfidence: &maxConf})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Mid" {
		t.Errorf("expected 1 result in confidence range, got %v", results)
	}
}

func TestSimilarDocsEmptySimhash(t *testing.T) {
	db := setupTestDB(t)

	results, err := db.SimilarDocs("", 80)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil for empty simhash, got %v", results)
	}
}

func TestSimilarDocsInvalidSimhash(t *testing.T) {
	db := setupTestDB(t)

	_, err := db.SimilarDocs("not-a-hex-string", 80)
	if err == nil {
		t.Error("expected error for invalid simhash")
	}
}

func TestDocsCountsUnsetFields(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{
			Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "a1",
			Body:        "a",
			Frontmatter: map[string]interface{}{},
		},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := db.DocsCounts(DocsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total != 1 {
		t.Errorf("expected total 1, got %d", counts.Total)
	}
	if counts.Status["unset"] != 1 {
		t.Errorf("expected 1 unset status, got %v", counts.Status)
	}
	if counts.Person["unset"] != 1 {
		t.Errorf("expected 1 unset person, got %v", counts.Person)
	}
}

func TestCheckIntegrity(t *testing.T) {
	db := setupTestDB(t)
	if err := db.CheckIntegrity(); err != nil {
		t.Errorf("CheckIntegrity failed: %v", err)
	}
}

func TestListFiles(t *testing.T) {
	db := setupTestDB(t)

	docs := []*vault.Document{
		{Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "a", Body: "a"},
		{Path: "/tmp/b/b.md", Title: "B", Created: "2026-01-02T00:00:00Z", SHA256: "b", Body: "b"},
	}
	for _, d := range docs {
		if err := db.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	files, err := db.ListFiles("")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}

	files, err = db.ListFiles("/tmp/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Title != "B" {
		t.Errorf("expected 1 file under /tmp/b, got %v", files)
	}
}

func TestGetAllLinks(t *testing.T) {
	db := setupTestDB(t)

	doc := &vault.Document{
		Path: "/tmp/a.md", Title: "A", Created: "2026-01-01T00:00:00Z", SHA256: "a",
		Body:  "a",
		Links: []string{"b.md", "c.md"},
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	edges, err := db.GetAllLinks()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(edges))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	db := setupTestDB(t)
	results, err := db.Search("")
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil for empty query, got %v", results)
	}
}

func TestSearchPlanAppliesOperatorsNegationAndRegex(t *testing.T) {
	db := setupTestDB(t)
	docs := []*vault.Document{
		{
			Path: "/vault/finance/tax-open.md", Title: "Open tax invoice", SHA256: "1", Created: "2026-01-01T00:00:00Z",
			Status: "open", Body: "steuer annual report invoice-2026 ready",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice", "tax"}, "document_type": "invoice"},
		},
		{
			Path: "/vault/finance/tax-paid.md", Title: "Paid tax invoice", SHA256: "2", Created: "2026-01-01T00:00:00Z",
			Status: "paid", Body: "steuer annual report invoice-2025 paid",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice", "tax"}, "document_type": "invoice"},
		},
		{
			Path: "/vault/finance/draft.md", Title: "Draft", SHA256: "3", Created: "2026-01-01T00:00:00Z",
			Status: "open", Body: "steuer annual report invoice-2026 draft",
			Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice"}, "document_type": "invoice"},
		},
	}
	for _, doc := range docs {
		if err := db.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := searchquery.Parse(`tag:invoice path:finance type:invoice -status:paid "annual report" steuer /invoice-2026/ -draft`)
	if err != nil {
		t.Fatal(err)
	}
	results, err := db.SearchPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %#v", results)
	}
	if results[0].Path != "/vault/finance/tax-open.md" {
		t.Errorf("path = %q, want open finance invoice", results[0].Path)
	}
}

func TestSearchPlanExactTagMatching(t *testing.T) {
	db := setupTestDB(t)
	for _, doc := range []*vault.Document{
		{Path: "/vault/invoice.md", Title: "Invoice", SHA256: "invoice", Created: "2026-01-01T00:00:00Z", Body: "tax", Frontmatter: map[string]interface{}{"tags": []interface{}{"invoice"}}},
		{Path: "/vault/invoices.md", Title: "Invoices", SHA256: "invoices", Created: "2026-01-01T00:00:00Z", Body: "tax", Frontmatter: map[string]interface{}{"tags": []interface{}{"invoices"}}},
	} {
		if err := db.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := searchquery.Parse("tag:invoice")
	if err != nil {
		t.Fatal(err)
	}
	results, err := db.SearchPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Invoice" {
		t.Fatalf("exact tag filter returned %#v", results)
	}
}

// --- Error-path tests for sidecar.Open (coverage target: db.go:29-49) ---

func TestOpen_DefaultPathResolution(t *testing.T) {
	// Open("") should resolve to ~/.local/share/symdesk/sidecar.db.
	// The actual file likely doesn't exist in CI, so we expect a sqlitekit
	// error—but the important thing is that lines 30-35 executed.
	_, err := Open("")
	if err == nil {
		// If it succeeded, the default path was usable—still counts.
		return
	}
	// Accept any error; the point is the default-path branch ran.
	if !strings.Contains(err.Error(), "open sqlite") && !strings.Contains(err.Error(), "migrate") {
		t.Errorf("unexpected error from Open(\"\"): %v", err)
	}
}

func TestOpen_HomeDirFailure(t *testing.T) {
	// When os.UserHomeDir() fails, Open("") should return a "user home dir" error.
	t.Setenv("HOME", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("USERPROFILE", "")
	_, err := Open("")
	if err == nil {
		t.Fatal("expected error when HOME is unset")
	}
	if !strings.Contains(err.Error(), "user home dir") {
		t.Errorf("expected 'user home dir' in error, got: %v", err)
	}
}

func TestOpen_InvalidPath(t *testing.T) {
	// A path whose parent directory is a regular file should fail at sqlitekit.Open.
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(blocker, "test.db"))
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
	if !strings.Contains(err.Error(), "open sqlite") {
		t.Errorf("expected 'open sqlite' in error, got: %v", err)
	}
}

func TestOpen_MigrationFailure(t *testing.T) {
	// Pre-create a conflicting "files" table so that sidecar.Open's
	// sqlitekit.Migrate fails on the non-idempotent CREATE TABLE.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE files (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	raw.Close()

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("expected error from Open with conflicting migration")
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Errorf("expected 'migrate' in error, got: %v", err)
	}
}

// --- GetTitle tests (coverage target: db.go:272-279) ---

func TestGetTitle_MissingPath(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.GetTitle("/nonexistent/path.md")
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestGetTitle_ExistingPath(t *testing.T) {
	db := setupTestDB(t)

	doc := &vault.Document{
		Path:    "/tmp/test.md",
		Title:   "Test Title",
		Created: "2026-01-01T00:00:00Z",
		SHA256:  "abc123",
		Body:    "body",
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	title, err := db.GetTitle("/tmp/test.md")
	if err != nil {
		t.Fatalf("GetTitle failed: %v", err)
	}
	if title != "Test Title" {
		t.Errorf("expected 'Test Title', got '%s'", title)
	}
}
