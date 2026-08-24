package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestScanReportsOrphanedDerivedArtifact(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "derived.md", "---\ntitle: Summary\nderived_from: missing_source.md\n---\nGenerated text\n")

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy {
		t.Fatal("expected report to be unhealthy for orphaned artifact")
	}
	var found bool
	for _, f := range report.Findings {
		if f.Category == "orphaned_derived_artifact" && f.Path == "derived.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected orphaned_derived_artifact finding, got %#v", report.Findings)
	}
}

func TestScanReportsStaleDerivedArtifact(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "source.md", "---\ntitle: Source Note\n---\nOriginal text\n")
	writeNote(t, root, "derived.md", "---\ntitle: Summary\nderived_from: source.md\n---\nGenerated text\n")

	// Make source.md newer than derived.md
	past := time.Now().Add(-1 * time.Hour)
	now := time.Now()
	_ = os.Chtimes(filepath.Join(root, "derived.md"), past, past)
	_ = os.Chtimes(filepath.Join(root, "source.md"), now, now)

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy {
		t.Fatal("expected report to be unhealthy for stale artifact")
	}
	var found bool
	for _, f := range report.Findings {
		if f.Category == "stale_derived_artifact" && f.Path == "derived.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected stale_derived_artifact finding, got %#v", report.Findings)
	}
}

func TestScanFreshDerivedArtifactHasNoFindings(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "source.md", "---\ntitle: Source Note\n---\nOriginal text\n")
	writeNote(t, root, "derived.md", "---\ntitle: Summary\nderived_from: source.md\n---\nGenerated text\n")

	// Make derived.md newer than source.md
	past := time.Now().Add(-1 * time.Hour)
	now := time.Now()
	_ = os.Chtimes(filepath.Join(root, "source.md"), past, past)
	_ = os.Chtimes(filepath.Join(root, "derived.md"), now, now)

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range report.Findings {
		if f.Category == "stale_derived_artifact" || f.Category == "orphaned_derived_artifact" {
			t.Fatalf("unexpected derived artifact finding on fresh artifact: %#v", f)
		}
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
