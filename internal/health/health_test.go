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
	if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte("---\ntitle: [broken\n---\n"), 0o600); err != nil {
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

func TestScanResolvesAliasLinks(t *testing.T) {
	root := t.TempDir()
	// Note with aliases as list
	writeNote(t, root, "agency.md", "---\ntitle: Bundesagentur für Arbeit\naliases:\n  - BA\n  - Arbeitsamt\n---\ncontent\n")
	// Note with alias as single string
	writeNote(t, root, "single.md", "---\ntitle: Single Alias Note\naliases: SingleAlias\n---\ncontent\n")
	// Source linking to aliases and real target and a missing link
	writeNote(t, root, "source.md", "---\ntitle: Source\n---\n[[BA.md]]\n[[Arbeitsamt.md]]\n[[SingleAlias.md]]\n[[missing_target.md]]\n")

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 3 {
		t.Fatalf("expected 3 files scanned, got %d", report.FilesScanned)
	}
	// Only missing_target.md should be flagged as broken_wikilink; BA.md, Arbeitsamt.md, SingleAlias.md resolve to aliases
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding (missing_target.md), got %d: %#v", len(report.Findings), report.Findings)
	}
	if report.Findings[0].Category != "broken_wikilink" {
		t.Errorf("expected broken_wikilink, got %s", report.Findings[0].Category)
	}
}

func writeNote(t *testing.T, root, name, content string) {
	t.Helper()
	dst := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
