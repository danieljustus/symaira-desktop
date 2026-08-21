package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-seek/internal/db"
)

func TestFrontmatterSHA256_Found(t *testing.T) {
	content := "---\n" +
		"source_path: /tmp/invoice.pdf\n" +
		"sha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
		"mime: application/pdf\n" +
		"---\n\n# Invoice\n"

	sha, ok := frontmatterSHA256(content)
	if !ok {
		t.Fatal("expected sha256 to be found")
	}
	if sha != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("unexpected sha256: %s", sha)
	}
}

func TestFrontmatterSHA256_NoFrontmatter(t *testing.T) {
	if _, ok := frontmatterSHA256("# Just a note\n\nno frontmatter here"); ok {
		t.Error("expected no sha256 without frontmatter")
	}
}

func TestFrontmatterSHA256_NoSHAField(t *testing.T) {
	content := "---\nsource_path: /tmp/x.txt\n---\nbody"
	if _, ok := frontmatterSHA256(content); ok {
		t.Error("expected no sha256 when field is absent")
	}
}

func TestFindSidecarPath_WalksUpToVaultRoot(t *testing.T) {
	vault := t.TempDir()
	sha := "abc123def456"
	sidecarDir := filepath.Join(vault, sidecarDirName)
	if err := os.MkdirAll(sidecarDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sidecarPath := filepath.Join(sidecarDir, sha+".jsonl")
	if err := os.WriteFile(sidecarPath, []byte(""), 0600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	nested := filepath.Join(vault, "Finance", "Invoices")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	docPath := filepath.Join(nested, "note.md")

	got := findSidecarPath(docPath, sha)
	if got != sidecarPath {
		t.Errorf("expected %s, got %s", sidecarPath, got)
	}
}

func TestFindSidecarPath_NotFound(t *testing.T) {
	docPath := filepath.Join(t.TempDir(), "note.md")
	if got := findSidecarPath(docPath, "doesnotexist"); got != "" {
		t.Errorf("expected empty path, got %s", got)
	}
}

func TestDetectSidecarPath_NonMarkdownSkipped(t *testing.T) {
	if got := detectSidecarPath("/tmp/file.txt", "---\nsha256: abc\n---\n"); got != "" {
		t.Errorf("expected non-markdown files to be skipped, got %s", got)
	}
}

func writeSidecarJSONL(t *testing.T, path string, extractions []sidecarExtraction) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create sidecar: %v", err)
	}
	defer f.Close()
	for _, e := range extractions {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func testDBForExtractions(t *testing.T) *db.DB {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbClient.Close() })
	return dbClient
}

// TestIndexFile_ImportsExtractionSidecar covers acceptance criterion:
// "Indexing a Markdown note with a valid extraction sidecar stores extraction rows."
func TestIndexFile_ImportsExtractionSidecar(t *testing.T) {
	vault := t.TempDir()
	dbClient := testDBForExtractions(t)
	embedder := &fakeEmbedder{dim: 8}

	sha := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	docPath := filepath.Join(vault, "invoice.md")
	content := "---\nsource_path: /tmp/invoice.pdf\nsha256: " + sha + "\nmime: application/pdf\n---\n\n" +
		"Invoice total due is listed below.\nTotal: $500.00 due by end of month.\n"
	if err := os.WriteFile(docPath, []byte(content), 0644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	sidecarPath := filepath.Join(vault, sidecarDirName, sha+".jsonl")
	writeSidecarJSONL(t, sidecarPath, []sidecarExtraction{
		{Field: "amount", Type: "amount", Value: "$500.00", Span: &sidecarSpan{Start: 10, End: 20, Snippet: "Total: $500.00"}, Matched: true},
		{Field: "deadline", Type: "deadline", Value: "end of month", Matched: false},
	})

	if _, err := IndexFile(dbClient, embedder, docPath); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	extractions, err := dbClient.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("GetDocumentExtractions: %v", err)
	}
	if len(extractions) != 2 {
		t.Fatalf("expected 2 extractions, got %d", len(extractions))
	}

	var amount *db.Extraction
	for _, e := range extractions {
		if e.Class == "amount" {
			amount = e
		}
	}
	if amount == nil {
		t.Fatal("expected an 'amount' extraction")
	}
	if amount.Value != "$500.00" || !amount.Matched || amount.EvidenceText != "Total: $500.00" {
		t.Errorf("unexpected amount extraction: %+v", amount)
	}
	if amount.ChunkID == nil {
		t.Error("expected amount extraction to be linked to a chunk")
	}
}

