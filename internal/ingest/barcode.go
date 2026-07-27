// Package ingest implements document ingestion, OCR, and barcode-based splitting.
package ingest

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/datamatrix"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// BarcodeConfig controls barcode-based splitting behaviour.
type BarcodeConfig struct {
	// SeparatorPattern is a prefix that identifies a separator barcode.
	// When a barcode's text starts with this pattern the page is treated as
	// a document boundary.  Default: "PATCH-T" (case-insensitive match).
	SeparatorPattern string

	// DiscardSeparators discards separator pages from resulting documents.
	// When false, separators are retained at the start of each section.
	DiscardSeparators bool

	// ParseASN enables ASN (archive serial number) extraction from barcodes.
	// When enabled, barcode text matching the ASN pattern (a positive integer
	// after a configured prefix) is assigned to the corresponding document.
	ParseASN bool

	// ASNPrefix is the prefix before an ASN value in a barcode.
	// Default: "ASN:" (case-insensitive).
	ASNPrefix string
}

// DefaultBarcodeConfig returns sane defaults for barcode splitting.
func DefaultBarcodeConfig() BarcodeConfig {
	return BarcodeConfig{
		SeparatorPattern:  "PATCH-T",
		DiscardSeparators: true,
		ParseASN:          false,
		ASNPrefix:         "ASN:",
	}
}

// allReaders returns every reader that gozxing supports, tried in order.
func allReaders() []gozxing.Reader {
	return []gozxing.Reader{
		oned.NewCode39Reader(),
		oned.NewCode128Reader(),
		oned.NewEAN13Reader(),
		oned.NewEAN8Reader(),
		oned.NewUPCAReader(),
		oned.NewUPCEReader(),
		oned.NewITFReader(),
		qrcode.NewQRCodeReader(),
		datamatrix.NewDataMatrixReader(),
	}
}

// DetectBarcode scans an image for a barcode and returns its text content
// and the detected format.  Returns ("", "") when no barcode is found.
func DetectBarcode(img image.Image) (text string, format string, err error) {
	bin, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", "", fmt.Errorf("binarize: %w", err)
	}

	for _, reader := range allReaders() {
		result, err := reader.Decode(bin, nil)
		if err != nil {
			continue
		}
		return result.GetText(), result.GetBarcodeFormat().String(), nil
	}
	return "", "", nil
}

// ScanPDFForBarcodes converts each page of a PDF to an image and returns the
// 1-based page numbers where a separator barcode (matching cfg.SeparatorPattern)
// was detected.  Requires pdftoppm (poppler-utils) on PATH.
func ScanPDFForBarcodes(pdfPath string, cfg BarcodeConfig) ([]int, error) {
	ppmPath, err := exec.LookPath("pdftoppm")
	if err != nil {
		return nil, fmt.Errorf("pdftoppm not found: barcode scanning requires poppler-utils")
	}

	tmpDir, err := os.MkdirTemp("", "symdesk-barcode-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Render every page as a PNG image (300 DPI for reliable barcode detection).
	cmd := exec.Command(ppmPath,
		"-png", "-r", "300",
		pdfPath,
		filepath.Join(tmpDir, "page"),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w: %s", err, stderr.String())
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read temp dir: %w", err)
	}

	pattern := strings.ToUpper(cfg.SeparatorPattern)
	var separatorPages []int

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".png") {
			continue
		}

		// Parse page number from pdftoppm output ("page-01.png", "page-02.png", ...)
		pageNum, err := parsePPMFilePage(entry.Name())
		if err != nil {
			continue
		}

		img, err := readImageFile(filepath.Join(tmpDir, entry.Name()))
		if err != nil {
			continue // unreadable page — skip but don't abort batch
		}

		text, _, err := DetectBarcode(img)
		if err != nil {
			continue
		}

		if text != "" && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(text)), pattern) {
			separatorPages = append(separatorPages, pageNum)
		}
	}

	return separatorPages, nil
}

// parsePPMFilePage extracts the page number from a pdftoppm output filename.
// Filenames are like "page-01.png", "page-12.png", etc.
func parsePPMFilePage(name string) (int, error) {
	base := strings.TrimSuffix(name, ".png")
	parts := strings.Split(base, "-")
	if len(parts) < 2 {
		return 0, fmt.Errorf("unexpected filename: %s", name)
	}
	return strconv.Atoi(parts[len(parts)-1])
}

func readImageFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// HasPdfToPPM reports whether pdftoppm is available on PATH.
func HasPdfToPPM() bool {
	_, err := exec.LookPath("pdftoppm")
	return err == nil
}
