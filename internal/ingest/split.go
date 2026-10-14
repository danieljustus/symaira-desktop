package ingest

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/pdfops"
)

// SplitPDF splits a PDF into multiple files at the given 1-based page
// boundaries (splitPoints).  Each element of splitPoints is the last page
// of a section; the first section starts at page 1.
//
// Example: splitPoints = [3, 7] on a 10-page PDF yields three files:
//
//	part-1.pdf (pages 1–3), part-2.pdf (pages 4–7), part-3.pdf (pages 8–10).
//
// Returns the paths of the generated part files in order.
func SplitPDF(pdfPath string, splitPoints []int, outputDir string) ([]string, error) {
	if len(splitPoints) == 0 {
		// No splits — copy the original as a single part.
		dst := filepath.Join(outputDir, "part-1.pdf")
		if err := pdfops.Copy(pdfPath, dst); err != nil {
			return nil, fmt.Errorf("write part: %w", err)
		}
		return []string{dst}, nil
	}

	// qpdf is mandatory for validated outputs; Poppler alone cannot split.
	return splitWithQPDF("qpdf", pdfPath, splitPoints, outputDir)
}

// splitWithQPDF bounds page counting, all ranges, checks and publication together.
func splitWithQPDF(qpdfPath, pdfPath string, splitPoints []int, outputDir string) ([]string, error) {
	return (pdfops.Tools{QPDF: qpdfPath}).SplitLegacy(context.Background(), pdfPath, splitPoints, outputDir)
}

// HasPopplerSplit reports whether the historical Poppler utilities are present.
// It does not imply availability of validated PDF splitting, which needs qpdf.
// Retained for compatibility with older callers; new operations use HasPDFSplit.
func HasPopplerSplit() bool {
	for _, tool := range []string{"pdfinfo", "pdfseparate", "pdfunite"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false
		}
	}
	return true
}

// HasPDFSplit reports availability of the validated PDF operation's tools.
func HasPDFSplit() bool {
	_, err := exec.LookPath("pdfinfo")
	return err == nil && HasQPDF()
}

// BarcodeSplitResult holds the result of splitting a PDF by barcode separators.
type BarcodeSplitResult struct {
	// Parts are the paths to the generated part PDFs, in order.
	Parts []string

	// ASNs maps part index (0-based) to the extracted ASN value, if any.
	ASNs map[int]int

	// SeparatorPages lists the 1-based page numbers where separators were found.
	SeparatorPages []int

	// Errors lists per-page errors (keyed by 1-based page number).
	Errors map[int]string
}

// SplitPDFByBarcode scans a multi-page PDF for separator barcodes and splits
// it into one PDF per document section.  Separator pages are discarded or
// retained based on cfg.DiscardSeparators.
//
// When cfg.ParseASN is true, ASN values are extracted from barcodes matching
// the configured ASNPrefix pattern.
func SplitPDFByBarcode(pdfPath string, cfg BarcodeConfig, outputDir string) (*BarcodeSplitResult, error) {
	result := &BarcodeSplitResult{
		ASNs:   make(map[int]int),
		Errors: make(map[int]string),
	}

	sepPages, err := ScanPDFForBarcodes(pdfPath, cfg)
	if err != nil {
		return nil, fmt.Errorf("scan barcodes: %w", err)
	}
	result.SeparatorPages = sepPages

	// Build split points: for each separator page, the split happens
	// before the separator (when discarding) or after (when retaining).
	var splitPoints []int

	if cfg.DiscardSeparators {
		// Split before each separator page, discarding it.
		for _, p := range sepPages {
			if p > 1 {
				splitPoints = append(splitPoints, p-1)
			}
		}
	} else {
		// Split after each separator page, retaining it as the last page of the section.
		splitPoints = append([]int{}, sepPages...)
	}

	parts, err := SplitPDF(pdfPath, splitPoints, outputDir)
	if err != nil {
		return nil, fmt.Errorf("split pdf: %w", err)
	}
	result.Parts = parts

	return result, nil
}

// HasQPDF reports whether qpdf is available on PATH.
func HasQPDF() bool {
	_, err := exec.LookPath("qpdf")
	return err == nil
}
