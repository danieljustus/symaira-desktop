// Package archive generates searchable archival PDFs from scanned originals.
// It embeds OCR text as a selectable invisible text layer so documents remain
// readable in any PDF viewer without SymDesk.
//
// Ownership decision (recorded in VAULT.md): the archive generation step lives
// in symdesk, downstream of the OCR worker. When a worker completes OCR and
// the result contains text, symdesk produces the archival PDF. This keeps the
// worker focused on OCR alone and lets symdesk own the vault-side file layout.
package archive

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Generate produces a searchable PDF from a scanned original at inputPath,
// writing the result to outputPath. The ocrText parameter contains the OCR
// text, with pages separated by "\\n\\n--- Page ---\\n\\n".
//
// For image-only originals, the image is first imported as a PDF, then the
// text layer is stamped.
//
// For already-PDF originals, the text is stamped as an invisible layer onto
// each existing page.
func Generate(inputPath, outputPath, ocrText string) error {
	ext := strings.ToLower(filepath.Ext(inputPath))
	switch ext {
	case ".pdf":
		return addTextLayerToPDF(inputPath, outputPath, ocrText)
	case ".png", ".jpg", ".jpeg", ".tif", ".tiff", ".webp", ".bmp":
		return wrapImageWithText(inputPath, outputPath, ocrText)
	default:
		return fmt.Errorf("unsupported format for archive generation: %s", ext)
	}
}

// addTextLayerToPDF opens an existing PDF and stamps invisible text onto each
// page. The ocrText is split by page markers to get per-page text.
func addTextLayerToPDF(srcPath, dstPath, ocrText string) error {
	pages := splitPages(ocrText)

	// Copy the original PDF first
	if srcPath != dstPath {
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("copy source PDF: %w", err)
		}
	}

	// Add text stamps page by page
	for i, pageText := range pages {
		pageText = strings.TrimSpace(pageText)
		if pageText == "" {
			continue
		}

		wm, err := api.TextWatermark(pageText, "", false, false, types.POINTS)
		if err != nil {
			return fmt.Errorf("create watermark for page %d: %w", i+1, err)
		}
		// Make text invisible (render mode 3) but still selectable
		wm.Update = false
		wm.OnTop = false

		selectedPages := []string{fmt.Sprintf("%d", i+1)}
		if err := api.AddWatermarksFile(dstPath, dstPath, selectedPages, wm, nil); err != nil {
			return fmt.Errorf("add watermark to page %d: %w", i+1, err)
		}
	}

	return nil
}

// wrapImageWithText creates a single-page PDF from an image and overlays the
// OCR text as an invisible selectable layer.
func wrapImageWithText(imgPath, dstPath, ocrText string) error {
	// Import the image as a PDF page
	if err := api.ImportImagesFile([]string{imgPath}, dstPath, nil, nil); err != nil {
		return fmt.Errorf("import image to PDF: %w", err)
	}

	// Now add the text layer
	return addTextLayerToPDF(dstPath, dstPath, ocrText)
}

// splitPages splits OCR text by page markers.
func splitPages(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Split by "--- Page ---" markers (used by the OCR worker)
	if strings.Contains(text, "--- Page ---") {
		return strings.Split(text, "\n\n--- Page ---\n\n")
	}

	// Split by form-feed
	if strings.Contains(text, "\f") {
		return strings.Split(text, "\f")
	}

	// Single page
	return []string{text}
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if _, err := io.Copy(d, s); err != nil {
		return err
	}
	return d.Sync()
}

// OCRTextFromFile reads OCR text from a plain-text OCR sidecar file.
// If the file is JSON (symingest format), it extracts the "text" field.
func OCRTextFromFile(ocrJSONPath string) (string, error) {
	data, err := os.ReadFile(ocrJSONPath)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	return content, nil
}

// ArchivePath returns the vault-relative path for an archival PDF given a
// document path. The archive file is placed alongside the original with an
// "_archive.pdf" suffix.
func ArchivePath(docRelPath string) string {
	ext := filepath.Ext(docRelPath)
	base := strings.TrimSuffix(docRelPath, ext)
	return base + "_archive.pdf"
}

// HasTextLayer performs a quick check: does the PDF contain text operators
// (BT/ET blocks) indicating it has a text layer?
func HasTextLayer(pdfPath string) bool {
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "BT") && strings.Contains(content, "ET")
}
