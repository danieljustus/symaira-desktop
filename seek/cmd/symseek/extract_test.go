package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-seek/internal/config"
	"github.com/danieljustus/symaira-seek/internal/db"
)

const testSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func writeIndexedDocWithSidecar(t *testing.T, tmpDir string) string {
	t.Helper()
	docDir := filepath.Join(tmpDir, "docs")
	if err := os.MkdirAll(docDir, 0700); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(docDir, "invoice.md")
	content := "---\nsha256: " + testSHA256 + "\n---\n\n" +
		"Total due for this invoice is $500.00 payable by month end.\n"
	if err := os.WriteFile(docPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	sidecarDir := filepath.Join(docDir, ".symaira", "extractions")
	if err := os.MkdirAll(sidecarDir, 0700); err != nil {
		t.Fatal(err)
	}
	sidecarPath := filepath.Join(sidecarDir, testSHA256+".jsonl")
	line := `{"field":"amount","type":"amount","value":"$500.00","span":{"start":10,"end":20,"snippet":"Total due"},"matched":true}` + "\n"
	if err := os.WriteFile(sidecarPath, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	return docDir
}

// TestExtractSearchCmd_FindsEvidenceFromFixture covers acceptance criterion:
// "symseek extract search can find extraction text/evidence from a fixture."
func TestExtractSearchCmd_FindsEvidenceFromFixture(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	resetGlobals()
	cfg = *config.DefaultConfig()

	docDir := writeIndexedDocWithSidecar(t, tmpDir)

	indexCmd := newRootCmd()
	indexCmd.SetArgs([]string{"index", docDir})
	if err := indexCmd.Execute(); err != nil {
		t.Fatalf("index: %v", err)
	}

	searchCmd := newRootCmd()
	searchCmd.SetArgs([]string{"extract", "search", "Total"})
	out := captureStdout(t, func() {
		if err := searchCmd.Execute(); err != nil {
			t.Fatalf("extract search: %v", err)
		}
	})
	if !strings.Contains(out, "amount") || !strings.Contains(out, "$500.00") {
		t.Errorf("expected amount extraction in output, got %q", out)
	}
}

func TestExtractSearchCmd_JSONOutput(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	resetGlobals()
	cfg = *config.DefaultConfig()

	docDir := writeIndexedDocWithSidecar(t, tmpDir)
	indexCmd := newRootCmd()
	indexCmd.SetArgs([]string{"index", docDir})
	if err := indexCmd.Execute(); err != nil {
		t.Fatalf("index: %v", err)
	}

	searchCmd := newRootCmd()
	searchCmd.SetArgs([]string{"extract", "search", "Total", "--json"})
	out := captureStdout(t, func() {
		if err := searchCmd.Execute(); err != nil {
			t.Fatalf("extract search: %v", err)
		}
	})

	var results []*db.Extraction
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Class != "amount" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestExtractListCmd_FilterByClass(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	resetGlobals()
	cfg = *config.DefaultConfig()

	docDir := writeIndexedDocWithSidecar(t, tmpDir)
	indexCmd := newRootCmd()
	indexCmd.SetArgs([]string{"index", docDir})
	if err := indexCmd.Execute(); err != nil {
		t.Fatalf("index: %v", err)
	}

	listCmd := newRootCmd()
	listCmd.SetArgs([]string{"extract", "list", "--class", "amount"})
	out := captureStdout(t, func() {
		if err := listCmd.Execute(); err != nil {
			t.Fatalf("extract list: %v", err)
		}
	})
	if !strings.Contains(out, "amount") {
		t.Errorf("expected amount class in output, got %q", out)
	}

	listCmd2 := newRootCmd()
	listCmd2.SetArgs([]string{"extract", "list", "--class", "deadline"})
	out2 := captureStdout(t, func() {
		if err := listCmd2.Execute(); err != nil {
			t.Fatalf("extract list: %v", err)
		}
	})
	if !strings.Contains(out2, "No matching extractions found.") {
		t.Errorf("expected no results for unused class, got %q", out2)
	}
}

// TestExtractImportCmd_InfersDocFromFilename covers manual import when a
// sidecar is added after indexing (auto-detection at index time only fires
// if the sidecar already exists on disk).
func TestExtractImportCmd_InfersDocFromFilename(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	resetGlobals()
	cfg = *config.DefaultConfig()

	docDir := filepath.Join(tmpDir, "docs")
	if err := os.MkdirAll(docDir, 0700); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(docDir, "note.md")
	if err := os.WriteFile(docPath, []byte("# Note\nNo sidecar yet."), 0600); err != nil {
		t.Fatal(err)
	}

	indexCmd := newRootCmd()
	indexCmd.SetArgs([]string{"index", docDir})
	if err := indexCmd.Execute(); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Discover the hash symseek assigned so the sidecar filename matches it.
	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	doc, err := dbClient.GetDocument(docPath)
	if err != nil || doc == nil {
		t.Fatalf("expected indexed document, err=%v doc=%v", err, doc)
	}
	dbClient.Close()

	sidecarPath := filepath.Join(tmpDir, doc.Hash+".jsonl")
	line := `{"field":"party","value":"Acme Corp","matched":true}` + "\n"
	if err := os.WriteFile(sidecarPath, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}

	importCmd := newRootCmd()
	importCmd.SetArgs([]string{"extract", "import", sidecarPath})
	if err := importCmd.Execute(); err != nil {
		t.Fatalf("extract import: %v", err)
	}

	dbClient2, err := db.Open()
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer dbClient2.Close()
	extractions, err := dbClient2.GetDocumentExtractions(docPath)
	if err != nil {
		t.Fatalf("GetDocumentExtractions: %v", err)
	}
	if len(extractions) != 1 || extractions[0].Class != "party" {
		t.Errorf("unexpected extractions after import: %+v", extractions)
	}
}

func TestExtractImportCmd_NoMatchingDocument(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	resetGlobals()
	cfg = *config.DefaultConfig()

	sidecarPath := filepath.Join(tmpDir, "nonexistent-hash.jsonl")
	if err := os.WriteFile(sidecarPath, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	importCmd := newRootCmd()
	importCmd.SetArgs([]string{"extract", "import", sidecarPath})
	err := importCmd.Execute()
	if err == nil {
		t.Fatal("expected error when no document matches the sidecar filename")
	}
}
