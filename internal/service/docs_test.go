package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestDocASNAllocatesLowestFreeRejectsCollisionsAndIndexes(t *testing.T) {
	svc := newTestService(t)
	for _, name := range []string{"first.md", "second.md", "third.md"} {
		content := "---\ntitle: " + strings.TrimSuffix(name, ".md") + "\n---\n\nBody\n"
		if err := os.WriteFile(filepath.Join(svc.VaultRoot, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	first, err := svc.DocASN("first.md", "next")
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Fatalf("expected first ASN 1, got %d", first)
	}
	second, err := svc.DocASN("second.md", "next")
	if err != nil {
		t.Fatal(err)
	}
	if second != 2 {
		t.Fatalf("expected second ASN 2, got %d", second)
	}

	if _, err := svc.DocASN("third.md", "1"); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("expected duplicate ASN rejection, got %v", err)
	}
	if _, err := svc.DocASN("third.md", "0"); err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("expected non-positive ASN rejection, got %v", err)
	}

	results, err := svc.DocsList(sidecar.DocsFilter{ASN: &second})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "second.md" || results[0].ASN != 2 {
		t.Fatalf("expected indexed second document by ASN, got %#v", results)
	}

	searchResults, err := svc.DB.Search("2")
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResults) != 1 || searchResults[0].Path != filepath.Join(svc.VaultRoot, "second.md") {
		t.Fatalf("expected search to find ASN 2 document, got %#v", searchResults)
	}
	if err := svc.PropsEdit("third.md", "asn", "3"); err == nil || !strings.Contains(err.Error(), "doc asn") {
		t.Fatalf("expected generic property ASN edit to be rejected, got %v", err)
	}
}

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
			"person":        "Alice",
			"status":        "open",
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

