package press

import (
	"bytes"
	"compress/zlib"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSVGFontFamilyResolution tests that when a Markdown document contains an
// embedded SVG image using font-family="Inter", the Typst PDF pipeline resolves
// the font from the embedded/configured font path and embeds the Inter font
// families (Inter-Regular and Inter-Bold) in the resulting PDF.
//
// The test skips cleanly when typst is not installed on the system PATH.
func TestSVGFontFamilyResolution(t *testing.T) {
	ctx := context.Background()
	eng := DetectTypst(ctx, "")
	if !eng.Available {
		t.Skip("typst not available on PATH: skipping Typst SVG font-family resolution fixture test")
	}

	docDir := t.TempDir()
	svgPath := filepath.Join(docDir, "diagram.svg")
	svgContent := `<svg xmlns="http://www.w3.org/2000/svg" width="500" height="200" viewBox="0 0 500 200">
  <rect width="100%" height="100%" fill="#f8f9fa"/>
  <text x="20" y="50" font-family="Inter" font-size="18" fill="#111827">Inter Regular Node Label</text>
  <text x="20" y="100" font-family="Inter" font-weight="bold" font-size="18" fill="#111827">Inter Bold Heading Label</text>
  <text x="20" y="150" style="font-family: Inter; font-weight: 700; font-size: 16px;" fill="#374151">Inter CSS Styled Label</text>
</svg>`
	if err := os.WriteFile(svgPath, []byte(svgContent), 0o644); err != nil {
		t.Fatalf("failed to write SVG fixture: %v", err)
	}

	mdContent := `---
profile: report
title: SVG Font Resolution Test
date: 24.08.2026
---
# Architecture Overview

Below is an embedded SVG vector graphic using Inter font families:

![System Architecture](diagram.svg)
`
	outPath := filepath.Join(docDir, "output.pdf")
	req := Request{
		Source:     []byte(mdContent),
		SourceDir:  docDir,
		OutputPath: outPath,
		Engine: EngineConfig{
			Timeout: 30 * time.Second,
		},
	}

	res, err := Render(ctx, req)
	if err != nil {
		t.Fatalf("Render failed for document with SVG image: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil render Result")
	}
	if res.Engine != "typst" {
		t.Errorf("expected engine %q, got %q", "typst", res.Engine)
	}

	pdfData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read output PDF: %v", err)
	}
	if !bytes.HasPrefix(pdfData, []byte("%PDF-")) {
		t.Errorf("output file does not start with PDF magic bytes")
	}
	if len(pdfData) < 1000 {
		t.Errorf("output PDF unexpectedly small: %d bytes", len(pdfData))
	}

	// Decompress PDF streams to inspect font objects and descriptors.
	decompressed := extractPDFTextAndDescriptors(pdfData)

	// Verify that Inter-Regular and Inter-Bold are resolved and embedded.
	if !bytes.Contains(decompressed, []byte("Inter-Regular")) && !bytes.Contains(decompressed, []byte("Inter_Regular")) {
		t.Errorf("PDF does not contain Inter-Regular font reference")
	}
	if !bytes.Contains(decompressed, []byte("Inter-Bold")) && !bytes.Contains(decompressed, []byte("Inter_Bold")) {
		t.Errorf("PDF does not contain Inter-Bold font reference")
	}
}

// TestSVGFontFamilyResolution_SkipsWhenUnavailable confirms that probing for
// an unavailable typst binary reports Available=false with an actionable hint
// and allows consumers/tests to skip cleanly without panics or errors.
func TestSVGFontFamilyResolution_SkipsWhenUnavailable(t *testing.T) {
	ResetProbeCache()
	eng := DetectTypst(context.Background(), "nonexistent-typst-binary-532")
	if eng.Available {
		t.Fatalf("expected Available=false for nonexistent binary, got true")
	}
	if eng.Hint == "" {
		t.Errorf("expected install hint when typst is unavailable")
	}
	if !strings.Contains(eng.Hint, "typst not found on PATH") {
		t.Errorf("unexpected hint content: %s", eng.Hint)
	}
}

// extractPDFTextAndDescriptors gathers uncompressed stream chunks and the raw
// PDF bytes so font descriptors and names can be searched reliably.
func extractPDFTextAndDescriptors(pdfData []byte) []byte {
	var buf bytes.Buffer
	buf.Write(pdfData)

	streamRe := regexp.MustCompile(`(?s)stream[
]+(.*?)[
]+endstream`)
	matches := streamRe.FindAllSubmatch(pdfData, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := m[1]
		r, err := zlib.NewReader(bytes.NewReader(raw))
		if err == nil {
			uncompressed, err := io.ReadAll(r)
			_ = r.Close()
			if err == nil {
				buf.WriteByte('\n')
				buf.Write(uncompressed)
			}
		}
	}
	return buf.Bytes()
}
