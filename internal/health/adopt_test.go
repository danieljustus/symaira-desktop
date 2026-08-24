package health

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestAdoptDryRunModifiesNothing(t *testing.T) {
	root := t.TempDir()
	file1 := "notes/first.md"
	file2 := "2026-08-24.md"
	writeNote(t, root, file1, "# First Note Title\n\nSome body text.\n")
	writeNote(t, root, file2, "Just text without heading.\n")

	p1 := filepath.Join(root, file1)
	p2 := filepath.Join(root, file2)

	b1Before, _ := os.ReadFile(p1) //nolint:gosec // p1 is a test fixture path
	b2Before, _ := os.ReadFile(p2) //nolint:gosec // p2 is a test fixture path
	hash1Before := hex.EncodeToString(sha256.New().Sum(b1Before))
	hash2Before := hex.EncodeToString(sha256.New().Sum(b2Before))

	report, err := Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !report.DryRun {
		t.Errorf("expected DryRun true")
	}
	if report.Total != 2 || report.Adopted != 2 || report.Skipped != 0 || report.Failed != 0 {
		t.Errorf("unexpected counts: total=%d, adopted=%d, skipped=%d, failed=%d", report.Total, report.Adopted, report.Skipped, report.Failed)
	}

	// Verify concrete proposed values in report
	for _, doc := range report.Documents {
		switch doc.Path {
		case "notes/first.md":
			if doc.Title != "First Note Title" {
				t.Errorf("expected title 'First Note Title', got %q", doc.Title)
			}
			if doc.Created == "" {
				t.Errorf("expected non-empty created")
			}
			if len(doc.Tags) != 0 {
				t.Errorf("expected empty tags slice, got %v", doc.Tags)
			}
		case "2026-08-24.md":
			if doc.Title != "2026-08-24" {
				t.Errorf("expected title '2026-08-24', got %q", doc.Title)
			}
			if doc.Created != "2026-08-24T00:00:00Z" {
				t.Errorf("expected created '2026-08-24T00:00:00Z', got %q", doc.Created)
			}
		}
	}

	// Verify files on disk are completely unchanged
	b1After, _ := os.ReadFile(p1) //nolint:gosec // p1 is a test fixture path
	b2After, _ := os.ReadFile(p2) //nolint:gosec // p2 is a test fixture path
	hash1After := hex.EncodeToString(sha256.New().Sum(b1After))
	hash2After := hex.EncodeToString(sha256.New().Sum(b2After))

	if hash1Before != hash1After || hash2Before != hash2After {
		t.Errorf("dry-run modified files on disk!")
	}
}

func TestAdoptAppliesMissingRequiredFields(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "note.md", "# My Note\nBody content\n")

	report, err := Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Adopted != 1 {
		t.Fatalf("expected 1 adopted file, got %d", report.Adopted)
	}

	doc, err := vault.ParseFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatalf("parse file failed: %v", err)
	}
	if doc.Title != "My Note" {
		t.Errorf("expected title 'My Note', got %q", doc.Title)
	}
	if doc.Created == "" {
		t.Errorf("expected non-empty created")
	}
	if _, ok := doc.Frontmatter["tags"]; !ok {
		t.Errorf("expected tags in frontmatter")
	}
}

func TestAdoptLeavesCompliantFilesUntouched(t *testing.T) {
	root := t.TempDir()
	content := "---\ntitle: \"Compliant Note\"\ncreated: \"2026-08-01T00:00:00Z\"\ntags: [tag1, tag2]\n---\n# Compliant Note\nBody\n"
	writeNote(t, root, "compliant.md", content)

	p := filepath.Join(root, "compliant.md")
	bBefore, _ := os.ReadFile(p) //nolint:gosec // p is a test fixture path
	hashBefore := hex.EncodeToString(sha256.New().Sum(bBefore))

	report, err := Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Total != 1 || report.Skipped != 1 || report.Adopted != 0 {
		t.Errorf("expected total=1, skipped=1, adopted=0, got %+v", report)
	}

	bAfter, _ := os.ReadFile(p) //nolint:gosec // p is a test fixture path
	hashAfter := hex.EncodeToString(sha256.New().Sum(bAfter))

	if hashBefore != hashAfter {
		t.Errorf("compliant file was modified on disk! Hash before=%s, hash after=%s", hashBefore, hashAfter)
	}
}

