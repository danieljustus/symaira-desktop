package parser

import (
	"bytes"
	"compress/zlib"
	"fmt"
)

// buildExtractionPDF writes a deterministic PDF whose objects are given in
// full (object number = index + 1, root must be object 1). Offsets are real
// byte counts.
func buildExtractionPDF(objects ...string) []byte {
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

func pdfStream(dict, body string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(body), body)
}

// pdfPredictorStream wraps body in a PNG-Up predictor (12) over Flate with a
// single column per row group so each byte is its own row.
func pdfPredictorStream(body string) string {
	var raw bytes.Buffer
	prev := byte(0)
	for i := 0; i < len(body); i++ {
		raw.WriteByte(2)
		raw.WriteByte(body[i] - prev)
		prev = body[i]
	}
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(raw.Bytes())
	_ = w.Close()
	return pdfStream("/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 1 >>", buf.String())
}

func pdfFlateStream(body string) string {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	return pdfStream("/Filter /FlateDecode", buf.String())
}

// Object layout used by the single-page helpers: 1 catalog, 2 pages, 3 page,
// 4 content stream, then fonts and extras from 5 on.
func pdfOnePage(resources, content string, extra ...string) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << " + resources + " >> /Contents 4 0 R >>",
		pdfStream("", content),
	}
	return buildExtractionPDF(append(objects, extra...)...)
}

const toUnicodeCMap = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<00> <FF>
endcodespacerange
2 beginbfchar
<01> <0041>
<02> <00E4>
endbfchar
1 beginbfrange
<10> <12> <0061>
endbfrange
endcmap
CMapName currentdict /CMap defineresource pop
end
end`

const toUnicodeCMap2Byte = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
2 beginbfchar
<0003> <0058>
<0004> <65E5>
endbfchar
1 beginbfrange
<0010> <0012> <0061>
endbfrange
endcmap
CMapName currentdict /CMap defineresource pop
end
end`

