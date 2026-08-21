package db

import (
	"path/filepath"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

func TestChunkCharSpansRoundTrip(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "spans.md")

	doc := &Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}
	if err := database.SaveDocument(doc); err != nil {
		t.Fatalf("save document: %v", err)
	}

	chunks := []*Chunk{
		{UUID: "u1", DocumentPath: docPath, ChunkIndex: 0, Content: "hello", Embedding: []float32{1, 0}, Hash: "h", CharStart: intPtr(0), CharEnd: intPtr(5)},
		{UUID: "u2", DocumentPath: docPath, ChunkIndex: 1, Content: "world", Embedding: []float32{0, 1}, Hash: "h", CharStart: nil, CharEnd: nil},
	}
	if err := database.SaveChunks(chunks); err != nil {
		t.Fatalf("save chunks: %v", err)
	}

	got, err := database.GetChunksForDocument(docPath)
	if err != nil {
		t.Fatalf("get chunks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(got))
	}
	if got[0].CharStart == nil || *got[0].CharStart != 0 || got[0].CharEnd == nil || *got[0].CharEnd != 5 {
		t.Errorf("chunk 0 span mismatch: start=%v end=%v", got[0].CharStart, got[0].CharEnd)
	}
	if got[1].CharStart != nil || got[1].CharEnd != nil {
		t.Errorf("chunk 1 span should remain nil for legacy-style insert, got start=%v end=%v", got[1].CharStart, got[1].CharEnd)
	}
}

func TestSaveAndGetDocumentExtractions(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "doc.md")

	if err := database.SaveDocument(&Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save document: %v", err)
	}

	extractions := []*Extraction{
		{DocumentPath: docPath, Class: "amount", Value: "$42.00", EvidenceText: "Total: $42.00", SpanStart: intPtr(10), SpanEnd: intPtr(16), Matched: true, Producer: "symingest/annotate", SourceRef: "sidecar.jsonl", CreatedAt: time.Now()},
		{DocumentPath: docPath, Class: "deadline", Value: "2026-08-01", EvidenceText: "due 2026-08-01", Matched: false, Producer: "symingest/annotate", SourceRef: "sidecar.jsonl", CreatedAt: time.Now()},
	}
	if err := database.SaveExtractions(extractions); err != nil {
		t.Fatalf("save extractions: %v", err)
	}
	if extractions[0].ID == 0 || extractions[1].ID == 0 {
		t.Errorf("expected IDs to be populated after insert")
	}

	got, err := database.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("get document extractions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 extractions, got %d", len(got))
	}
	// Extraction with a span sorts first.
	if got[0].Class != "amount" {
		t.Errorf("expected amount extraction first, got %s", got[0].Class)
	}
	if !got[0].Matched {
		t.Errorf("expected first extraction to be matched")
	}
	if got[1].Matched {
		t.Errorf("expected second extraction to be unmatched")
	}
}

func TestDeleteExtractionsForDocument_NoDuplicatesOnReindex(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "doc.md")

	if err := database.SaveDocument(&Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save document: %v", err)
	}

	makeExtraction := func() *Extraction {
		return &Extraction{DocumentPath: docPath, Class: "amount", Value: "$1.00", EvidenceText: "e", CreatedAt: time.Now()}
	}

	if err := database.SaveExtractions([]*Extraction{makeExtraction()}); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Simulate re-indexing the same document: delete-then-reinsert instead of
	// blindly appending, so a rerun never duplicates rows.
	if err := database.DeleteExtractionsForDocument(docPath); err != nil {
		t.Fatalf("delete before reindex: %v", err)
	}
	if err := database.SaveExtractions([]*Extraction{makeExtraction()}); err != nil {
		t.Fatalf("second import: %v", err)
	}

	got, err := database.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("get document extractions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 extraction after reindex, got %d", len(got))
	}
}