func TestAdoptPreservesUnknownFieldsAndComments(t *testing.T) {
	root := t.TempDir()
	content := "---\naliases:\n  - custom-alias\ncustom_field: 42\n# Important comment\n---\n# My Note\nBody text.\n"
	writeNote(t, root, "partial.md", content)

	report, err := Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Adopted != 1 {
		t.Fatalf("expected 1 adopted, got %d", report.Adopted)
	}

	p := filepath.Join(root, "partial.md")
	bAfter, _ := os.ReadFile(p) //nolint:gosec // p is a test fixture path
	afterStr := string(bAfter)

	// Ensure unknown keys and comments are preserved
	if !strings.Contains(afterStr, "aliases:\n  - custom-alias") {
		t.Errorf("aliases not preserved in:\n%s", afterStr)
	}
	if !strings.Contains(afterStr, "custom_field: 42") {
		t.Errorf("custom_field not preserved in:\n%s", afterStr)
	}
	if !strings.Contains(afterStr, "# Important comment") {
		t.Errorf("comment not preserved in:\n%s", afterStr)
	}
	if !strings.Contains(afterStr, "title: \"My Note\"") {
		t.Errorf("title not added in:\n%s", afterStr)
	}
	if !strings.Contains(afterStr, "tags: []") {
		t.Errorf("tags not added in:\n%s", afterStr)
	}
}

func TestAdoptSubsequentHealthScanClean(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "# Heading A\nText A\n")
	writeNote(t, root, "b.md", "Text B\n")

	// Pre-adoption scan has missing_frontmatter warnings
	reportBefore, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if reportBefore.Healthy || len(reportBefore.Findings) != 2 {
		t.Fatalf("expected 2 missing_frontmatter findings before adopt, got %d", len(reportBefore.Findings))
	}

	// Adopt
	_, err = Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    false,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Post-adoption scan
	reportAfter, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range reportAfter.Findings {
		if f.Category == "missing_frontmatter" {
			t.Errorf("unexpected missing_frontmatter finding after adopt: %+v", f)
		}
	}
}

func TestAdoptCreatedDerivations(t *testing.T) {
	root := t.TempDir()

	// 1. Daily note
	writeNote(t, root, "2026-08-24.md", "Daily note text\n")
	// 2. Standard note with specific mtime
	writeNote(t, root, "standard.md", "Standard note text\n")
	mtime := time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC)
	_ = os.Chtimes(filepath.Join(root, "standard.md"), mtime, mtime)

	report, err := Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Adopted != 2 {
		t.Fatalf("expected 2 adopted, got %d", report.Adopted)
	}

	docDaily, err := vault.ParseFile(filepath.Join(root, "2026-08-24.md"))
	if err != nil {
		t.Fatal(err)
	}
	if docDaily.Created != "2026-08-24T00:00:00Z" {
		t.Errorf("daily note created = %q, want '2026-08-24T00:00:00Z'", docDaily.Created)
	}

	docStd, err := vault.ParseFile(filepath.Join(root, "standard.md"))
	if err != nil {
		t.Fatal(err)
	}
	if docStd.Created == "" {
		t.Errorf("standard note created should not be empty")
	}
}

func TestAdoptHistorySafety(t *testing.T) {
	root := t.TempDir()
	originalContent := "# Note\nOriginal text before adopt\n"
	writeNote(t, root, "history_note.md", originalContent)

	histStore := history.NewStore(root)

	_, err := Adopt(AdoptOptions{
		VaultRoot: root,
		DryRun:    false,
		History:   histStore,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify history snapshot exists
	entries, err := histStore.List("history_note.md")
	if err != nil {
		t.Fatalf("list history failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one history entry for history_note.md")
	}

	// Verify content in snapshot matches original pre-adoption content
	blob, err := histStore.Content(entries[0].ID)
	if err != nil {
		t.Fatalf("failed to get snapshot content: %v", err)
	}
	if string(blob) != originalContent {
		t.Errorf("snapshot content:\n%s\nwant:\n%s", string(blob), originalContent)
	}

	// Restore original content
	_, err = histStore.Restore("history_note.md", entries[0].ID)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	restored, _ := os.ReadFile(filepath.Join(root, "history_note.md")) //nolint:gosec // root is a test fixture root
	if string(restored) != originalContent {
		t.Errorf("restored content:\n%s\nwant:\n%s", string(restored), originalContent)
	}
}

func TestWriteAdoptReport(t *testing.T) {
	root := t.TempDir()
	reportPath := filepath.Join(root, "report.json")
	report := &AdoptReport{
		SchemaVersion: AdoptReportSchemaVersion,
		Vault:         root,
		Total:         1,
		Adopted:       1,
	}
	if err := WriteAdoptReport(reportPath, report); err != nil {
		t.Fatalf("WriteAdoptReport failed: %v", err)
	}
	data, err := os.ReadFile(reportPath) //nolint:gosec // reportPath is a test fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"schema_version": 1`) {
		t.Errorf("missing schema_version in report output: %s", string(data))
	}
}
