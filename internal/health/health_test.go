package health

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFindsBrokenLinksAndBuildsPlan(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "source.md", "---\ntitle: Source\n---\n[[target.md]]\n[[missing.md]]\n")
	writeNote(t, root, "target.md", "---\ntitle: Target\n---\ncontent\n")

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 2 {
		t.Fatalf("expected two files, got %d", report.FilesScanned)
	}
	if report.Healthy || len(report.Findings) != 1 {
		t.Fatalf("expected one health finding, got %#v", report)
	}
	if report.Findings[0].Category != "broken_wikilink" || len(report.RepairPlan) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestScanDoesNotFlagBarePersonLinks(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "note.md", "---\ntitle: Note\n---\n[[Jane Doe]]\n")

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings, got %#v", report.Findings)
	}
}

func TestScanReportsParseErrorsWithoutStopping(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "good.md", "---\ntitle: Good\n---\ncontent\n")
	if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte("---\ntitle: [broken\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 2 || len(report.Findings) != 1 || report.Findings[0].Category != "parse_error" {
		t.Fatalf("unexpected parse report: %#v", report)
	}
}

func writeNote(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
