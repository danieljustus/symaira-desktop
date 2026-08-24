package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackfillFrontmatterBytes_NoFrontmatter(t *testing.T) {
	input := []byte("# Hello World\n\nThis is a note.\n")
	missing := map[string]interface{}{
		"title":   "Hello World",
		"created": "2026-08-24T00:00:00Z",
		"tags":    []string{},
	}

	got, err := BackfillFrontmatterBytes(input, missing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "---\ntitle: \"Hello World\"\ncreated: \"2026-08-24T00:00:00Z\"\ntags: []\n---\n# Hello World\n\nThis is a note.\n"
	if string(got) != expected {
		t.Errorf("got:\n%s\nwant:\n%s", string(got), expected)
	}
}

func TestBackfillFrontmatterBytes_ExistingPartialFrontmatterPreservesUnknownKeys(t *testing.T) {
	input := []byte("---\naliases:\n  - custom-alias\ncustom_field: 42\n---\n# Note Content\n")
	missing := map[string]interface{}{
		"title":   "Note Content",
		"created": "2026-08-24T12:00:00Z",
		"tags":    []string{},
	}

	got, err := BackfillFrontmatterBytes(input, missing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "---\naliases:\n  - custom-alias\ncustom_field: 42\ntitle: \"Note Content\"\ncreated: \"2026-08-24T12:00:00Z\"\ntags: []\n---\n# Note Content\n"
	if string(got) != expected {
		t.Errorf("got:\n%s\nwant:\n%s", string(got), expected)
	}
}

func TestBackfillFrontmatterBytes_PreservesExistingRequiredKeys(t *testing.T) {
	input := []byte("---\ntitle: \"Existing Title\"\ntags:\n  - tag1\n---\nBody\n")
	missing := map[string]interface{}{
		"created": "2026-08-24T00:00:00Z",
	}

	got, err := BackfillFrontmatterBytes(input, missing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "---\ntitle: \"Existing Title\"\ntags:\n  - tag1\ncreated: \"2026-08-24T00:00:00Z\"\n---\nBody\n"
	if string(got) != expected {
		t.Errorf("got:\n%s\nwant:\n%s", string(got), expected)
	}
}

func TestBackfillFrontmatterBytes_CRLFLineEndings(t *testing.T) {
	input := []byte("# Heading\r\n\r\nBody text.\r\n")
	missing := map[string]interface{}{
		"title":   "Heading",
		"created": "2026-08-24T00:00:00Z",
		"tags":    []string{},
	}

	got, err := BackfillFrontmatterBytes(input, missing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(string(got), "\r\n") {
		t.Errorf("expected CRLF line endings preserved, got: %q", string(got))
	}
}

func TestBackfillFrontmatter_FileAtomicAndUnchangedNoOp(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.md")
	content := []byte("---\ntitle: \"Existing\"\ncreated: \"2026-08-24T00:00:00Z\"\ntags: []\n---\n# Content\n")
	if err := os.WriteFile(filePath, content, 0600); err != nil {
		t.Fatal(err)
	}

	hashBefore := sha256.Sum256(content)

	// No missing fields
	err := BackfillFrontmatter(filePath, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	readAfter, err := os.ReadFile(filePath) //nolint:gosec // filePath is the test fixture path
	if err != nil {
		t.Fatal(err)
	}
	hashAfter := sha256.Sum256(readAfter)

	if hex.EncodeToString(hashBefore[:]) != hex.EncodeToString(hashAfter[:]) {
		t.Errorf("expected file to be untouched, but hashes differ")
	}
}
