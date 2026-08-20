package ocr

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/extract"
)

// writeFakeBin creates an executable script that prints out and exits 0.
func writeFakeBin(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		path += ".bat"
		script = "@echo off\n" + script
	} else {
		script = "#!/bin/sh\n" + script
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeTesseractScriptEx(output string, exitCode int, stderr string) string {
	if runtime.GOOS == "windows" {
		errRedirect := ""
		if stderr != "" {
			errRedirect = fmt.Sprintf("echo %s >&2", stderr)
		}
		return fmt.Sprintf(`if "%%1"=="--list-langs" (
echo List of available languages (1):
echo eng
exit /b 0
)
%s
echo %s
exit /b %d`, errRedirect, output, exitCode)
	}
	errRedirect := ""
	if stderr != "" {
		errRedirect = fmt.Sprintf("echo %q >&2", stderr)
	}
	return fmt.Sprintf(`if [ "$1" = "--list-langs" ]; then
echo "List of available languages (1):"
echo "eng"
exit 0
fi
%s
echo "%s"
exit %d`, errRedirect, output, exitCode)
}

func fakeTesseractScript(output string) string {
	return fakeTesseractScriptEx(output, 0, "")
}

func TestRunner_Available_MissingTool(t *testing.T) {
	r := &Runner{Tesseract: filepath.Join(t.TempDir(), "does-not-exist")}
	if err := r.Available(); err == nil {
		t.Fatal("expected error for missing tesseract")
	}
}

func TestRunner_ExtractImage(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("extracted text"))

	r := &Runner{Tesseract: tess, OCRLang: "eng"}
	img := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := r.Extract(context.Background(), img, extract.KindPNG)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Text != "extracted text" {
		t.Fatalf("text = %q, want %q", res.Text, "extracted text")
	}
	if res.Engine != "tesseract" {
		t.Fatalf("engine = %q, want tesseract", res.Engine)
	}
}

// TestRunner_ExtractImage_DifferentWorkingDirectory reproduces the Leptonica
// failure mode observed when tesseract is invoked with an absolute image path
// from a process working directory unrelated to the image. The fix runs
// tesseract with its cwd set to the image's directory and a relative file
// name; this fake tesseract rejects absolute paths to simulate that failure.
func TestRunner_ExtractImage_DifferentWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract", `
if [ "$1" = "--list-langs" ]; then
	echo "List of available languages (1):"
	echo "eng"
	exit 0
