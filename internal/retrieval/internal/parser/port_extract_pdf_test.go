package parser

import (
	"bytes"
	"fmt"
)

// Minimal deterministic PDF writer. Offsets and stream lengths are actual byte
// counts, not guessed fixtures; ParseFile then exercises ledongthuc/pdf.
func extractionPDF(pages ...string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"}
	kids := ""
	for _, stream := range pages {
		page := len(objects) + 1
		content := page + 1
		kids += fmt.Sprintf("%d 0 R ", page)
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", content), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), kids)
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), start)
	return out.Bytes()
}
func pdfExtractionInputs() []extractionInput {
	return []extractionInput{
		{"simple.pdf", ".pdf", extractionPDF("BT /F1 12 Tf (Hello world) Tj ET"), false},
		{"operators.pdf", ".pdf", extractionPDF("BT /F1 12 Tf (First) Tj T* [(sec) 200 (ond)] TJ (quote) ' ET BT (last) Tj ET"), false},
		{"pages.pdf", ".pdf", extractionPDF("BT /F1 12 Tf (hyphen-) Tj ET", "", "BT /F1 12 Tf (ated text) Tj ET"), false},
		{"winansi.pdf", ".pdf", extractionPDF("BT /F1 12 Tf <636166e92080> Tj ET"), false},
		{"empty.pdf", ".pdf", extractionPDF(""), false},
		{"invalid.pdf", ".pdf", []byte("not a PDF"), false},
		{"bad-double-quote.pdf", ".pdf", extractionPDF("BT /F1 12 Tf 0 0 (ignored) \" ET"), false},
	}
}
