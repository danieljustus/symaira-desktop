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

func TestScanResolvesAttachmentEmbeds(t *testing.T) {
	root := t.TempDir()
	// Create note with embed forms and standard markdown link
	noteContent := "---\ntitle: Note with Attachments\n---\n![[scan.png]]\n![[assets/scan.png]]\n![[Scan.PNG]]\n![Image](assets/scan.png)\n"
	writeNote(t, root, "note.md", noteContent)

	// Create attachment in assets/
	assetsDir := filepath.Join(root, "assets")
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "scan.png"), []byte("pngdata"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 1 {
		t.Fatalf("expected 1 markdown file scanned, got %d", report.FilesScanned)
	}
	if !report.Healthy || len(report.Findings) != 0 {
		t.Fatalf("expected 0 findings for valid attachment embeds, got %#v", report.Findings)
	}
}

func TestScanMissingAttachmentEmbedReportsDistinguishedMessage(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "note.md", "---\ntitle: Missing\n---\n![[missing.png]]\n")

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding for missing attachment, got %#v", report.Findings)
	}
	f := report.Findings[0]
	if f.Category != "broken_wikilink" {
		t.Errorf("expected category 'broken_wikilink', got %q", f.Category)
	}
	wantMsg := `attachment target "missing.png" does not resolve to a vault attachment`
	if f.Message != wantMsg {
		t.Errorf("expected message %q, got %q", wantMsg, f.Message)
	}
}

func TestScanIgnoredDirectoryAttachmentsNotResolvable(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "note.md", "---\ntitle: Note\n---\n![[vendor_asset.png]]\n")

	// Create attachment inside ignored node_modules directory
	nmDir := filepath.Join(root, "node_modules", "pkg")
	if err := os.MkdirAll(nmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "vendor_asset.png"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding because node_modules is ignored, got %#v", report.Findings)
	}
	if report.Findings[0].Category != "broken_wikilink" {
		t.Errorf("expected broken_wikilink finding, got %v", report.Findings[0])
	}
}

func TestScanResolvesCanvasWikilinks(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "note.md", "---\ntitle: Note\n---\n[[Board.canvas]]\n[[boards/Board.canvas]]\n[[missing.canvas]]\n")

	boardsDir := filepath.Join(root, "boards")
	if err := os.MkdirAll(boardsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boardsDir, "Board.canvas"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(root, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding for missing.canvas, got %d: %#v", len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Category != "broken_wikilink" {
		t.Errorf("expected category 'broken_wikilink', got %q", f.Category)
	}
	wantMsg := `wikilink target "missing.canvas" does not resolve to a vault document`
	if f.Message != wantMsg {
		t.Errorf("expected message %q, got %q", wantMsg, f.Message)
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