fi
case "$3" in
	/*) echo "Leptonica Error in findFileFormat: image file not found" >&2; exit 1;;
esac
echo "extracted from $3"
`)

	imgDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(imgDir, "scan.png")
	if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Run from a working directory unrelated to the image.
	otherDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatal(err)
		}
	})

	r := &Runner{Tesseract: tess, OCRLang: "eng"}
	res, err := r.Extract(context.Background(), img, extract.KindPNG)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(res.Text, "scan.png") {
		t.Fatalf("text = %q, want it to contain the relative image name", res.Text)
	}
}

func TestRunner_ExtractPDF(t *testing.T) {
	dir := t.TempDir()
	pdfppm := writeFakeBin(t, dir, "pdftoppm", `echo "page-1.png" > "$5-page-1.png"`)
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("page text"))

	r := &Runner{Tesseract: tess, PDFToPPM: pdfppm, OCRLang: "eng"}
	pdf := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := r.Extract(context.Background(), pdf, extract.KindPDF)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Text != "page text" {
		t.Fatalf("text = %q, want %q", res.Text, "page text")
	}
	if res.Engine != "pdftoppm+tesseract" {
		t.Fatalf("engine = %q", res.Engine)
	}
}

func TestRunner_ExtractPDF_MissingPDFToPPM(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("ok"))
	r := &Runner{Tesseract: tess, PDFToPPM: filepath.Join(dir, "missing"), OCRLang: "eng"}
	pdf := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Extract(context.Background(), pdf, extract.KindPDF); err == nil {
		t.Fatal("expected error for missing pdftoppm")
	}
}

func TestDefaultRunner_CleanedPaths(t *testing.T) {
	r := DefaultRunner("eng")
	if r.Tesseract != "tesseract" {
		t.Fatalf("Tesseract = %q, want %q", r.Tesseract, "tesseract")
	}
	if r.PDFToPPM != "pdftoppm" {
		t.Fatalf("PDFToPPM = %q, want %q", r.PDFToPPM, "pdftoppm")
	}
}

func TestRunner_Available_EmptyPath(t *testing.T) {
	r := &Runner{Tesseract: ""}
	if err := r.Available(); err == nil {
		t.Fatal("expected error for empty tesseract path")
	}
}

func TestRunner_AvailableForPDF_EmptyPDFToPPM(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("ok"))
	r := &Runner{Tesseract: tess, PDFToPPM: ""}
	if err := r.AvailableForPDF(); err == nil {
		t.Fatal("expected error for empty pdftoppm path")
	}
}

func TestRunner_ExtractPDF_MaintainsPageOrder(t *testing.T) {
	dir := t.TempDir()
	pdfppm := writeFakeBin(t, dir, "pdftoppm",
		`echo "page-1.png" > "$5-page-1.png" && echo "page-2.png" > "$5-page-2.png"`)
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScript("page text from $(basename \"$3\")"))
	r := &Runner{Tesseract: tess, PDFToPPM: pdfppm, OCRLang: "eng"}
	pdf := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Extract(context.Background(), pdf, extract.KindPDF)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(res.Text, "page-page-1") {
		t.Fatalf("text missing first page content:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "page-page-2") {
		t.Fatalf("text missing second page content:\n%s", res.Text)
	}
	idx1 := strings.Index(res.Text, "page-1")
	idx2 := strings.Index(res.Text, "page-2")
	if idx1 > idx2 {
		t.Fatal("pages out of order: page-2 appears before page-1")
	}
}

func TestRunner_CapturesStderr(t *testing.T) {
	dir := t.TempDir()
	tess := writeFakeBin(t, dir, "tesseract", fakeTesseractScriptEx("broken", 1, "some stderr output"))
	r := &Runner{Tesseract: tess, OCRLang: "eng"}
	img := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Extract(context.Background(), img, extract.KindPNG); err == nil {
		t.Fatal("expected error when tesseract writes to stderr")
	}
}

func TestRunner_ValidateLanguages(t *testing.T) {
	dir := t.TempDir()
	// Fake tesseract that supports --list-langs returning eng and deu
	tess := writeFakeBin(t, dir, "tesseract", `
if [ "$1" = "--list-langs" ]; then
	echo "List of available languages (2):"
	echo "eng"
	echo "deu"
	exit 0
fi
`)

	// Case 1: single available language
	r1 := &Runner{Tesseract: tess, OCRLang: "eng"}
	l1, err := r1.validateLanguages(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l1 != "eng" {
		t.Fatalf("want eng, got %q", l1)
	}

	// Case 2: multiple available languages
	r2 := &Runner{Tesseract: tess, OCRLang: "deu+eng"}
	l2, err := r2.validateLanguages(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l2 != "deu+eng" {
		t.Fatalf("want deu+eng, got %q", l2)
	}

	// Case 3: subset of languages missing (warning printed, fallback used)
	r3 := &Runner{Tesseract: tess, OCRLang: "deu+fra+eng"}
	l3, err := r3.validateLanguages(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l3 != "deu+eng" {
		t.Fatalf("want deu+eng, got %q", l3)
	}

	// Case 4: none of the languages are available (fails)
	r4 := &Runner{Tesseract: tess, OCRLang: "fra+ita"}
	_, err = r4.validateLanguages(context.Background())
	if err == nil {
		t.Fatal("expected error for unavailable languages")
	}
	if !strings.Contains(err.Error(), "none of the configured OCR languages") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRunner_ValidateLanguages_CachesListLanguages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell counter fixture is Unix-only")
	}
	dir := t.TempDir()
	counter := filepath.Join(dir, "list-langs-count")
	tess := writeFakeBin(t, dir, "tesseract", fmt.Sprintf(`
count_file=%q
if [ "$1" = "--list-langs" ]; then
	n=0
	if [ -f "$count_file" ]; then n=$(cat "$count_file"); fi
	n=$((n + 1))
	printf "%%s\n" "$n" > "$count_file"
	echo "List of available languages (1):"
	echo "eng"
	exit 0
fi
echo "extracted text"
`, counter))

	r := &Runner{Tesseract: tess, OCRLang: "eng"}
	for i := 0; i < 2; i++ {
		img := filepath.Join(dir, fmt.Sprintf("scan-%d.png", i))
		if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Extract(context.Background(), img, extract.KindPNG); err != nil {
			t.Fatalf("Extract %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "1" {
		t.Fatalf("--list-langs calls = %s, want 1", got)
	}
}

func TestRunner_Integration(t *testing.T) {
	if os.Getenv("SYM_TEST_REAL_OCR") != "true" {
		t.Skip("skipping real OCR integration tests (set SYM_TEST_REAL_OCR=true to run)")
	}

	r := DefaultRunner("eng")
	if err := r.Available(); err != nil {
		t.Fatalf("Tesseract not available for integration test: %v", err)
	}

	dir := t.TempDir()

	// 1. PNG integration test
	pngBytes, err := base64.StdEncoding.DecodeString(FixturePNGBase64)
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	pngPath := filepath.Join(dir, "test.png")
	if err := os.WriteFile(pngPath, pngBytes, 0o644); err != nil {
		t.Fatalf("write png file: %v", err)
	}

	resPNG, err := r.Extract(context.Background(), pngPath, extract.KindPNG)
	if err != nil {
		t.Fatalf("PNG Extract failed: %v", err)
	}
	if !strings.Contains(resPNG.Text, "OCR Test Page") {
		t.Fatalf("expected 'OCR Test Page' in PNG output, got: %q", resPNG.Text)
	}

	// 2. PDF integration test
	if err := r.AvailableForPDF(); err != nil {
		t.Fatalf("pdftoppm not available for PDF integration test: %v", err)
	}
	pdfBytes, err := base64.StdEncoding.DecodeString(FixturePDFBase64)
	if err != nil {
		t.Fatalf("decode pdf fixture: %v", err)
	}
	pdfPath := filepath.Join(dir, "test.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o644); err != nil {
		t.Fatalf("write pdf file: %v", err)
	}

	resPDF, err := r.Extract(context.Background(), pdfPath, extract.KindPDF)
	if err != nil {
		t.Fatalf("PDF Extract failed: %v", err)
	}
	if !strings.Contains(resPDF.Text, "OCR Test Page") {
		t.Fatalf("expected 'OCR Test Page' in PDF output, got: %q", resPDF.Text)
	}
}
