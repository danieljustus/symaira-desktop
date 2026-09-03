package paperless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseManifest(t *testing.T) {
	// Create a temporary manifest
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")

	entries := []map[string]any{
		{
			"id":                    1,
			"correspondent":         "Acme Corp",
			"document_type":         "Invoice",
			"title":                 "Invoice 2024-01",
			"content":               "This is the OCR content",
			"tags":                  []string{"invoice", "2024"},
			"created":               "2024-01-15T10:30:00Z",
			"added":                 "2024-01-15T10:30:00Z",
			"modified":              "2024-01-15T12:00:00Z",
			"file_size":             12345,
			"checksum":              "abc123def456",
			"archive_serial_number": 42,
			"original_file_name":    "invoice.pdf",
			"archived_file_name":    "0000001.pdf",
			"notes": []map[string]any{
				{"note": "Paid on 2024-02-01", "created": "2024-02-01T00:00:00Z", "user": 1},
			},
		},
	}

	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	result, err := ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}

	e := result[0]
	if e.ID != 1 {
		t.Errorf("expected ID 1, got %d", e.ID)
	}
	if e.Correspondent == nil || *e.Correspondent != "Acme Corp" {
		t.Errorf("expected correspondent 'Acme Corp', got %v", e.Correspondent)
	}
	if e.DocumentType == nil || *e.DocumentType != "Invoice" {
		t.Errorf("expected document_type 'Invoice', got %v", e.DocumentType)
	}
	if e.Title != "Invoice 2024-01" {
		t.Errorf("expected title 'Invoice 2024-01', got %q", e.Title)
	}
	if e.Content != "This is the OCR content" {
		t.Errorf("unexpected content: %q", e.Content)
	}
	if len(e.Tags) != 2 || e.Tags[0] != "invoice" || e.Tags[1] != "2024" {
		t.Errorf("unexpected tags: %v", e.Tags)
	}
	if e.Checksum == nil || *e.Checksum != "abc123def456" {
		t.Errorf("expected checksum, got %v", e.Checksum)
	}
	if e.ArchiveSerialNumber == nil || *e.ArchiveSerialNumber != 42 {
		t.Errorf("expected ASN 42, got %v", e.ArchiveSerialNumber)
	}
	if len(e.Notes) != 1 {
		t.Errorf("expected 1 note, got %d", len(e.Notes))
	}
	if e.Notes[0].Note != "Paid on 2024-02-01" {
		t.Errorf("unexpected note text: %q", e.Notes[0].Note)
	}
}

func TestParseManifest_Empty(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")

	data := []byte("[]")
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	result, err := ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result))
	}
}

