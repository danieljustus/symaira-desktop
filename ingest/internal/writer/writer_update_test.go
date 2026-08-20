package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateNote_UpdatesMachineFieldsPreservesUserFields(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	// Write an initial note
	initialPath, err := w.WriteNote(
		"/tmp/original.pdf", "hash1", "application/pdf", "tesseract", "Original body",
		"/archive/hash1.pdf", time.Unix(100, 0).UTC(), "invoice", []string{"financial"},
		"Acme Corp", "Invoice", "", "", "", "", nil, nil,
	)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	// Now update it with new machine fields
	newTime := time.Unix(200, 0).UTC()
	err = w.UpdateNote(initialPath, Note{
		SourcePath:    "/tmp/updated.pdf",
		IngestedAt:    newTime,
		SHA256:        "hash2",
		MIME:          "image/png",
		Tags:          []string{"receipt"},
		Category:      "expense",
		Correspondent: "New Corp",
		DocumentType:  "Receipt",
		OCREngine:     "tesseract",
		ArchivePath:   "/archive/hash2.png",
	}, "Updated body text")
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	data, err := os.ReadFile(initialPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// Machine fields should be updated
	for _, want := range []string{
		"source_path: /tmp/updated.pdf",
		"sha256: hash2",
		"mime: image/png",
		"category: expense",
		"correspondent: New Corp",
		"document_type: Receipt",
		"ocr_engine: tesseract",
		"archive_path: /archive/hash2.png",
		"Updated body text",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestUpdateNote_DeleteCorrespondentWhenEmpty(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	// Write note with a correspondent
	path, err := w.WriteNote(
		"/tmp/doc.pdf", "hash", "application/pdf", "", "body",
		"", time.Unix(0, 0).UTC(), "", nil, "Old Corp", "OldType", "", "", "", "", nil, nil,
	)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	// Update with empty correspondent and document_type — should delete them
	err = w.UpdateNote(path, Note{
		SourcePath: "/tmp/doc.pdf",
		IngestedAt: time.Unix(0, 0).UTC(),
		SHA256:     "hash",
		MIME:       "application/pdf",
		Tags:       []string{},
	}, "body")
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if strings.Contains(out, "correspondent:") {
		t.Error("correspondent should be deleted when empty")
	}
	if strings.Contains(out, "document_type:") {
		t.Error("document_type should be deleted when empty")
	}
}

func TestUpdateNote_NonexistentFile(t *testing.T) {
	w := &NoteWriter{Vault: t.TempDir()}
	err := w.UpdateNote(filepath.Join(t.TempDir(), "nope.md"), Note{}, "body")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestUpdateNote_InvalidFrontmatter(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	path := filepath.Join(vault, "bad.md")
	if err := os.WriteFile(path, []byte("not yaml at all\nno frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := w.UpdateNote(path, Note{}, "body")
	if err == nil {
		t.Fatal("expected error for invalid frontmatter")
	}
}

func TestRenderBody_WithArchivePath(t *testing.T) {
	body := renderBody("hello world", "/archive/file.pdf")
	if !strings.Contains(body, "hello world") {
		t.Error("missing body text")
	}
	if !strings.Contains(body, "[Archived Original](file:///archive/file.pdf)") {
		t.Error("missing archive link")
	}
	if !strings.HasSuffix(body, "\n") {
		t.Error("body should end with newline")
	}
}

func TestRenderBody_WithoutArchivePath(t *testing.T) {
	body := renderBody("hello world", "")
	if !strings.Contains(body, "hello world") {
		t.Error("missing body text")
	}
	if strings.Contains(body, "[Archived Original]") {
		t.Error("should not contain archive link when archivePath is empty")
	}
	if !strings.HasSuffix(body, "\n") {
		t.Error("body should end with newline")
	}
}

func TestRenderBody_TextAlreadyEndsWithNewline(t *testing.T) {
	body := renderBody("hello\n", "")
	if !strings.HasSuffix(body, "hello\n") {
		t.Error("should not add extra newline when text already ends with one")
	}
	// Should not have double newline
	if strings.Contains(body, "hello\n\n") {
		t.Error("should not add double newline")
	}
}

func TestWriteNote_WithArchivePath(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	path, err := w.WriteNote(
		"/tmp/doc.pdf", "hash", "application/pdf", "", "body text",
		"/archive/hash.pdf", time.Unix(0, 0).UTC(), "", nil, "", "", "", "", "", "", nil, nil,
	)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "[Archived Original](file:///archive/hash.pdf)") {
		t.Error("WriteNote with archivePath should include archive link")
	}
}

func TestParseFrontmatter_MalformedYAML(t *testing.T) {
	content := "---\n: : invalid yaml\n---\nbody"
	_, _, err := parseFrontmatter(content)
	if err == nil {
		t.Fatal("expected error for malformed YAML frontmatter")
	}
}

func TestUpdateMachineFields_NilTags(t *testing.T) {
	fields := map[string]interface{}{"old_key": "value"}
	meta := Note{
		SourcePath: "/path",
		IngestedAt: time.Unix(0, 0),
		SHA256:     "hash",
		MIME:       "text/plain",
		Tags:       nil,
		Category:   "cat",
	}
	updateMachineFields(fields, meta)

	tags, ok := fields["tags"].([]string)
	if !ok {
		t.Fatalf("tags = %T, want []string", fields["tags"])
	}
	if len(tags) != 0 {
		t.Errorf("tags = %v, want empty slice", tags)
	}
}

func TestUpdateMachineFields_WithTags(t *testing.T) {
	fields := map[string]interface{}{}
	meta := Note{
		SourcePath: "/path",
		IngestedAt: time.Unix(0, 0),
		SHA256:     "hash",
		MIME:       "text/plain",
		Tags:       []string{"a", "b"},
		Category:   "cat",
	}
	updateMachineFields(fields, meta)

	tags, ok := fields["tags"].([]string)
	if !ok {
		t.Fatalf("tags = %T, want []string", fields["tags"])
	}
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("tags = %v, want [a b]", tags)
	}
}
