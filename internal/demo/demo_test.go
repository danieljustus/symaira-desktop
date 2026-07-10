package demo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestInitCreatesAllFiles(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "test-vault")

	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	docDir := filepath.Join(vaultDir, "documents")
	entries, err := os.ReadDir(docDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(documents) {
		t.Errorf("expected %d documents, got %d", len(documents), len(entries))
	}

	for _, doc := range documents {
		mdPath := filepath.Join(docDir, doc.name+".md")
		if _, err := os.Stat(mdPath); err != nil {
			t.Errorf("missing document %s: %v", doc.name, err)
		}
		pdfPath := filepath.Join(vaultDir, "pdfs", doc.name+".pdf")
		if _, err := os.Stat(pdfPath); err != nil {
			t.Errorf("missing pdf %s: %v", doc.name, err)
		}
	}

	noteDir := filepath.Join(vaultDir, "notes")
	noteEntries, err := os.ReadDir(noteDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(noteEntries) != len(notes) {
		t.Errorf("expected %d notes, got %d", len(notes), len(noteEntries))
	}

	viewsPath := filepath.Join(vaultDir, ".symdesk", "views.json")
	if _, err := os.Stat(viewsPath); err != nil {
		t.Errorf("missing views.json: %v", err)
	}
}

func TestInitRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "existing")

	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "keep-me.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Init(vaultDir); err == nil {
		t.Error("expected error for non-empty directory")
	} else if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("expected 'non-empty' in error, got: %v", err)
	}
}

func TestInitIdempotentOnEmptyDir(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "empty-vault")

	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	if err := Init(vaultDir); err == nil {
		t.Error("expected error on second init (directory no longer empty)")
	}
}

func TestDocumentFrontmatterV2(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	docDir := filepath.Join(vaultDir, "documents")

	docsMissingDate := map[string]bool{
		"Brief_Versicherung_Erika": true,
	}

	for _, doc := range documents {
		mdPath := filepath.Join(docDir, doc.name+".md")
		parsed, err := vault.ParseFile(mdPath)
		if err != nil {
			t.Fatalf("parse %s: %v", doc.name, err)
		}

		if !docsMissingDate[doc.name] && parsed.DocumentDate == "" {
			t.Errorf("%s: missing document_date", doc.name)
		}
		if parsed.Status == "" {
			t.Errorf("%s: missing status", doc.name)
		}
		if parsed.Person == "" {
			t.Errorf("%s: missing person", doc.name)
		}
		corr, ok := parsed.Frontmatter["correspondent"].(string)
		if !ok || corr == "" {
			t.Errorf("%s: missing correspondent", doc.name)
		}
	}
}

