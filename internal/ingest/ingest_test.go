package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestFile(t *testing.T) {
	vaultRoot := t.TempDir()
	srcDir := t.TempDir()

	src := filepath.Join(srcDir, "scan.pdf")
	if err := os.WriteFile(src, []byte("%PDF-1.4 fake"), 0644); err != nil {
		t.Fatal(err)
	}

	relNote, err := IngestFile(vaultRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(relNote, "inbox/") || !strings.HasSuffix(relNote, ".md") {
		t.Errorf("unexpected note path: %s", relNote)
	}

	noteBytes, err := os.ReadFile(filepath.Join(vaultRoot, relNote))
	if err != nil {
		t.Fatal(err)
	}
	note := string(noteBytes)
	if !strings.Contains(note, `status: "inbox"`) {
		t.Errorf("frontmatter missing status: %s", note)
	}
	if !strings.Contains(note, "scan_") || !strings.Contains(note, ".pdf") {
		t.Errorf("note does not embed the copied asset: %s", note)
	}

	// The original file must be copied into the inbox.
	entries, err := os.ReadDir(filepath.Join(vaultRoot, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	var foundAsset bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "scan_") && strings.HasSuffix(e.Name(), ".pdf") {
			foundAsset = true
		}
	}
	if !foundAsset {
		t.Error("copied asset not found in inbox")
	}
}

func TestIngestFileMissingSource(t *testing.T) {
	if _, err := IngestFile(t.TempDir(), "/nonexistent/file.pdf"); err == nil {
		t.Error("expected error for missing source")
	}
}
