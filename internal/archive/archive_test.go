package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitPagesWithMarkers(t *testing.T) {
	text := "Page one text\n\n--- Page ---\n\nPage two text"
	pages := splitPages(text)
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
	if pages[0] != "Page one text" {
		t.Errorf("page 1: %q", pages[0])
	}
	if pages[1] != "Page two text" {
		t.Errorf("page 2: %q", pages[1])
	}
}

func TestSplitPagesWithFormFeed(t *testing.T) {
	text := "Page one\fPage two"
	pages := splitPages(text)
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
}

func TestSplitPagesSingle(t *testing.T) {
	text := "Single page text"
	pages := splitPages(text)
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(pages))
	}
}

func TestSplitPagesEmpty(t *testing.T) {
	pages := splitPages("")
	if pages != nil {
		t.Fatalf("expected nil, got %v", pages)
	}
}

func TestArchivePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"documents/invoice.pdf", "documents/invoice_archive.pdf"},
		{"scan.png", "scan_archive.pdf"},
		{"notes/meeting.md", "notes/meeting_archive.pdf"},
	}
	for _, tt := range tests {
		got := ArchivePath(tt.in)
		if got != tt.want {
			t.Errorf("ArchivePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHasTextLayer(t *testing.T) {
	dir := t.TempDir()

	// Create a minimal PDF with text operators
	pdfWithText := filepath.Join(dir, "with_text.pdf")
	content := "%PDF-1.4\n1 0 obj\n<< /Type /Page /Parent 2 0 R /Contents 3 0 R >>\nendobj\n3 0 obj\n<< >>\nstream\nBT\n/F1 12 Tf\n(Hello) Tj\nET\nendstream\nendobj\n"
	if err := os.WriteFile(pdfWithText, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasTextLayer(pdfWithText) {
		t.Error("expected HasTextLayer to return true for PDF with BT/ET")
	}

	// Create a minimal PDF without text operators
	pdfNoText := filepath.Join(dir, "no_text.pdf")
	content2 := "%PDF-1.4\n1 0 obj\n<< /Type /Page >>\nendobj\n"
	if err := os.WriteFile(pdfNoText, []byte(content2), 0644); err != nil {
		t.Fatal(err)
	}
	if HasTextLayer(pdfNoText) {
		t.Error("expected HasTextLayer to return false for PDF without BT/ET")
	}

	// Non-existent file
	if HasTextLayer(filepath.Join(dir, "nonexistent.pdf")) {
		t.Error("expected HasTextLayer to return false for missing file")
	}
}

func TestOCRTextFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ocr.txt")
	content := "Hello World\nThis is OCR text"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	text, err := OCRTextFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if text != content {
		t.Errorf("expected %q, got %q", content, text)
	}
}