func TestNearDuplicatePair(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(vaultDir, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	indexAll(t, vaultDir, db)

	origPath := filepath.Join(vaultDir, "documents", "Rechnung_Musterfirma_2026-01.md")
	origDoc, err := vault.ParseFile(origPath)
	if err != nil {
		t.Fatal(err)
	}
	if origDoc.Simhash == "" {
		origDoc.Simhash = computeSimhash(t, origDoc.Body)
	}

	results, err := db.SimilarDocs(origDoc.Simhash, 50)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, r := range results {
		if strings.Contains(r.Path, "Rechnung_Musterfirma_2026-02") {
			found = true
			t.Logf("found near-duplicate: %s (similarity=%d%%)", r.Title, r.Similarity)
		}
	}
	if !found {
		t.Error("expected Rechnung_Musterfirma_2026-02 in similar results")
	}
}

func TestReviewQueueNonEmpty(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(vaultDir, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	indexAll(t, vaultDir, db)

	results, err := db.ReviewQueue(85)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected non-empty review queue (low confidence + missing metadata docs)")
	}

	for _, r := range results {
		t.Logf("review: %s (confidence=%d, reasons=%v)", r.Title, r.Confidence, r.Reasons)
	}
}

func TestSavedViews(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	mgr := dbviews.NewManager(vaultDir)
	views, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 6 {
		t.Fatalf("expected 6 views, got %d", len(views))
	}

	viewIDs := make(map[string]bool)
	for _, v := range views {
		viewIDs[v.ID] = true
		if v.Name == "" {
			t.Errorf("view %s has empty name", v.ID)
		}
		if len(v.Filters) == 0 && v.Type != "timeline" && v.Type != "list" {
			t.Errorf("view %s has no filters", v.ID)
		}
	}

	expected := []string{"demo_open_invoices", "demo_tax_2026", "demo_needs_review", "demo_invoice_gallery", "demo_document_timeline", "demo_document_list"}
	for _, id := range expected {
		if !viewIDs[id] {
			t.Errorf("missing expected view: %s", id)
		}
	}

	viewsJSON, err := os.ReadFile(filepath.Join(vaultDir, ".symdesk", "views.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed []dbviews.View
	if err := json.Unmarshal(viewsJSON, &parsed); err != nil {
		t.Fatalf("views.json is not valid JSON: %v", err)
	}
}

func TestDocsListAfterIndex(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(vaultDir, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	indexAll(t, vaultDir, db)

	results, err := db.DocsList(sidecar.DocsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	totalExpected := len(documents) + len(notes)
	if len(results) != totalExpected {
		t.Errorf("expected %d documents, got %d", totalExpected, len(results))
	}

	for _, r := range results {
		if r.Title == "" {
			t.Errorf("document at %s has empty title", r.Path)
		}
	}
}

func TestNoRealPersonalData(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	realNames := []string{
		"Müller", "Schmidt", "Fischer", "Weber",
		"Wagner", "Becker", "Schulz", "Hoffmann",
		"Schäfer", "Koch",
	}

	allContent := collectAllText(t, vaultDir)

	for _, name := range realNames {
		if strings.Contains(allContent, name) {
			t.Errorf("found potentially real surname %q in demo content", name)
		}
	}

	fakeNames := []string{"Mustermann", "Beispielstadt", "Musterfirma"}
	for _, name := range fakeNames {
		if !strings.Contains(allContent, name) {
			t.Errorf("expected fake name %q in demo content", name)
		}
	}
}

func TestPDFsAreSmall(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}

	pdfDir := filepath.Join(vaultDir, "pdfs")
	entries, err := os.ReadDir(pdfDir)
	if err != nil {
		t.Fatal(err)
	}

	var totalSize int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		totalSize += info.Size()
		if info.Size() > 100*1024 {
			t.Errorf("PDF %s is too large: %d bytes", e.Name(), info.Size())
		}
	}

	if totalSize > 2*1024*1024 {
		t.Errorf("total PDF size %d bytes exceeds 2 MB limit", totalSize)
	}
	t.Logf("total PDF size: %d bytes", totalSize)
}

func indexAll(t *testing.T, vaultDir string, db *sidecar.DB) {
	t.Helper()
	err := vault.Walk(vaultDir, func(path string) error {
		doc, err := vault.ParseFile(path)
		if err != nil {
			return err
		}
		return db.IndexDocument(doc)
	})
	if err != nil {
		t.Fatalf("index vault: %v", err)
	}
}

func computeSimhash(t *testing.T, body string) string {
	t.Helper()
	// Import would cause circular dependency; just recompute from raw text.
	// The simhash package is pure computation with no state.
	tokens := strings.Fields(strings.ToLower(body))
	var vector [64]int
	for _, tok := range tokens {
		h := fnv1a64(tok)
		for i := 0; i < 64; i++ {
			if h&(1<<uint(i)) != 0 {
				vector[i]++
			} else {
				vector[i]--
			}
		}
	}
	var fingerprint uint64
	for i := 0; i < 64; i++ {
		if vector[i] > 0 {
			fingerprint |= 1 << uint(i)
		}
	}
	return strings.Repeat("0", 16)[:0] + formatHex(fingerprint)
}

func fnv1a64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func formatHex(v uint64) string {
	const hexDigits = "0123456789abcdef"
	var buf [16]byte
	for i := 15; i >= 0; i-- {
		buf[i] = hexDigits[v&0xf]
		v >>= 4
	}
	return string(buf[:])
}

func collectAllText(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		b.Write(data)
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
