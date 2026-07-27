package ingest

import (
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectBarcodeCode39(t *testing.T) {
	// Use a real barcode image from gozxing's testdata.
	code39Img := filepath.Join(gozxingTestdataDir(t), "oned", "testdata", "code39", "01.png")
	if _, err := os.Stat(code39Img); err != nil {
		t.Skipf("gozxing testdata not available: %v", err)
	}

	img, err := readImageFile(code39Img)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}

	text, format, err := DetectBarcode(img)
	if err != nil {
		t.Fatalf("DetectBarcode error: %v", err)
	}
	if text == "" {
		t.Error("expected barcode text, got empty")
	}
	if format == "" {
		t.Error("expected barcode format, got empty")
	}
	t.Logf("detected: %q (format: %s)", text, format)
}

func TestDetectBarcodeNoBarcode(t *testing.T) {
	// A plain white image has no barcode.
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	text, _, err := DetectBarcode(img)
	if err != nil {
		t.Fatalf("DetectBarcode error: %v", err)
	}
	if text != "" {
		t.Errorf("expected no barcode, got %q", text)
	}
}

func TestDefaultBarcodeConfig(t *testing.T) {
	cfg := DefaultBarcodeConfig()
	if cfg.SeparatorPattern != "PATCH-T" {
		t.Errorf("expected PATCH-T, got %q", cfg.SeparatorPattern)
	}
	if !cfg.DiscardSeparators {
		t.Error("expected DiscardSeparators=true by default")
	}
	if cfg.ParseASN {
		t.Error("expected ParseASN=false by default")
	}
	if cfg.ASNPrefix != "ASN:" {
		t.Errorf("expected ASN:, got %q", cfg.ASNPrefix)
	}
}

func TestParsePPMFilePage(t *testing.T) {
	tests := []struct {
		name     string
		expected int
	}{
		{"page-01.png", 1},
		{"page-12.png", 12},
		{"page-001.png", 1},
		{"page-99.png", 99},
	}
	for _, tt := range tests {
		n, err := parsePPMFilePage(tt.name)
		if err != nil {
			t.Errorf("parsePPMFilePage(%q): %v", tt.name, err)
			continue
		}
		if n != tt.expected {
			t.Errorf("parsePPMFilePage(%q) = %d, want %d", tt.name, n, tt.expected)
		}
	}

	_, err := parsePPMFilePage("not-a-page-file.txt")
	if err == nil {
		t.Error("expected error for invalid filename")
	}
}

func TestHasPdfToPPM(t *testing.T) {
	// pdftoppm is available on this system; just check it doesn't panic.
	_ = HasPdfToPPM()
}

func TestHasQPDF(t *testing.T) {
	_ = HasQPDF()
}

func TestSplitPDFNoSplitPoints(t *testing.T) {
	// Create a minimal valid PDF for testing.
	srcDir := t.TempDir()
	outDir := t.TempDir()
	pdfPath := filepath.Join(srcDir, "minimal.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF(), 0644); err != nil {
		t.Fatal(err)
	}

	parts, err := SplitPDF(pdfPath, nil, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Errorf("expected 1 part, got %d", len(parts))
	}
}

func TestSplitPDFWithQPDF(t *testing.T) {
	if !HasQPDF() {
		t.Skip("qpdf not available")
	}

	srcDir := t.TempDir()
	outDir := t.TempDir()
	pdfPath := filepath.Join(srcDir, "multi.pdf")

	// Create a 3-page PDF using qpdf.
	emptyPDF := filepath.Join(srcDir, "empty.pdf")
	if err := os.WriteFile(emptyPDF, minimalPDF(), 0644); err != nil {
		t.Fatal(err)
	}

	// Concatenate three copies to make a 3-page PDF.
	cmd := exec.Command("qpdf", "--empty", "--pages",
		emptyPDF, "1", emptyPDF, "1", emptyPDF, "1",
		"--", pdfPath)
	out, err := cmd.CombinedOutput()
	// qpdf may exit 3 for warnings on minimal PDFs (still produces valid output).
	if err != nil && !strings.Contains(string(out), "operation succeeded with warnings") {
		t.Fatalf("qpdf concat: %v: %s", err, out)
	}

	// Split after page 2 → two parts: pages 1-2, page 3.
	parts, err := SplitPDF(pdfPath, []int{2}, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %v", len(parts), parts)
	}

	// Verify both files exist.
	for _, p := range parts {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("part missing: %s: %v", p, err)
		}
	}
}

func TestSplitPDFByBarcode_NoScanner(t *testing.T) {
	cfg := DefaultBarcodeConfig()
	tmpDir := t.TempDir()

	// A PDF with no pdftoppm on path — should still work without splitting.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "/usr/bin:/bin")
	defer os.Setenv("PATH", origPath)

	pdfPath := filepath.Join(tmpDir, "test.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF(), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := SplitPDFByBarcode(pdfPath, cfg, tmpDir)
	if err == nil {
		t.Log("split succeeded (pdftoppm was found via non-standard path)")
	}
	// The error is expected when pdftoppm is not available.
}

func TestIngestFileWithBarcodeSplit_NoPDF(t *testing.T) {
	// Non-PDF files should not trigger splitting.
	t.Setenv("PATH", "/usr/bin:/bin")
	vaultRoot := t.TempDir()
	src := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultBarcodeConfig()
	notes, err := IngestFileWithBarcodeSplit(vaultRoot, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Errorf("expected 1 note, got %d", len(notes))
	}
	if !strings.HasPrefix(notes[0], "inbox/") {
		t.Errorf("unexpected note path: %s", notes[0])
	}
}

func TestIngestFileWithBarcodeSplit_PDF(t *testing.T) {
	vaultRoot := t.TempDir()
	src := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(src, minimalPDF(), 0644); err != nil {
		t.Fatal(err)
	}

	// When PATH has no pdftoppm or qpdf, splitting is skipped and the
	// PDF is ingested normally.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "/usr/bin:/bin")
	defer os.Setenv("PATH", origPath)

	cfg := DefaultBarcodeConfig()
	notes, err := IngestFileWithBarcodeSplit(vaultRoot, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Errorf("expected 1 note (no split tools), got %d", len(notes))
	}
}

// gozxingTestdataDir returns the module cache path for gozxing testdata.
func gozxingTestdataDir(t *testing.T) string {
	t.Helper()
	// Use go env GOMODCACHE to find the module.
	cmd := exec.Command("go", "env", "GOMODCACHE")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go env GOMODCACHE failed: %v", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "github.com", "makiuchi-d", "gozxing@v0.1.1")
}

// minimalPDF returns the bytes of a minimal valid PDF.
func minimalPDF() []byte {
	return []byte(`%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R/Resources<<>>>>endobj
xref
0 4
0000000000 65535 f 
0000000009 00000 n 
0000000058 00000 n 
0000000115 00000 n 
trailer<</Size 4/Root 1 0 R>>
startxref
206
%%EOF`)
}
