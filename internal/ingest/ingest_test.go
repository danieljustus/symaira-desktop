package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestFile(t *testing.T) {
	// Ensure we test the fallback path by removing symingest from PATH
	t.Setenv("PATH", "/usr/bin:/bin")
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

func TestIngestDelegation(t *testing.T) {
	tempDir := t.TempDir()

	mockSymingest := filepath.Join(tempDir, "symingest")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then\n" +
		"	echo '{\"schema_version\": 1, \"version\": \"0.7.0\"}'\n" +
		"	exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"ingest\" ]; then\n" +
		"	if [ \"$4\" = \"--json\" ]; then\n" +
		"		echo '{\"path\": \"mock_output.md\"}'\n" +
		"		exit 0\n" +
		"	fi\n" +
		"	exit 1\n" +
		"fi\n" +
		"exit 1\n"

	if err := os.WriteFile(mockSymingest, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write mock script: %v", err)
	}

	t.Setenv("PATH", tempDir+":"+os.Getenv("PATH"))

	ok, _ := HasSymingest()
	if !ok {
		t.Fatalf("expected HasSymingest to be true with mock")
	}

	vaultRoot := t.TempDir()
	srcFile := filepath.Join(t.TempDir(), "test.txt")
	os.WriteFile(srcFile, []byte("test"), 0644)

	relPath, err := IngestFile(vaultRoot, srcFile)
	if err != nil {
		t.Fatalf("IngestFile failed: %v", err)
	}

	if relPath != "mock_output.md" {
		t.Errorf("expected mock_output.md, got %s", relPath)
	}
}
