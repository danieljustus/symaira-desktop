package ingest

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"
)

// The tests in this file exercise ScanPDFForBarcodes and ingestPDFWithSplit
// with fake pdftoppm / symingest binaries on PATH (same pattern as
// mockSymingestBinary in jobs_test.go).  The fake pdftoppm "renders" pages
// by copying pre-generated PNG files (real Code39 barcodes produced with the
// gozxing writer, so the detection pipeline is exercised end to end).

// mockPdftoppmScript renders MOCK_PAGES pages, overlaying barcode / other /
// corrupt images on the pages listed in MOCK_BARCODE_PAGES /
// MOCK_OTHER_PAGES / MOCK_CORRUPT_PAGES.  When MOCK_STATE_DIR is set, only
// the first invocation renders barcodes; later invocations render blank
// pages (this bounds the IngestFile -> ingestPDFWithSplit recursion for
// split parts).  Set MOCK_PDFTOPPM_FAIL=1 to simulate a reader failure.
const mockPdftoppmScript = `if [ "$1" != "-png" ]; then
	exit 1
fi
if [ "$MOCK_PDFTOPPM_FAIL" = "1" ]; then
	echo "mock pdftoppm failure" >&2
	exit 1
fi
prefix="$5"
pages="$MOCK_PAGES"
barcode_pages="$MOCK_BARCODE_PAGES"
if [ -n "$MOCK_STATE_DIR" ] && [ -f "$MOCK_STATE_DIR/first-scan-done" ]; then
	pages="$MOCK_PAGES_AFTER"
	barcode_pages=""
else
	if [ -n "$MOCK_STATE_DIR" ]; then
		: > "$MOCK_STATE_DIR/first-scan-done"
	fi
fi
i=1
while [ "$i" -le "$pages" ]; do
	num=$(printf "%02d" "$i")
	cp "$MOCK_IMG_DIR/blank.png" "$prefix-$num.png" || exit 1
	i=$((i+1))
done
for p in $barcode_pages; do
	num=$(printf "%02d" "$p")
	cp "$MOCK_IMG_DIR/barcode.png" "$prefix-$num.png" || exit 1
done
for p in $MOCK_OTHER_PAGES; do
	num=$(printf "%02d" "$p")
	cp "$MOCK_IMG_DIR/other.png" "$prefix-$num.png" || exit 1
done
for p in $MOCK_CORRUPT_PAGES; do
	num=$(printf "%02d" "$p")
	cp "$MOCK_IMG_DIR/corrupt.png" "$prefix-$num.png" || exit 1
done
exit 0
`

// mockSymingestScript answers version, splits any PDF into two parts via
// split-pdf, and reports a note path per ingested part.  Set
// MOCK_INGEST_FAIL=1 to make every ingest fail.
const mockSymingestScript = `if [ "$1" = "version" ]; then
	echo '{"schema_version": 1, "version": "0.7.0"}'
	exit 0
fi
if [ "$1" = "split-pdf" ]; then
	outdir=""
	prev=""
	for a in "$@"; do
		if [ "$prev" = "--output-dir" ]; then
			outdir="$a"
		fi
		prev="$a"
	done
	if [ -z "$outdir" ]; then
		exit 1
	fi
	echo "part-1" > "$outdir/part-1.pdf"
	echo "part-2" > "$outdir/part-2.pdf"
	exit 0
fi
if [ "$1" = "ingest" ]; then
	if [ "$MOCK_INGEST_FAIL" = "1" ]; then
		exit 1
	fi
	path=""
	prev=""
	for a in "$@"; do
		if [ "$prev" = "--json" ]; then
			path="$a"
		fi
		prev="$a"
	done
	base="${path##*/}"
	echo "{\"path\": \"note-${base%.pdf}.md\"}"
	exit 0
fi
exit 1
`

// writeMockScript writes an executable shell script into dir.
func writeMockScript(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	// #nosec G306 -- mock scripts must remain executable for exec.Command.
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
}

