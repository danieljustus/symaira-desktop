package demo

import (
	"fmt"
	"strings"
)

// generatePDF creates a minimal valid PDF containing the given text.
// Each page holds up to ~50 lines. The output is a minimal PDF 1.4 file
// with Helvetica text — small (< 5 KB) and valid enough for viewing.
func generatePDF(title string, lines []string) []byte {
	var b strings.Builder

	// We'll build objects and track their byte offsets for the xref table.
	offsets := make([]int, 0, 16)

	writeObj := func(content string) {
		offsets = append(offsets, b.Len())
		b.WriteString(content)
		b.WriteString(" endobj\n")
	}

	// Header
	b.WriteString("%PDF-1.4\n")

	// Obj 1: Catalog
	writeObj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>")

	// Obj 2: Pages
	writeObj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>")

	// Obj 3: Page
	writeObj("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")

	// Obj 4: Content stream
	stream := buildTextStream(title, lines)
	writeObj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))

	// Obj 5: Font
	writeObj("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	// Cross-reference table
	xrefStart := b.Len()
	b.WriteString("xref\n")
	b.WriteString(fmt.Sprintf("0 %d\n", len(offsets)+1))
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}

	// Trailer
	b.WriteString("trailer\n")
	b.WriteString(fmt.Sprintf("<< /Size %d /Root 1 0 R >>\n", len(offsets)+1))
	b.WriteString("startxref\n")
	b.WriteString(fmt.Sprintf("%d\n", xrefStart))
	b.WriteString("%%EOF\n")

	return []byte(b.String())
}

// buildTextStream creates a PDF content stream with the title and lines.
func buildTextStream(title string, lines []string) string {
	var b strings.Builder
	b.WriteString("BT\n")

	// Title
	b.WriteString("/F1 16 Tf\n")
	b.WriteString("72 740 Td\n")
	b.WriteString("(")
	b.WriteString(escapePDF(title))
	b.WriteString(") Tj\n")

	// Body
	b.WriteString("/F1 11 Tf\n")
	b.WriteString("0 -30 Td\n")

	y := 710
	for _, line := range lines {
		if y < 72 {
			// New page would be needed; just stop for minimal PDF
			break
		}
		b.WriteString("(")
		b.WriteString(escapePDF(line))
		b.WriteString(") Tj\n")
		b.WriteString("0 -16 Td\n")
		y -= 16
	}

	b.WriteString("ET\n")
	return b.String()
}

// escapePDF escapes special characters for PDF text strings.
func escapePDF(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}