func pdfFontExtractionInputs() []extractionInput {
	win := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"
	mac := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>"
	return []extractionInput{
		{"font-tounicode.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0102> Tj <101112> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>", pdfStream("", toUnicodeCMap)), false},
		{"font-tounicode-over-encoding.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0102> Tj (xy) Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 6 0 R >>", pdfStream("", toUnicodeCMap)), false},
		{"font-type0-identity.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0003> Tj [<0004> 120 <001000110012>] TJ ET",
			"<< /Type /Font /Subtype /Type0 /BaseFont /Fixture /Encoding /Identity-H /DescendantFonts [6 0 R] /ToUnicode 7 0 R >>",
			"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Fixture /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> >>",
			pdfStream("", toUnicodeCMap2Byte)), false},
		{"font-type0-no-tounicode.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <00480069> Tj ET",
			"<< /Type /Font /Subtype /Type0 /BaseFont /Fixture /Encoding /Identity-H /DescendantFonts [6 0 R] >>",
			"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Fixture /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> >>"), false},
		{"font-macroman.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <8a9fa0d0> Tj ET", mac), false},
		{"font-standard-encoding.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <61e1aeb1> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /StandardEncoding >>"), false},
		{"font-differences.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <414243> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Type /Encoding /BaseEncoding /WinAnsiEncoding /Differences [65 /eacute /germandbls 67 /uni65E5 /unknownglyph] >> >>"), false},
		{"font-no-encoding.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <48e9ff> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"), false},
		{"font-two-fonts.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R /F2 6 0 R >>", "BT /F1 12 Tf <e9> Tj /F2 12 Tf <8a> Tj /F1 12 Tf <e9> Tj ET", win, mac), false},
		{"font-missing-resource.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F9 12 Tf <e9> Tj /F1 12 Tf <e9> Tj ET", win), false},
		{"font-before-tf.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT <e9> Tj /F1 12 Tf <e9> Tj ET", win), false},
		{"font-tf-persists-across-bt.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <e9> Tj ET BT <e9> Tj ET", win), false},
		{"string-escape-parens.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf (a\(b\)\\c) Tj ET`, win), false},
		{"string-escape-octal.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf (\101\53 \1\0010) Tj ET`, win), false},
		{"string-escape-control.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf (x\ny\rz\tw\bv\fu) Tj ET`, win), false},
		{"string-escape-continuation.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (ab\\\ncd\\\r\nef\\q) Tj ET", win), false},
		{"string-nested-parens.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf (a(b(c)d)e) Tj ET`, win), false},
		{"string-hex-odd.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf <414> Tj <41 42 4> Tj ET`, win), false},
		{"string-hex-spaces.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <48 65\n6c6C6f> Tj ET", win), false},
		{"string-hex-invalid.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf <48zz65> Tj (after) Tj ET`, win), false},
		{"string-unterminated.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", `BT /F1 12 Tf (never closed`, win), false},
		{"text-positioning-ops.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf 1 0 0 1 10 10 Tm (a) Tj 5 5 Td (b) Tj 0 -12 TD (c) Tj 3 Tc 2 Tw 100 Tz (d) Tj ET", win), false},
		{"tj-kerning-only.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf [(A) -500 (B) -50 (C) 1000 (D)] TJ ET", win), false},
		{"tj-non-array.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (x) TJ (y) Tj ET", win), false},
		{"tj-wrong-arity.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (a) (b) Tj ET", win), false},
		{"tf-wrong-arity.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 Tf (x) Tj ET", win), false},
		{"contents-array.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 6 0 R >> >> /Contents [4 0 R 5 0 R] >>",
			pdfStream("", "BT /F1 12 Tf"),
			pdfStream("", "(split across) Tj ET"),
			win), false},
		{"inherited-resources.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] /Resources << /Font << /F1 5 0 R >> >> /MediaBox [0 0 612 792] >>",
			"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
			pdfStream("", "BT /F1 12 Tf <e9> Tj ET"),
			win), false},
		{"nested-page-tree.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 2 /Kids [3 0 R 6 0 R] >>",
			"<< /Type /Pages /Parent 2 0 R /Count 1 /Kids [4 0 R] /Resources << /Font << /F1 8 0 R >> >> >>",
			"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 612 792] /Contents 5 0 R >>",
			pdfStream("", "BT /F1 12 Tf (first) Tj ET"),
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 7 0 R /Resources << /Font << /F1 8 0 R >> >> >>",
			pdfStream("", "BT /F1 12 Tf (second) Tj ET"),
			win), false},
		{"flate-content.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfFlateStream("BT /F1 12 Tf (compressed text) Tj ET"),
			win), false},
		{"indirect-length-and-font-ref.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources 6 0 R /Contents 4 0 R >>",
			"<< /Length 5 0 R >>\nstream\nBT /F1 12 Tf <e9> Tj ET\nendstream",
			"23",
			"<< /Font << /F1 7 0 R >> >>",
			win), false},
		{"no-xref-recovery.pdf", ".pdf", []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Count 1 /Kids [3 0 R] >>\nendobj\n3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n4 0 obj\n<< /Length 30 >>\nstream\nBT /F1 12 Tf (recovered) Tj ET\nendstream\nendobj\n5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n"), false},
		{"truncated.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (cut) Tj ET", win)[:200], false},
		{"header-only.pdf", ".pdf", []byte("%PDF-1.4\n"), false},
		{"font-bfrange-array.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <010203> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 begincodespacerange <00> <FF> endcodespacerange\n1 beginbfrange <01> <03> [<0058> <0059> <005A>] endbfrange\n")), false},
		{"font-bfrange-offset.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0A0B0C0D> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 begincodespacerange <00> <FF> endcodespacerange\n1 beginbfrange <0A> <0C> <0041> endbfrange\n")), false},
		{"font-bfchar-surrogate-pair.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0102> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 begincodespacerange <00> <FF> endcodespacerange\n2 beginbfchar <01> <D83DDE00> <02> <D83D> endbfchar\n")), false},
		{"font-cmap-odd-utf16.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <01> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 begincodespacerange <00> <FF> endcodespacerange\n1 beginbfchar <01> <004100> endbfchar\n")), false},
		{"font-cmap-no-codespace.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0102> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 beginbfchar <01> <0041> endbfchar\n")), false},
		{"font-cmap-bad-codespace.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <0102> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 begincodespacerange <00> <FFFF> endcodespacerange\n1 beginbfchar <01> <0041> endbfchar\n")), false},
		{"font-cmap-unmapped-code.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <010900ff> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfStream("", "1 begincodespacerange <00> <7F> endcodespacerange\n1 beginbfchar <01> <0041> endbfchar\n")), false},
		{"font-cmap-flate.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <01> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
			pdfFlateStream("1 begincodespacerange <00> <FF> endcodespacerange\n1 beginbfchar <01> <0041> endbfchar\n")), false},
		{"font-tounicode-not-stream.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <e9> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode (nope) >>"), false},
		{"font-encoding-unknown-name.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <e9> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /SomethingElse >>"), false},
		{"font-encoding-indirect-dict.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf <4142> Tj ET",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding 6 0 R >>",
			"<< /Type /Encoding /Differences [65 7 0 R /Euro] >>", "/germandbls"), false},
		{"flate-predictor-12.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfPredictorStream("BT /F1 12 Tf (predicted) Tj ET"),
			win), false},
		{"filter-lzw.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("/Filter /LZWDecode", "BT /F1 12 Tf (x) Tj ET"),
			win), false},
		{"filter-corrupt-flate.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("/Filter /FlateDecode", "not zlib data at all"),
			win), false},
		{"contents-indirect-array.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 7 0 R >> >> /Contents 4 0 R >>",
			"[5 0 R 6 0 R]",
			pdfStream("", "BT /F1 12 Tf [(one) -20 (two)] TJ"),
			pdfStream("", "(three) ' ET"),
			win), false},
		{"page-without-contents.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 2 /Kids [3 0 R 4 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 6 0 R >> >> /Contents 5 0 R >>",
			pdfStream("", "BT /F1 12 Tf (second page) Tj ET"),
			win), false},
		{"pages-count-too-small.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R 4 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 5 0 R >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 5 0 R >>",
			pdfStream("", "BT /F1 12 Tf (counted) Tj ET"),
			win), false},
		{"pages-count-too-large.pdf", ".pdf", buildExtractionPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 5 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			pdfStream("", "BT /F1 12 Tf (one) Tj ET"),
			win), false},
		{"content-lexer-oddities.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "% comment line\nBT /F#31 12 Tf true false null 1.5 -2 +3 .5 (ok) Tj /OC << /A [1 2 << /B (c) >>] >> BDC (inner) Tj EMC ET", win), false},
		{"content-stray-bracket.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf ] (after) Tj ET", win), false},
		{"content-stray-gt.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf > (after) Tj ET", win), false},
		{"content-braces.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf { (in) Tj } (out) Tj ET", win), false},
		{"content-postscript-ops.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf 1 dict begin /K (v) def K Tj end ET", win), false},
		{"content-integer-overflow.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf 99999999999999999999 (x) Tj ET", win), false},
		{"header-pdf-1-7.pdf", ".pdf", bytes.Replace(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (v17) Tj ET", win), []byte("%PDF-1.4"), []byte("%PDF-1.7"), 1), false},
		{"header-pdf-1-8.pdf", ".pdf", bytes.Replace(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (v18) Tj ET", win), []byte("%PDF-1.4"), []byte("%PDF-1.8"), 1), false},
		{"header-pdf-2-0.pdf", ".pdf", bytes.Replace(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (v20) Tj ET", win), []byte("%PDF-1.4"), []byte("%PDF-2.0"), 1), false},
		{"header-crlf.pdf", ".pdf", bytes.Replace(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (crlf) Tj ET", win), []byte("%PDF-1.4\n"), []byte("%PDF-1.4\r\n"), 1), false},
		{"header-trailing-space.pdf", ".pdf", bytes.Replace(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (sp) Tj ET", win), []byte("%PDF-1.4\n"), []byte("%PDF-1.4 \n"), 1), false},
		{"eof-junk-after.pdf", ".pdf", append(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (junk) Tj ET", win), bytes.Repeat([]byte("x"), 200)...), false},
		{"eof-trailing-whitespace.pdf", ".pdf", append(pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (ws) Tj ET", win), []byte(" \r\n\t\n")...), false},
		{"unicode-literal-string.pdf", ".pdf", pdfOnePage("/Font << /F1 5 0 R >>", "BT /F1 12 Tf (\xfe\xff\x00H\x00i) Tj ET", win), false},
	}
}