// writeBarcodePNG renders a real Code39 barcode (with quiet zone) as a PNG.
func writeBarcodePNG(t *testing.T, path, text string) {
	t.Helper()
	bm, err := oned.NewCode39Writer().Encode(text, gozxing.BarcodeFormat_CODE_39, 300, 150, nil)
	if err != nil {
		t.Fatalf("encode barcode %q: %v", text, err)
	}
	const margin = 40
	img := image.NewRGBA(image.Rect(0, 0, bm.GetWidth()+2*margin, bm.GetHeight()+2*margin))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(margin, margin, margin+bm.GetWidth(), margin+bm.GetHeight()), bm, image.Point{}, draw.Src)
	// #nosec G304 -- fixture path under t.TempDir()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// writeBlankPNG writes a plain white PNG.
func writeBlankPNG(t *testing.T, path string) {
	t.Helper()
	// #nosec G304 -- fixture path under t.TempDir()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		t.Fatal(err)
	}
}

// setupBarcodeFixture creates the shared image set and a mock dir with
// pdftoppm (and optionally symingest) fakes.
func setupBarcodeFixture(t *testing.T, withSymingest bool) (imgDir, mockDir string) {
	t.Helper()
	imgDir = t.TempDir()
	writeBarcodePNG(t, filepath.Join(imgDir, "barcode.png"), "PATCH-T")
	writeBarcodePNG(t, filepath.Join(imgDir, "other.png"), "HELLO")
	writeBlankPNG(t, filepath.Join(imgDir, "blank.png"))
	if err := os.WriteFile(filepath.Join(imgDir, "corrupt.png"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}

	mockDir = t.TempDir()
	writeMockScript(t, mockDir, "pdftoppm", mockPdftoppmScript)
	if withSymingest {
		writeMockScript(t, mockDir, "symingest", mockSymingestScript)
	}
	return imgDir, mockDir
}

func TestScanPDFForBarcodes(t *testing.T) {
	imgDir, mockDir := setupBarcodeFixture(t, false)
	pdfPath := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF(), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		pages        string
		barcodePages string
		otherPages   string
		corruptPages string
		fail         bool
		pathEnv      string
		want         []int
		wantErr      string
	}{
		{"no barcode found", "2", "", "", "", false, mockDir + ":/bin:/usr/bin", nil, ""},
		{"multiple barcodes", "4", "1 3", "2", "4", false, mockDir + ":/bin:/usr/bin", []int{1, 3}, ""},
		{"reader failure", "1", "", "", "", true, mockDir + ":/bin:/usr/bin", nil, "pdftoppm failed"},
		{"missing tool", "1", "", "", "", false, "/usr/bin:/bin", nil, "pdftoppm not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", tt.pathEnv)
			t.Setenv("MOCK_IMG_DIR", imgDir)
			t.Setenv("MOCK_PAGES", tt.pages)
			t.Setenv("MOCK_BARCODE_PAGES", tt.barcodePages)
			t.Setenv("MOCK_OTHER_PAGES", tt.otherPages)
			t.Setenv("MOCK_CORRUPT_PAGES", tt.corruptPages)
			t.Setenv("MOCK_PDFTOPPM_FAIL", map[bool]string{true: "1"}[tt.fail])

			got, err := ScanPDFForBarcodes(pdfPath, DefaultBarcodeConfig())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ScanPDFForBarcodes = %v, want %v", got, tt.want)
			}
		})
	}
}

// envForSplitTools points PATH at the mock dir plus the system dirs that
// contain only coreutils (no qpdf / pdftoppm / symingest).
const envForSplitTools = "%s:/bin:/usr/bin"