// TestIndexFile_ReindexDoesNotDuplicateExtractions covers acceptance criterion:
// "Re-indexing an unchanged document does not duplicate extraction rows" and
// "Deleting/re-indexing a document removes or replaces stale extraction rows."
func TestIndexFile_ReindexDoesNotDuplicateExtractions(t *testing.T) {
	vault := t.TempDir()
	dbClient := testDBForExtractions(t)
	embedder := &fakeEmbedder{dim: 8}

	sha := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	docPath := filepath.Join(vault, "note.md")
	content := "---\nsha256: " + sha + "\n---\n\nBody content here that is long enough to chunk sensibly.\n"
	if err := os.WriteFile(docPath, []byte(content), 0644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	sidecarPath := filepath.Join(vault, sidecarDirName, sha+".jsonl")
	writeSidecarJSONL(t, sidecarPath, []sidecarExtraction{
		{Field: "party", Value: "Acme Corp", Matched: true},
	})

	if _, err := IndexFile(dbClient, embedder, docPath); err != nil {
		t.Fatalf("first IndexFile: %v", err)
	}
	first, err := dbClient.GetDocumentExtractions(docPath)
	if err != nil || len(first) != 1 {
		t.Fatalf("expected 1 extraction after first index, got %d (err=%v)", len(first), err)
	}

	// Re-index the unchanged file: hash matches, indexing is skipped entirely.
	if _, err := IndexFile(dbClient, embedder, docPath); err != nil {
		t.Fatalf("second IndexFile (unchanged): %v", err)
	}
	afterUnchanged, err := dbClient.GetDocumentExtractions(docPath)
	if err != nil || len(afterUnchanged) != 1 {
		t.Fatalf("expected still 1 extraction after unchanged reindex, got %d (err=%v)", len(afterUnchanged), err)
	}

	// Change the document body (same sidecar/sha in frontmatter): reindex
	// must replace, not accumulate, extraction rows.
	content2 := "---\nsha256: " + sha + "\n---\n\nUpdated body content, still chunked sensibly for testing.\n"
	if err := os.WriteFile(docPath, []byte(content2), 0644); err != nil {
		t.Fatalf("rewrite doc: %v", err)
	}
	if _, err := IndexFile(dbClient, embedder, docPath); err != nil {
		t.Fatalf("third IndexFile (changed): %v", err)
	}
	afterChanged, err := dbClient.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("GetDocumentExtractions after change: %v", err)
	}
	if len(afterChanged) != 1 {
		t.Fatalf("expected exactly 1 extraction after content change (no duplication), got %d", len(afterChanged))
	}

	// Deleting the document must remove its extractions entirely.
	if err := dbClient.DeleteDocument(docPath); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	afterDelete, err := dbClient.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("GetDocumentExtractions after delete: %v", err)
	}
	if len(afterDelete) != 0 {
		t.Fatalf("expected 0 extractions after document delete, got %d", len(afterDelete))
	}
}

func TestBestMatchingChunkID_PicksContainingSpan(t *testing.T) {
	c1ID := int64(1)
	c2ID := int64(2)
	start1, end1 := 0, 10
	start2, end2 := 10, 20
	chunks := []*db.Chunk{
		{ID: c1ID, CharStart: &start1, CharEnd: &end1},
		{ID: c2ID, CharStart: &start2, CharEnd: &end2},
	}

	got := bestMatchingChunkID(chunks, 12, 15)
	if got == nil || *got != c2ID {
		t.Errorf("expected chunk 2, got %v", got)
	}
}

func TestBestMatchingChunkID_NoSpansReturnsNil(t *testing.T) {
	chunks := []*db.Chunk{{ID: 1}, {ID: 2}}
	if got := bestMatchingChunkID(chunks, 5, 8); got != nil {
		t.Errorf("expected nil when no chunk has span data, got %v", *got)
	}
}