func TestParseManifest_MissingFile(t *testing.T) {
	_, err := ParseManifest("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSourceIdentifier(t *testing.T) {
	e := ManifestEntry{
		ID:       123,
		Checksum: ptr("sha256:abcdef"),
	}
	got := sourceIdentifier(e)
	want := "paperless:123:sha256:abcdef"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	e2 := ManifestEntry{ID: 456}
	got = sourceIdentifier(e2)
	if !strings.HasPrefix(got, "paperless:456:") {
		t.Errorf("expected prefix 'paperless:456:', got %q", got)
	}
}

func TestNoteFileName(t *testing.T) {
	tests := []struct {
		title      string
		id         int
		contains   []string
		notContain []string
	}{
		{"Invoice 2024-01", 1,
			[]string{"Paperless_1_", ".md"},
			[]string{},
		},
		{"Test/File: Name", 42,
			[]string{"Paperless_42_", ".md"},
			[]string{},
		},
		{"", 99,
			[]string{"Paperless_99_doc_99.md"},
			[]string{},
		},
	}

	for _, tt := range tests {
		got := noteFileName(ManifestEntry{ID: tt.id, Title: tt.title})
		for _, c := range tt.contains {
			if !strings.Contains(got, c) {
				t.Errorf("noteFileName(%q, %d) = %q, expected to contain %q", tt.title, tt.id, got, c)
			}
		}
		for _, nc := range tt.notContain {
			if strings.Contains(got, nc) {
				t.Errorf("noteFileName(%q, %d) = %q, should not contain %q", tt.title, tt.id, got, nc)
			}
		}
		if !strings.HasSuffix(got, ".md") {
			t.Errorf("noteFileName(%q, %d) = %q, should end with .md", tt.title, tt.id, got)
		}
		if len(got) > 200 {
			t.Errorf("noteFileName(%q, %d) = %q, name too long (%d chars)", tt.title, tt.id, got, len(got))
		}
	}
}

func TestArchiveFileName(t *testing.T) {
	e := ManifestEntry{
		ArchivedFileName: ptr("0000001.pdf"),
		OriginalFileName: ptr("invoice.pdf"),
	}
	if got := archiveFileName(e); got != "0000001.pdf" {
		t.Errorf("expected archived name, got %q", got)
	}

	e.ArchivedFileName = nil
	if got := archiveFileName(e); got != "invoice.pdf" {
		t.Errorf("expected original name, got %q", got)
	}

	e.OriginalFileName = nil
	got := archiveFileName(e)
	if !strings.HasPrefix(got, "paperless_") {
		t.Errorf("expected fallback name, got %q", got)
	}
}

func TestBuildNote(t *testing.T) {
	entry := ManifestEntry{
		ID:                  1,
		Correspondent:       ptr("Acme Corp"),
		DocumentType:        ptr("Invoice"),
		Title:               "Invoice 2024-01",
		Content:             "OCR text content here.",
		Tags:                []string{"invoice", "2024"},
		Created:             ptr("2024-01-15T10:30:00Z"),
		Added:               ptr("2024-01-15T10:30:00Z"),
		Modified:            ptr("2024-01-15T12:00:00Z"),
		Checksum:            ptr("abc123"),
		ArchiveSerialNumber: ptr(42),
		OriginalFileName:    ptr("invoice.pdf"),
		ArchivedFileName:    ptr("0000001.pdf"),
		Notes: []ManifestNote{
			{Note: "Paid on 2024-02-01", Created: ptr("2024-02-01T00:00:00Z")},
		},
	}

	note := mustBuildNote(entry)

	// Check frontmatter
	if !strings.Contains(note, "title: \"Invoice 2024-01\"") {
		t.Error("missing title")
	}
	if !strings.Contains(note, "correspondent: \"Acme Corp\"") {
		t.Error("missing correspondent")
	}
	if !strings.Contains(note, "document_type: \"Invoice\"") {
		t.Error("missing document_type")
	}
	if !strings.Contains(note, "document_date: \"2024-01-15\"") {
		t.Error("missing document_date")
	}
	if !strings.Contains(note, "asn: 42") {
		t.Error("missing asn")
	}
	if !strings.Contains(note, "source_identifier: \"paperless:1:abc123\"") {
		t.Error("missing source_identifier")
	}
	if !strings.Contains(note, "imported_from: \"paperless\"") {
		t.Error("missing imported_from")
	}
	if !strings.Contains(note, "archive_path: \"archive/paperless/0000001.pdf\"") {
		t.Error("missing archive_path")
	}
	if !strings.Contains(note, "tags:\n  - \"invoice\"\n  - \"2024\"") {
		t.Error("missing tags")
	}
	if !strings.Contains(note, "status: \"open\"") {
		t.Error("missing status")
	}
	if !strings.Contains(note, "confidence: 100") {
		t.Error("missing confidence")
	}
	if !strings.Contains(note, "mime: \"application/pdf\"") {
		t.Error("missing mime type")
	}
	if !strings.Contains(note, "paperless:\n  id: 1") {
		t.Error("missing paperless metadata")
	}
	if !strings.Contains(note, "OCR text content here.") {
		t.Error("missing OCR content")
	}
	if !strings.Contains(note, "[[archive/paperless/0000001.pdf]]") {
		t.Error("missing wikilink to archive file")
	}
	if !strings.Contains(note, "## Paperless Notes") {
		t.Error("missing paperless notes section")
	}
	if !strings.Contains(note, "Paid on 2024-02-01") {
		t.Error("missing paperless note text")
	}
}

func TestBuildNote_Minimal(t *testing.T) {
	entry := ManifestEntry{
		ID:      1,
		Title:   "Minimal Document",
		Content: "content",
	}

	note := mustBuildNote(entry)

	if !strings.Contains(note, "title: \"Minimal Document\"") {
		t.Error("missing title")
	}
	if !strings.Contains(note, "tags: []") {
		t.Error("missing tags")
	}
	if !strings.Contains(note, "source_identifier: \"paperless:1:\"") {
		t.Error("missing source_identifier")
	}
	// Should NOT contain optional fields
	if strings.Contains(note, "correspondent:") {
		t.Error("unexpected correspondent")
	}
	if strings.Contains(note, "asn:") {
		t.Error("unexpected asn")
	}
}

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"file/name:test", "file_name_test"},
		{"test*?.txt", "test_.txt"},           // * → _, ? → _, then __ collapsed
		{"trailing dots...", "trailing dots"}, // dots trimmed, spaces left
		{"a\nb	c", "a_b_c"},
		{strings.Repeat("x", 150), strings.Repeat("x", 120)},
	}

	for _, tt := range tests {
		got := sanitizeFileName(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExtractDate(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"2024-01-15T10:30:00Z", "2024-01-15"},
		{"2024-01-15", "2024-01-15"},
		{"short", ""}, // less than 10 chars → empty
		{"", ""},
	}

	for _, tt := range tests {
		got := extractDate(tt.input)
		if got != tt.expected {
			t.Errorf("extractDate(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMimeFromExt(t *testing.T) {
	tests := []struct {
		ext      string
		expected string
	}{
		{".pdf", "application/pdf"},
		{".png", "image/png"},
		{".jpg", "image/jpeg"},
		{".jpeg", "image/jpeg"},
		{".tiff", "image/tiff"},
		{".gif", "image/gif"},
		{".txt", "text/plain"},
		{".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{".odt", "application/vnd.oasis.opendocument.text"},
		{".xyz", ""},
	}

	for _, tt := range tests {
		got := mimeFromExt(tt.ext)
		if got != tt.expected {
			t.Errorf("mimeFromExt(%q) = %q, want %q", tt.ext, got, tt.expected)
		}
	}
}

func TestIsManifestFile(t *testing.T) {
	if !IsManifestFile("manifest.json") {
		t.Error("expected true for manifest.json")
	}
	if IsManifestFile("other.json") {
		t.Error("expected false for other.json")
	}
}

// Helper

func ptr[T any](v T) *T { return &v }

func mustBuildNote(entry ManifestEntry) string {
	return buildNote(entry, "archive/paperless/0000001.pdf", sourceIdentifier(entry), time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC))
}
