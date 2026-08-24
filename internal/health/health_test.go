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

func TestScanResolvesAttachmentEmbeds(t *testing.T) {
	root := t.TempDir()
	// Create note with embed forms and standard markdown link
	noteContent := "---\ntitle: Note with Attachments\n---\n![[scan.png]]\n![[assets/scan.png]]\n![[Scan.PNG]]\n![Image](assets/scan.png)\n"
	writeNote(t, root, "note.md", noteContent)

	// Create attachment in assets/
	assetsDir := filepath.Join(root, "assets")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "scan.png"), []byte("pngdata"), 0644); err != nil {
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
	if err := os.MkdirAll(nmDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "vendor_asset.png"), []byte("data"), 0644); err != nil {
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
	if err := os.MkdirAll(boardsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boardsDir, "Board.canvas"), []byte("{}"), 0644); err != nil {
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
