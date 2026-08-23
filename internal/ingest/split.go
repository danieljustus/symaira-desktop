package ingest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
		data, err := os.ReadFile(pdfPath)
		if err != nil {
			return nil, fmt.Errorf("read input: %w", err)
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return nil, fmt.Errorf("write part: %w", err)
		}
		return []string{dst}, nil
	}

	// Prefer qpdf for splitting; otherwise go through the in-process ingest
	// pipeline, which drives the Poppler utilities and reports its own
	// missing-tool error — so this does not gate on HasPopplerSplit and
	// swallow a pipeline that is present but failing for another reason.
	if qpdfPath, err := exec.LookPath("qpdf"); err == nil {
		return splitWithQPDF(qpdfPath, pdfPath, splitPoints, outputDir)
	}
	return splitWithPipeline(pdfPath, splitPoints, outputDir)
}

// splitWithQPDF uses qpdf to extract page ranges.
func splitWithQPDF(qpdfPath, pdfPath string, splitPoints []int, outputDir string) ([]string, error) {
	var parts []string
	start := 1

	for i, end := range splitPoints {
		partPath := filepath.Join(outputDir, fmt.Sprintf("part-%d.pdf", i+1))
		rangeSpec := fmt.Sprintf("%d-%d", start, end)
		if err := qpdfExtract(qpdfPath, pdfPath, partPath, rangeSpec); err != nil {
			return nil, fmt.Errorf("qpdf extract pages %s: %w", rangeSpec, err)
		}
		parts = append(parts, partPath)
		start = end + 1
	}

	// Remaining pages after the last split point.
	partPath := filepath.Join(outputDir, fmt.Sprintf("part-%d.pdf", len(splitPoints)+1))
	rangeSpec := fmt.Sprintf("%d-z", start) // z = end of document in qpdf
	if err := qpdfExtract(qpdfPath, pdfPath, partPath, rangeSpec); err != nil {
		return nil, fmt.Errorf("qpdf extract pages %s: %w", rangeSpec, err)
	}
	parts = append(parts, partPath)

	return parts, nil
}

func qpdfExtract(qpdfPath, inputPath, outputPath, pageRange string) error {
	//nolint:gosec // G204: qpdfPath comes from exec.LookPath, and the remaining
	// arguments are a fixed argv of file paths and a page range, never shell-
	// interpreted.
	cmd := exec.Command(qpdfPath,
		"--empty", "--pages", inputPath, pageRange,
		"--", outputPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", err, stderr.String())
	}
	return nil
}

// splitWithPipeline splits through the absorbed ingest pipeline, which runs
// the Poppler utilities in-process rather than as a sibling symingest binary.
func splitWithPipeline(pdfPath string, splitPoints []int, outputDir string) ([]string, error) {
	// The pipeline's selector lists the pages to split *after*, which is the
	// same meaning splitPoints carries here.
	strPoints := make([]string, len(splitPoints))
	for i, p := range splitPoints {
		strPoints[i] = strconv.Itoa(p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	parts, err := SplitPDFFunc(ctx, pdfPath, strings.Join(strPoints, ","), outputDir)
	if err != nil {
		return nil, fmt.Errorf("split pdf: %w", err)
	}
	return parts, nil
}

// HasPopplerSplit reports whether the Poppler utilities the pipeline's split
// needs are all available on PATH.
func HasPopplerSplit() bool {
	for _, tool := range []string{"pdfinfo", "pdfseparate", "pdfunite"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false
		}
	}
	return true
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