// setMockSplitEnv installs the fake pdftoppm (and symingest) tools and the
// barcode image fixture.  When firstScanHasBarcode is true, the first page
// scan reports a separator on page 2 while later scans (of split parts)
// report none.
func setMockSplitEnv(t *testing.T, withSymingest, firstScanHasBarcode bool) (vaultRoot, pdfPath string) {
	t.Helper()
	imgDir, mockDir := setupBarcodeFixture(t, withSymingest)
	stateDir := t.TempDir()

	t.Setenv("PATH", strings.ReplaceAll(envForSplitTools, "%s", mockDir))
	t.Setenv("MOCK_IMG_DIR", imgDir)
	t.Setenv("MOCK_STATE_DIR", stateDir)
	t.Setenv("MOCK_PAGES", "2")
	t.Setenv("MOCK_PAGES_AFTER", "1")
	if firstScanHasBarcode {
		t.Setenv("MOCK_BARCODE_PAGES", "2")
	} else {
		t.Setenv("MOCK_BARCODE_PAGES", "")
	}
	t.Setenv("MOCK_OTHER_PAGES", "")
	t.Setenv("MOCK_CORRUPT_PAGES", "")

	vaultRoot = t.TempDir()
	pdfPath = filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	return vaultRoot, pdfPath
}

func TestIngestPDFWithSplit_SplitDecision(t *testing.T) {
	vaultRoot, pdfPath := setMockSplitEnv(t, true, true)

	notes, err := ingestPDFWithSplit(vaultRoot, pdfPath, DefaultBarcodeConfig())
	if err != nil {
		t.Fatalf("ingestPDFWithSplit: %v", err)
	}
	want := []string{"note-part-1.md", "note-part-2.md"}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes = %v, want %v", notes, want)
	}
}

func TestIngestPDFWithSplit_NoSeparators(t *testing.T) {
	vaultRoot, pdfPath := setMockSplitEnv(t, false, false)

	notes, err := ingestPDFWithSplit(vaultRoot, pdfPath, DefaultBarcodeConfig())
	if err != nil {
		t.Fatalf("ingestPDFWithSplit: %v", err)
	}
	if notes != nil {
		t.Errorf("expected nil notes when nothing to split, got %v", notes)
	}
}

func TestIngestPDFWithSplit_ScanError(t *testing.T) {
	vaultRoot := t.TempDir()
	pdfPath := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	// No pdftoppm / qpdf / symingest on PATH: scanning fails up front.
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())

	notes, err := ingestPDFWithSplit(vaultRoot, pdfPath, DefaultBarcodeConfig())
	if err == nil || !strings.Contains(err.Error(), "pdftoppm not found") {
		t.Fatalf("expected barcode split scan error, got %v", err)
	}
	if notes != nil {
		t.Errorf("expected nil notes on error, got %v", notes)
	}
}

func TestIngestPDFWithSplit_AllPartsFailed(t *testing.T) {
	vaultRoot, pdfPath := setMockSplitEnv(t, true, true)
	t.Setenv("MOCK_INGEST_FAIL", "1")

	notes, err := ingestPDFWithSplit(vaultRoot, pdfPath, DefaultBarcodeConfig())
	if err == nil || !strings.Contains(err.Error(), "all parts failed to ingest") {
		t.Fatalf("expected all-parts-failed error, got %v", err)
	}
	if notes != nil {
		t.Errorf("expected nil notes when all parts fail, got %v", notes)
	}
}

// TestIngestFileWithBarcodeSplit_SplitToolsNoSeparators verifies the
// fallback to non-split ingest when split tools are present but no
// separator barcodes are found.
func TestIngestFileWithBarcodeSplit_SplitToolsNoSeparators(t *testing.T) {
	vaultRoot, pdfPath := setMockSplitEnv(t, true, false)

	notes, err := IngestFileWithBarcodeSplit(vaultRoot, pdfPath, DefaultBarcodeConfig())
	if err != nil {
		t.Fatalf("IngestFileWithBarcodeSplit: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 note (fallback), got %d: %v", len(notes), notes)
	}
	if !strings.HasPrefix(notes[0], "note-") {
		t.Errorf("expected note from symingest ingest, got %q", notes[0])
	}
}

// TestIngestFile_SplitToolsFirstNote verifies that IngestFile on a PDF with
// separator barcodes returns the first split note (backward-compat path).
func TestIngestFile_SplitToolsFirstNote(t *testing.T) {
	vaultRoot, pdfPath := setMockSplitEnv(t, true, true)

	relNote, err := IngestFile(vaultRoot, pdfPath)
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if relNote != "note-part-1.md" {
		t.Errorf("expected first split note, got %q", relNote)
	}
}