func TestDeleteDocument_RemovesExtractions(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "doc.md")

	if err := database.SaveDocument(&Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save document: %v", err)
	}
	if err := database.SaveExtractions([]*Extraction{{DocumentPath: docPath, Class: "amount", Value: "$1.00", EvidenceText: "e", CreatedAt: time.Now()}}); err != nil {
		t.Fatalf("save extraction: %v", err)
	}

	if err := database.DeleteDocument(docPath); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	got, err := database.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("get document extractions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 stale extractions after document delete, got %d", len(got))
	}
}

func TestListExtractions_FilterByClass(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := database.SaveDocument(&Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save document: %v", err)
	}

	extractions := []*Extraction{
		{DocumentPath: docPath, Class: "amount", Value: "$1.00", EvidenceText: "e1", CreatedAt: time.Now()},
		{DocumentPath: docPath, Class: "deadline", Value: "2026-01-01", EvidenceText: "e2", CreatedAt: time.Now()},
		{DocumentPath: docPath, Class: "amount", Value: "$2.00", EvidenceText: "e3", CreatedAt: time.Now()},
	}
	if err := database.SaveExtractions(extractions); err != nil {
		t.Fatalf("save extractions: %v", err)
	}

	got, err := database.ListExtractions("amount", 10)
	if err != nil {
		t.Fatalf("list extractions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 'amount' extractions, got %d", len(got))
	}
	for _, e := range got {
		if e.Class != "amount" {
			t.Errorf("expected class 'amount', got %q", e.Class)
		}
	}

	all, err := database.ListExtractions("", 10)
	if err != nil {
		t.Fatalf("list all extractions: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 extractions with no class filter, got %d", len(all))
	}
}

func TestSearchExtractions_FullText(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := database.SaveDocument(&Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save document: %v", err)
	}

	extractions := []*Extraction{
		{DocumentPath: docPath, Class: "amount", Value: "$500.00", EvidenceText: "Invoice total: $500.00 due", CreatedAt: time.Now()},
		{DocumentPath: docPath, Class: "party", Value: "Acme Corp", EvidenceText: "Billed to Acme Corp", CreatedAt: time.Now()},
	}
	if err := database.SaveExtractions(extractions); err != nil {
		t.Fatalf("save extractions: %v", err)
	}

	got, err := database.SearchExtractions("invoice", 10)
	if err != nil {
		t.Fatalf("search extractions: %v", err)
	}
	if len(got) != 1 || got[0].Class != "amount" {
		t.Fatalf("expected 1 'amount' match for 'invoice', got %+v", got)
	}

	got2, err := database.SearchExtractions("Acme", 10)
	if err != nil {
		t.Fatalf("search extractions: %v", err)
	}
	if len(got2) != 1 || got2[0].Class != "party" {
		t.Fatalf("expected 1 'party' match for 'Acme', got %+v", got2)
	}
}

func TestGetChunkSpansForDocument(t *testing.T) {
	database := openTestDB(t)
	docPath := filepath.Join(t.TempDir(), "doc.md")
	if err := database.SaveDocument(&Document{Path: docPath, Hash: "h1", UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save document: %v", err)
	}

	chunks := []*Chunk{
		{UUID: "u1", DocumentPath: docPath, ChunkIndex: 0, Content: "hello world", Embedding: []float32{1}, Hash: "h", CharStart: intPtr(0), CharEnd: intPtr(11)},
		{UUID: "u2", DocumentPath: docPath, ChunkIndex: 1, Content: "foo bar", Embedding: []float32{1}, Hash: "h", CharStart: intPtr(11), CharEnd: intPtr(18)},
	}
	if err := database.SaveChunks(chunks); err != nil {
		t.Fatalf("save chunks: %v", err)
	}

	spans, err := database.GetChunkSpansForDocument(docPath)
	if err != nil {
		t.Fatalf("get chunk spans: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("expected 2 chunk spans, got %d", len(spans))
	}
	if *spans[0].CharEnd != 11 || *spans[1].CharStart != 11 {
		t.Errorf("unexpected chunk span boundaries: %+v %+v", spans[0], spans[1])
	}
}