func TestDocStatusSetsFrontmatter(t *testing.T) {
	svc := newTestService(t)

	fileName := "status_test.md"
	absPath := filepath.Join(svc.VaultRoot, fileName)
	content := "---\ntitle: \"Status Test\"\nstatus: \"open\"\n---\n\nBody.\n"
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := svc.DB.IndexDocument(&vault.Document{
		Path: absPath, Title: "Status Test", Created: "2026-01-01T00:00:00Z",
		SHA256: "s1", Body: "Body.", Status: "open",
		Frontmatter: map[string]interface{}{"status": "open"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.DocStatus(fileName, "done"); err != nil {
		t.Fatal(err)
	}

	doc, err := vault.ParseFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != "done" {
		t.Errorf("expected status 'done', got '%s'", doc.Status)
	}
	if doc.Title != "Status Test" {
		t.Errorf("expected title preserved, got '%s'", doc.Title)
	}
}

func TestDocStatusRejectsInvalid(t *testing.T) {
	svc := newTestService(t)
	if err := svc.DocStatus("any.md", "bogus"); err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestDocDueSetsFrontmatter(t *testing.T) {
	svc := newTestService(t)

	fileName := "due_test.md"
	absPath := filepath.Join(svc.VaultRoot, fileName)
	content := "---\ntitle: \"Due Test\"\n---\n\nBody.\n"
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := svc.DB.IndexDocument(&vault.Document{
		Path: absPath, Title: "Due Test", Created: "2026-01-01T00:00:00Z",
		SHA256: "d1", Body: "Body.",
		Frontmatter: map[string]interface{}{},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.DocDue(fileName, "2026-12-31"); err != nil {
		t.Fatal(err)
	}

	doc, err := vault.ParseFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if doc.DueDate != "2026-12-31" {
		t.Errorf("expected due_date '2026-12-31', got '%s'", doc.DueDate)
	}
}

func TestDocsReviewReturnsUnderThreshold(t *testing.T) {
	svc := newTestService(t)

	docs := []*vault.Document{
		{
			Path: filepath.Join(svc.VaultRoot, "good.md"), Title: "Good", Created: "2026-01-01T00:00:00Z",
			SHA256: "g1", Body: "good", Confidence: 95, DocumentDate: "2026-07-01",
			Frontmatter: map[string]interface{}{"document_type": "bill", "confidence": 95, "document_date": "2026-07-01"},
		},
		{
			Path: filepath.Join(svc.VaultRoot, "bad.md"), Title: "Bad", Created: "2026-01-01T00:00:00Z",
			SHA256: "b1", Body: "bad", Confidence: 40,
			Frontmatter: map[string]interface{}{"confidence": 40},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.DocsReview(85)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Bad" {
		t.Errorf("expected 1 review result (Bad), got %v", results)
	}
}

func TestDocStatusTraversalDenied(t *testing.T) {
	svc := newTestService(t)
	if err := svc.DocStatus("../escape.md", "done"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestDocStatusNonexistentFile(t *testing.T) {
	svc := newTestService(t)
	if err := svc.DocStatus("does_not_exist.md", "done"); err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestDocDueTraversalDenied(t *testing.T) {
	svc := newTestService(t)
	if err := svc.DocDue("../escape.md", "2026-12-31"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestDocDueNonexistentFile(t *testing.T) {
	svc := newTestService(t)
	if err := svc.DocDue("does_not_exist.md", "2026-12-31"); err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestSimilarDocsTraversalDenied(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.SimilarDocs("../escape.md", 80)
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestSimilarDocsNonexistentFile(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.SimilarDocs("nonexistent.md", 80)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestSimilarDocsComputesSimhashWhenEmpty(t *testing.T) {
	svc := newTestService(t)

	path := filepath.Join(svc.VaultRoot, "nohash.md")
	content := "---\ntitle: \"No Hash\"\n---\n\nSome body text for simhash computation.\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := svc.DB.IndexDocument(&vault.Document{
		Path: path, Title: "No Hash", Created: "2026-01-01T00:00:00Z",
		SHA256: "nh1", Body: "Some body text for simhash computation.",
		Frontmatter: map[string]interface{}{},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := svc.SimilarDocs("nohash.md", 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 similar results for self, got %d", len(results))
	}
}

func TestSimilarDocsFindsNearDuplicates(t *testing.T) {
	svc := newTestService(t)

	origPath := filepath.Join(svc.VaultRoot, "orig.md")
	clonePath := filepath.Join(svc.VaultRoot, "clone.md")

	origContent := "---\ntitle: \"Orig\"\nsimhash: \"a1b2c3d4e5f6a7b8\"\n---\n\nMonthly bill for Alice from Power Co.\n"
	cloneContent := "---\ntitle: \"Clone\"\nsimhash: \"a1b2c3d4e5f6a7b0\"\n---\n\nMonthly bill for Alice from Power Co.\n"

	if err := os.WriteFile(origPath, []byte(origContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clonePath, []byte(cloneContent), 0644); err != nil {
		t.Fatal(err)
	}

	docs := []*vault.Document{
		{
			Path: origPath, Title: "Orig", Created: "2026-01-01T00:00:00Z",
			SHA256: "o1", Body: "Monthly bill for Alice from Power Co.",
			Simhash: "a1b2c3d4e5f6a7b8",
			Frontmatter: map[string]interface{}{
				"simhash": "a1b2c3d4e5f6a7b8",
			},
		},
		{
			Path: clonePath, Title: "Clone", Created: "2026-01-01T00:00:00Z",
			SHA256: "c1", Body: "Monthly bill for Alice from Power Co.",
			Simhash: "a1b2c3d4e5f6a7b0",
			Frontmatter: map[string]interface{}{
				"simhash": "a1b2c3d4e5f6a7b0",
			},
		},
	}
	for _, d := range docs {
		if err := svc.DB.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}

	results, err := svc.SimilarDocs("orig.md", 80)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if r.Path == clonePath {
			found = true
		}
	}
	if !found {
		t.Error("expected clone.md in similar results")
	}
}

func writeTestDoc(t *testing.T, svc *Service, name, content string) string {
	t.Helper()
	absPath := filepath.Join(svc.VaultRoot, name)
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return absPath
}

func TestDocTypeSetsFrontmatter(t *testing.T) {
	svc := newTestService(t)
	absPath := writeTestDoc(t, svc, "type_test.md", "---\ntitle: \"T\"\n---\n\nBody.\n")

	if err := svc.DocType("type_test.md", "invoice"); err != nil {
		t.Fatal(err)
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := doc.Frontmatter["document_type"].(string); got != "invoice" {
		t.Errorf("expected document_type 'invoice', got '%s'", got)
	}

	if err := svc.DocType("type_test.md", "  "); err == nil {
		t.Error("expected error for empty document type")
	}
}

func TestDocCorrespondentSetsFrontmatter(t *testing.T) {
	svc := newTestService(t)
	absPath := writeTestDoc(t, svc, "corr_test.md", "---\ntitle: \"C\"\n---\n\nBody.\n")

	if err := svc.DocCorrespondent("corr_test.md", "Power Co"); err != nil {
		t.Fatal(err)
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := doc.Frontmatter["correspondent"].(string); got != "Power Co" {
		t.Errorf("expected correspondent 'Power Co', got '%s'", got)
	}

	if err := svc.DocCorrespondent("corr_test.md", ""); err == nil {
		t.Error("expected error for empty correspondent")
	}
}

func TestDocTagAddAndRemove(t *testing.T) {
	svc := newTestService(t)
	absPath := writeTestDoc(t, svc, "tag_test.md", "---\ntitle: \"Tags\"\ntags: [\"existing\"]\n---\n\nBody.\n")

	if err := svc.DocTagAdd("tag_test.md", "urgent"); err != nil {
		t.Fatal(err)
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Tags) != 2 || doc.Tags[0] != "existing" || doc.Tags[1] != "urgent" {
		t.Fatalf("expected tags [existing urgent], got %v", doc.Tags)
	}

	// Adding the same tag again is a no-op.
	if err := svc.DocTagAdd("tag_test.md", "urgent"); err != nil {
		t.Fatal(err)
	}
	doc, _ = vault.ParseFile(absPath)
	if len(doc.Tags) != 2 {
		t.Fatalf("expected duplicate add to be a no-op, got %v", doc.Tags)
	}

	if err := svc.DocTagRemove("tag_test.md", "existing"); err != nil {
		t.Fatal(err)
	}
	doc, _ = vault.ParseFile(absPath)
	if len(doc.Tags) != 1 || doc.Tags[0] != "urgent" {
		t.Fatalf("expected tags [urgent] after removal, got %v", doc.Tags)
	}

	if err := svc.DocTagAdd("tag_test.md", " "); err == nil {
		t.Error("expected error for blank tag")
	}
}

func TestDocBatchReportsPerFileResults(t *testing.T) {
	svc := newTestService(t)
	writeTestDoc(t, svc, "ok1.md", "---\ntitle: \"One\"\n---\n\nBody.\n")
	writeTestDoc(t, svc, "ok2.md", "---\ntitle: \"Two\"\n---\n\nBody.\n")

	files := []string{"ok1.md", "missing.md", "ok2.md"}
	results, updated, failed := svc.DocBatch(files, func(f string) error {
		return svc.DocStatus(f, "done")
	})

	if updated != 2 || failed != 1 {
		t.Fatalf("expected 2 updated / 1 failed, got %d / %d", updated, failed)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].Status != "updated" || results[2].Status != "updated" {
		t.Errorf("expected ok1/ok2 updated, got %+v", results)
	}
	if results[1].File != "missing.md" || results[1].Status != "error" || results[1].Error == "" {
		t.Errorf("expected per-file error for missing.md, got %+v", results[1])
	}

	// Both successful files were actually mutated.
	for _, name := range []string{"ok1.md", "ok2.md"} {
		doc, err := vault.ParseFile(filepath.Join(svc.VaultRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if doc.Status != "done" {
			t.Errorf("%s: expected status done, got %s", name, doc.Status)
		}
	}
}
