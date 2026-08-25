package parser

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetFileHashAndParse(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "sample.txt")
	content := "Hello Symaira Seek!\nWelcome to Phase 2."
	err = os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	hash, err := GetFileHash(filePath)
	if err != nil {
		t.Fatalf("GetFileHash failed: %v", err)
	}
	if len(hash) != 64 {
		t.Errorf("expected 64 character SHA-256 hash, got %d chars (%s)", len(hash), hash)
	}

	parsed, err := ParseFile(filePath)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	if parsed != content {
		t.Errorf("parsed content mismatch: got %q, want %q", parsed, content)
	}
}

func TestSplitText(t *testing.T) {
	text := "This is a simple text. It has multiple sentences. We want to test recursive splitting."
	// Let's split with a small chunk size of 20 characters and 5 character overlap
	chunks := SplitText(text, 25, 5)

	if len(chunks) == 0 {
		t.Fatalf("expected chunks, got none")
	}

	// Verify all chunks are below or equal to 25 chars
	for i, chunk := range chunks {
		if len(chunk) > 25 {
			t.Errorf("chunk %d exceeds max length (size: %d, content: %q)", i, len(chunk), chunk)
		}
	}

	// Reconstruct text (approximately, allowing for overlaps and whitespace differences)
	reconstructed := strings.Join(chunks, " ")
	if !strings.Contains(reconstructed, "recursive") {
		t.Errorf("reconstructed text does not contain key words, got chunks: %v", chunks)
	}
}

func TestParseFileRejectsOversizedText(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-oversize")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bigPath := filepath.Join(tempDir, "big.txt")
	bigData := make([]byte, MaxIndexFileSize+1)
	for i := range bigData {
		bigData[i] = 'A'
	}
	if err := os.WriteFile(bigPath, bigData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = ParseFile(bigPath)
	if err == nil {
		t.Fatal("expected error for oversized text file, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit, got: %v", err)
	}
}

func TestParseDOCXZipBomb(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-docx-bomb")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	docxPath := filepath.Join(tempDir, "bomb.docx")
	createZipBomb(t, docxPath, "word/document.xml", MaxIndexFileSize+1)

	content, err := ParseFile(docxPath)
	if err == nil && int64(len(content)) > MaxIndexFileSize {
		t.Fatalf("DOCX zip-bomb returned %d bytes, exceeding limit", len(content))
	}
}

func TestParseXLSXZipBomb(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-xlsx-bomb")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	xlsxPath := filepath.Join(tempDir, "bomb.xlsx")
	createZipBomb(t, xlsxPath, "xl/sharedStrings.xml", MaxIndexFileSize+1)

	_, err = ParseFile(xlsxPath)
	if err == nil {
		t.Fatal("expected error or truncated result for zip-bomb XLSX, got nil")
	}
}

func TestParsePPTXZipBomb(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-pptx-bomb")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	pptxPath := filepath.Join(tempDir, "bomb.pptx")
	createZipBomb(t, pptxPath, "ppt/slides/slide1.xml", MaxIndexFileSize+1)

	_, err = ParseFile(pptxPath)
	if err == nil {
		t.Fatal("expected error or truncated result for zip-bomb PPTX, got nil")
	}
}

// createMinimalPDF writes a valid PDF with the given text rendered via a Type1 font.
func createMinimalPDF(t *testing.T, path, text string) {
	t.Helper()

	type segment struct {
		data []byte
	}
	var segs []segment

	segs = append(segs, segment{[]byte("%PDF-1.4\n")})
	segs = append(segs, segment{[]byte("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")})
	segs = append(segs, segment{[]byte("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")})
	segs = append(segs, segment{[]byte("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")})

	stream := fmt.Sprintf("BT\n/F1 12 Tf\n100 700 Td\n(%s) Tj\nET\n", text)
	contentObj := fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(stream), stream)
	segs = append(segs, segment{[]byte(contentObj)})

	segs = append(segs, segment{[]byte("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")})

	offsets := make([]int, 6)
	off := 0
	for i, s := range segs {
		offsets[i] = off
		off += len(s.data)
	}
	xrefOff := off

	var xref strings.Builder
	xref.WriteString("xref\n0 6\n")
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&xref, "%010d 00000 n \n", offsets[i])
	}

	var trailer strings.Builder
	trailer.WriteString(xref.String())
	fmt.Fprintf(&trailer, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)

	var buf []byte
	for _, s := range segs {
		buf = append(buf, s.data...)
	}
	buf = append(buf, []byte(trailer.String())...)

	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatalf("write PDF: %v", err)
	}
}

// createEmptyTextPDF writes a valid PDF with a blank page (no text operators).
func createEmptyTextPDF(t *testing.T, path string) {
	t.Helper()

	var segs [][]byte
	segs = append(segs, []byte("%PDF-1.4\n"))
	segs = append(segs, []byte("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"))
	segs = append(segs, []byte("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n"))
	segs = append(segs, []byte("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << >> >>\nendobj\n"))
	segs = append(segs, []byte("4 0 obj\n<< /Length 0 >>\nstream\nendstream\nendobj\n"))

	offsets := make([]int, len(segs))
	off := 0
	for i, s := range segs {
		offsets[i] = off
		off += len(s)
	}
	xrefOff := off

	var buf bytes.Buffer
	for _, s := range segs {
		buf.Write(s)
	}
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(segs))
	for i := 0; i < len(segs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(segs), xrefOff)

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write empty PDF: %v", err)
	}
}

func TestParseFilePDF(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-pdf")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	pdfPath := filepath.Join(tempDir, "hello.pdf")
	createMinimalPDF(t, pdfPath, "Hello PDF")

	parsed, err := ParseFile(pdfPath)
	if err != nil {
		t.Fatalf("ParseFile(.pdf) failed: %v", err)
	}
	if !strings.Contains(parsed, "Hello PDF") {
		t.Errorf("expected parsed text to contain %q, got %q", "Hello PDF", parsed)
	}
}

func TestParseFilePDFNotFound(t *testing.T) {
	_, err := ParseFile("/nonexistent/path/to/file.pdf")
	if err == nil {
		t.Fatal("expected error for nonexistent PDF, got nil")
	}
	if !strings.Contains(err.Error(), "failed to open PDF") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseFilePDFEmptyText(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-pdf-empty")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	pdfPath := filepath.Join(tempDir, "empty.pdf")
	createEmptyTextPDF(t, pdfPath)

	_, err = ParseFile(pdfPath)
	if err == nil {
		t.Fatal("expected error for empty-text PDF, got nil")
	}
	if !strings.Contains(err.Error(), "no extractable text") {
		t.Errorf("expected 'no extractable text' error, got: %v", err)
	}
}

// --- XLSX tests ---

// createMinimalXLSX builds a minimal XLSX (ZIP+XML). sharedStrings are the
// string table; rows[r][c] is the cell value; cellTypes[r][c] is "s" (shared
// string index), "inlineStr", or "" (numeric).
func createMinimalXLSX(t *testing.T, path string, sharedStrings []string, rows [][]string, cellTypes [][]string) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create xlsx: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)

	if len(sharedStrings) > 0 {
		ssWriter, err := w.Create("xl/sharedStrings.xml")
		if err != nil {
			t.Fatalf("create sharedStrings entry: %v", err)
		}
		var ss strings.Builder
		ss.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
		ss.WriteString(fmt.Sprintf(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="%d" uniqueCount="%d">`, len(sharedStrings), len(sharedStrings)))
		for _, s := range sharedStrings {
			ss.WriteString(fmt.Sprintf(`<si><t>%s</t></si>`, s))
		}
		ss.WriteString("</sst>")
		if _, err := ssWriter.Write([]byte(ss.String())); err != nil {
			t.Fatalf("write sharedStrings: %v", err)
		}
	}

	sheetWriter, err := w.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatalf("create sheet entry: %v", err)
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for rowIdx, row := range rows {
		sheet.WriteString(fmt.Sprintf(`<row r="%d">`, rowIdx+1))
		cols := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		for colIdx, val := range row {
			cellType := ""
			if cellTypes != nil && rowIdx < len(cellTypes) && colIdx < len(cellTypes[rowIdx]) {
				cellType = cellTypes[rowIdx][colIdx]
			}
			cellRef := fmt.Sprintf("%c%d", cols[colIdx%len(cols)], rowIdx+1)
			if cellType == "s" {
				sheet.WriteString(fmt.Sprintf(`<c r="%s" t="s"><v>%s</v></c>`, cellRef, val))
			} else if cellType == "inlineStr" {
				sheet.WriteString(fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, cellRef, val))
			} else {
				sheet.WriteString(fmt.Sprintf(`<c r="%s"><v>%s</v></c>`, cellRef, val))
			}
		}
		sheet.WriteString("</row>")
	}
	sheet.WriteString("</sheetData></worksheet>")
	if _, err := sheetWriter.Write([]byte(sheet.String())); err != nil {
		t.Fatalf("write sheet: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

func TestParseFileXLSXWithSharedStrings(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-xlsx")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	xlsxPath := filepath.Join(tempDir, "data.xlsx")
	sharedStrings := []string{"Hello", "World"}
	rows := [][]string{{"0", "1"}}
	cellTypes := [][]string{{"s", "s"}}

	createMinimalXLSX(t, xlsxPath, sharedStrings, rows, cellTypes)

	parsed, err := ParseFile(xlsxPath)
	if err != nil {
		t.Fatalf("ParseFile(.xlsx) failed: %v", err)
	}
	if !strings.Contains(parsed, "Hello") || !strings.Contains(parsed, "World") {
		t.Errorf("expected parsed text to contain 'Hello' and 'World', got %q", parsed)
	}
}

func TestParseFileXLSXWithMixedCellTypes(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-xlsx-mixed")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	xlsxPath := filepath.Join(tempDir, "mixed.xlsx")
	sharedStrings := []string{"Alpha", "Beta"}
	rows := [][]string{{"0", "99", "1"}}
	cellTypes := [][]string{{"s", "", "s"}}

	createMinimalXLSX(t, xlsxPath, sharedStrings, rows, cellTypes)

	parsed, err := ParseFile(xlsxPath)
	if err != nil {
		t.Fatalf("ParseFile(.xlsx) mixed failed: %v", err)
	}
	if !strings.Contains(parsed, "Alpha") || !strings.Contains(parsed, "Beta") || !strings.Contains(parsed, "99") {
		t.Errorf("expected parsed text to contain 'Alpha', 'Beta', and '99', got %q", parsed)
	}
}

func TestParseFileXLSXWithNumbers(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-xlsx-num")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	xlsxPath := filepath.Join(tempDir, "numbers.xlsx")
	rows := [][]string{{"42", "3.14"}}
	cellTypes := [][]string{{"", ""}}

	createMinimalXLSX(t, xlsxPath, nil, rows, cellTypes)

	parsed, err := ParseFile(xlsxPath)
	if err != nil {
		t.Fatalf("ParseFile(.xlsx) numbers failed: %v", err)
	}
	if !strings.Contains(parsed, "42") || !strings.Contains(parsed, "3.14") {
		t.Errorf("expected parsed text to contain '42' and '3.14', got %q", parsed)
	}
}

func TestParseFileXLSXEmptyText(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-parser-xlsx-empty")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	xlsxPath := filepath.Join(tempDir, "empty.xlsx")
	createMinimalXLSX(t, xlsxPath, nil, nil, nil)

	_, err = ParseFile(xlsxPath)
	if err == nil {
		t.Fatal("expected error for empty XLSX, got nil")
	}
	if !strings.Contains(err.Error(), "no extractable text") {
		t.Errorf("expected 'no extractable text' error, got: %v", err)
	}
}

func TestParseFileXLSXNotFound(t *testing.T) {
	_, err := ParseFile("/nonexistent/path/to/file.xlsx")
	if err == nil {
		t.Fatal("expected error for nonexistent XLSX, got nil")
	}
	if !strings.Contains(err.Error(), "failed to open XLSX") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// --- HTML tests (issue #340) ---

func TestParseFileHTML(t *testing.T) {
	htmlContent := `<!DOCTYPE html>
<html>
<head><title>Docs Home</title><style>.hero { color: red; }</style></head>
<body>
<h1>Welcome to the docs</h1>
<p>Symaira &amp; Seek indexes <b>plain text</b> from HTML.</p>
<script>const secret = "do not index";</script>
<div><p>Second block of content.</p></div>
</body>
</html>`
	htmlPath := filepath.Join(t.TempDir(), "docs.html")
	if err := os.WriteFile(htmlPath, []byte(htmlContent), 0644); err != nil {
		t.Fatalf("write html: %v", err)
	}

	parsed, err := ParseFile(htmlPath)
	if err != nil {
		t.Fatalf("ParseFile(.html) failed: %v", err)
	}

	// Block-level elements produce block boundaries consistent with the
	// Office extraction rule.
	if !strings.Contains(parsed, "Welcome to the docs\n\nSymaira & Seek indexes plain text from HTML.") {
		t.Errorf("expected a block boundary between h1 and p, got %q", parsed)
	}
	if !strings.Contains(parsed, "Second block of content.") {
		t.Errorf("expected div content, got %q", parsed)
	}
	// HTML entities are decoded to their characters.
	if !strings.Contains(parsed, "Symaira & Seek") {
		t.Errorf("expected decoded entity, got %q", parsed)
	}
	// Inline script and style content is excluded from extracted text.
	for _, forbidden := range []string{"do not index", "const secret", "color: red", "Docs Home", "hero"} {
		if strings.Contains(parsed, forbidden) {
			t.Errorf("extracted text must not contain %q, got %q", forbidden, parsed)
		}
	}
	// No tag names, attribute names or attribute values as index terms.
	for _, forbidden := range []string{"<h1", "<p", "DOCTYPE", "html>", "class=", "href"} {
		if strings.Contains(parsed, forbidden) {
			t.Errorf("extracted text must not contain markup %q, got %q", forbidden, parsed)
		}
	}
}

func TestParseFileHTM(t *testing.T) {
	htmPath := filepath.Join(t.TempDir(), "page.htm")
	content := `<html><body><p>Htm works too.</p></body></html>`
	if err := os.WriteFile(htmPath, []byte(content), 0644); err != nil {
		t.Fatalf("write htm: %v", err)
	}

	parsed, err := ParseFile(htmPath)
	if err != nil {
		t.Fatalf("ParseFile(.htm) failed: %v", err)
	}
	if parsed != "Htm works too." {
		t.Errorf("expected stripped text, got %q", parsed)
	}
}

func TestParseFileHTMLRejectsOversized(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "big.html")
	bigData := make([]byte, MaxIndexFileSize+1)
	for i := range bigData {
		bigData[i] = 'A'
	}
	if err := os.WriteFile(htmlPath, bigData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ParseFile(htmlPath)
	if err == nil {
		t.Fatal("expected error for oversized HTML file, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit, got: %v", err)
	}
}

// --- ODF and CSV tests (issue #341) ---

const odfNamespaces = `xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"`

func TestParseFileODT(t *testing.T) {
	contentXML := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document-content ` + odfNamespaces + `><office:body><office:text>` +
		`<text:h text:outline-level="1">ODT Heading</text:h>` +
		`<text:p>First <text:tab/>tabbed paragraph</text:p>` +
		`<text:p>Second<text:line-break/>line</text:p>` +
		`</office:text></office:body></office:document-content>`
	odtPath := filepath.Join(t.TempDir(), "doc.odt")
	createZip(t, odtPath, map[string]string{"content.xml": contentXML})

	parsed, err := ParseFile(odtPath)
	if err != nil {
		t.Fatalf("ParseFile(.odt) failed: %v", err)
	}
	want := "ODT Heading\n\nFirst \ttabbed paragraph\n\nSecond\nline"
	if parsed != want {
		t.Errorf("ODT extraction mismatch, got %q, want %q", parsed, want)
	}
}

func TestParseFileODS(t *testing.T) {
	contentXML := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document-content ` + odfNamespaces + `><office:body><office:spreadsheet><table:table>` +
		`<table:table-row><table:table-cell office:value-type="string"><text:p>alpha</text:p></table:table-cell><table:table-cell office:value-type="string"><text:p>beta</text:p></table:table-cell></table:table-row>` +
		`<table:table-row><table:table-cell office:value-type="float"><office:value>42</office:value></table:table-cell><table:table-cell office:value-type="string"><text:p>delta</text:p></table:table-cell></table:table-row>` +
		`</table:table></office:spreadsheet></office:body></office:document-content>`
	odsPath := filepath.Join(t.TempDir(), "sheet.ods")
	createZip(t, odsPath, map[string]string{"content.xml": contentXML})

	parsed, err := ParseFile(odsPath)
	if err != nil {
		t.Fatalf("ParseFile(.ods) failed: %v", err)
	}
	if !strings.Contains(parsed, "alpha\n\nbeta") {
		t.Errorf("ODS cells must be separate blocks, got %q", parsed)
	}
	if !strings.Contains(parsed, "42") || !strings.Contains(parsed, "delta") {
		t.Errorf("ODS numeric and string cells must be searchable, got %q", parsed)
	}
}

func TestParseFileODP(t *testing.T) {
	contentXML := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document-content ` + odfNamespaces + `><office:body><office:presentation>` +
		`<draw:page draw:name="page1"><draw:frame><draw:text-box>` +
		`<text:p>Slide heading</text:p><text:p>Slide body</text:p>` +
		`</draw:text-box></draw:frame></draw:page>` +
		`</office:presentation></office:body></office:document-content>`
	odpPath := filepath.Join(t.TempDir(), "deck.odp")
	createZip(t, odpPath, map[string]string{"content.xml": contentXML})

	parsed, err := ParseFile(odpPath)
	if err != nil {
		t.Fatalf("ParseFile(.odp) failed: %v", err)
	}
	want := "Slide heading\n\nSlide body"
	if parsed != want {
		t.Errorf("ODP extraction mismatch, got %q, want %q", parsed, want)
	}
}

func TestParseFileCSV(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "people.csv")
	content := "name,role\nada,engineer\nbob,designer\n"
	if err := os.WriteFile(csvPath, []byte(content), 0644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	parsed, err := ParseFile(csvPath)
	if err != nil {
		t.Fatalf("ParseFile(.csv) failed: %v", err)
	}
	want := "name\trole\nada\tengineer\nbob\tdesigner"
	if parsed != want {
		t.Errorf("CSV row boundaries not preserved, got %q, want %q", parsed, want)
	}
}

func TestIsKnownDocumentExtension(t *testing.T) {
	for _, ext := range []string{".rtf", ".RTF", ".epub", ".doc", ".xls", ".ppt", ".odg"} {
		if !IsKnownDocumentExtension(ext) {
			t.Errorf("expected %s to be a known document format", ext)
		}
	}
	for _, ext := range []string{".md", ".txt", ".html", ".htm", ".pdf", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".csv"} {
		if IsKnownDocumentExtension(ext) {
			t.Errorf("expected %s NOT to be an unsupported known format", ext)
		}
	}
}

func TestUnsupportedDocumentSkipMessage(t *testing.T) {
	msg := UnsupportedDocumentSkipMessage("/home/user/docs/manual.rtf", ".rtf")
	if !strings.Contains(msg, "/home/user/docs/manual.rtf") {
		t.Errorf("skip message must name the file, got %q", msg)
	}
	if !strings.Contains(msg, "not indexed") {
		t.Errorf("skip message must give the reason, got %q", msg)
	}
}

func TestParseFileDOCX_ParagraphBoundaries(t *testing.T) {
	docXML := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>first paragraph</w:t></w:r></w:p><w:p><w:r><w:t>second paragraph</w:t></w:r></w:p></w:body></w:document>`
	docxPath := filepath.Join(t.TempDir(), "para.docx")
	createZip(t, docxPath, map[string]string{"word/document.xml": docXML})

	parsed, err := ParseFile(docxPath)
	if err != nil {
		t.Fatalf("ParseFile(.docx) failed: %v", err)
	}
	want := "first paragraph\n\nsecond paragraph"
	if parsed != want {
		t.Errorf("paragraphs must be separated by a blank line, got %q, want %q", parsed, want)
	}
	if strings.Contains(parsed, "first paragraphsecond paragraph") {
		t.Errorf("words from adjacent paragraphs must not be fused, got %q", parsed)
	}
}

func TestParseFileDOCX_BreaksAndTabs(t *testing.T) {
	docXML := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>line one</w:t><w:br/><w:t>line two</w:t></w:r></w:p><w:p><w:r><w:t>col1</w:t><w:tab/><w:t>col2</w:t></w:r></w:p></w:body></w:document>`
	docxPath := filepath.Join(t.TempDir(), "breaks.docx")
	createZip(t, docxPath, map[string]string{"word/document.xml": docXML})

	parsed, err := ParseFile(docxPath)
	if err != nil {
		t.Fatalf("ParseFile(.docx) failed: %v", err)
	}
	want := "line one\nline two\n\ncol1	col2"
	if parsed != want {
		t.Errorf("w:br/w:tab whitespace not preserved, got %q, want %q", parsed, want)
	}
}

func TestParseFilePPTX_TwoTextBoxes(t *testing.T) {
	slideXML := `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><a:txBody><a:p><a:r><a:t>box one</a:t></a:r></a:p></a:txBody></p:sp><p:sp><a:txBody><a:p><a:r><a:t>box two</a:t></a:r></a:p></a:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	pptxPath := filepath.Join(t.TempDir(), "boxes.pptx")
	createZip(t, pptxPath, map[string]string{"ppt/slides/slide1.xml": slideXML})

	parsed, err := ParseFile(pptxPath)
	if err != nil {
		t.Fatalf("ParseFile(.pptx) failed: %v", err)
	}
	want := "box one\n\nbox two"
	if parsed != want {
		t.Errorf("distinct text boxes must be separate blocks, got %q, want %q", parsed, want)
	}
}

func TestParseFileXLSX_RowBoundaries(t *testing.T) {
	xlsxPath := filepath.Join(t.TempDir(), "rows.xlsx")
	rows := [][]string{{"alpha", "beta"}, {"gamma", "delta"}}
	createMinimalXLSX(t, xlsxPath, nil, rows, nil)

	parsed, err := ParseFile(xlsxPath)
	if err != nil {
		t.Fatalf("ParseFile(.xlsx) failed: %v", err)
	}
	want := "alpha	beta\ngamma	delta"
	if parsed != want {
		t.Errorf("expected one line per row, got %q, want %q", parsed, want)
	}
}

func TestExtractXMLText_CollapsesNewlineRuns(t *testing.T) {
	docXML := `<w:document><w:body><w:p><w:r><w:t>alpha</w:t></w:r></w:p><w:p/><w:p/><w:p><w:r><w:t>omega</w:t></w:r></w:p></w:body></w:document>`
	got, err := extractXMLText(strings.NewReader(docXML))
	if err != nil {
		t.Fatalf("extractXMLText: %v", err)
	}
	want := "alpha\n\nomega"
	if got != want {
		t.Errorf("runs of 3+ newlines must collapse to a paragraph break, got %q, want %q", got, want)
	}
}

// --- Archive-level decompression budget tests (issue #342) ---

func TestParseDOCXZipBomb_ManyEntries(t *testing.T) {
	docxPath := filepath.Join(t.TempDir(), "many.docx")
	createManyEntryZip(t, docxPath, MaxArchiveEntries+1, "word/document.xml",
		`<w:document><w:body><w:p><w:r><w:t>tiny</w:t></w:r></w:p></w:body></w:document>`)

	_, err := ParseFile(docxPath)
	if err == nil {
		t.Fatal("expected error for archive with too many entries, got nil")
	}
	if !strings.Contains(err.Error(), "entry") {
		t.Errorf("error should mention the entry limit, got: %v", err)
	}
}

func TestParseXLSXZipBomb_ManyEntries(t *testing.T) {
	xlsxPath := filepath.Join(t.TempDir(), "many.xlsx")
	createManyEntryZip(t, xlsxPath, MaxArchiveEntries+1, "xl/worksheets/sheet1.xml",
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`)

	_, err := ParseFile(xlsxPath)
	if err == nil {
		t.Fatal("expected error for archive with too many entries, got nil")
	}
	if !strings.Contains(err.Error(), "entry") {
		t.Errorf("error should mention the entry limit, got: %v", err)
	}
}

func TestParseXLSXZipBomb_ArchiveBudget(t *testing.T) {
	// 12 worksheets, each 9 MiB of cell text: every entry stays under the
	// per-entry cap but the archive collectively exceeds
	// MaxArchiveDecompressedBytes.
	xlsxPath := filepath.Join(t.TempDir(), "budget.xlsx")
	createBudgetBombXLSX(t, xlsxPath, 12, 9<<20)

	_, err := ParseFile(xlsxPath)
	if err == nil {
		t.Fatal("expected budget rejection for archive exceeding decompression budget, got nil")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error should mention the decompression budget, got: %v", err)
	}
}

func TestParseFileXLSX_MultiSheetStillIndexes(t *testing.T) {
	xlsxPath := filepath.Join(t.TempDir(), "multi.xlsx")
	createZip(t, xlsxPath, map[string]string{
		"xl/sharedStrings.xml":     `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>alpha</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><v>42</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet3.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><v>3.14</v></c></row></sheetData></worksheet>`,
	})

	parsed, err := ParseFile(xlsxPath)
	if err != nil {
		t.Fatalf("multi-sheet workbook must still index: %v", err)
	}
	for _, want := range []string{"alpha", "42", "3.14"} {
		if !strings.Contains(parsed, want) {
			t.Errorf("expected %q in multi-sheet extraction, got %q", want, parsed)
		}
	}
}

func TestParseFilePPTX_MultiSlideStillIndexes(t *testing.T) {
	slide := func(text string) string {
		return `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><a:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></a:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	pptxPath := filepath.Join(t.TempDir(), "multi.pptx")
	createZip(t, pptxPath, map[string]string{
		"ppt/slides/slide1.xml": slide("first slide"),
		"ppt/slides/slide2.xml": slide("second slide"),
	})

	parsed, err := ParseFile(pptxPath)
	if err != nil {
		t.Fatalf("multi-slide deck must still index: %v", err)
	}
	if !strings.Contains(parsed, "first slide") || !strings.Contains(parsed, "second slide") {
		t.Errorf("expected both slides, got %q", parsed)
	}
}

// createManyEntryZip writes a ZIP archive with count entries: the named
// entry carries namedContent, all other entries hold a single byte.
func createManyEntryZip(t *testing.T, zipPath string, count int, namedEntry, namedContent string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("entry%d.xml", i)
		content := "x"
		if i == 0 {
			name = namedEntry
			content = namedContent
		}
		entry, err := w.Create(name)
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

// createBudgetBombXLSX writes an XLSX with n worksheet entries, each a
// valid sheet whose single cell holds size bytes of text.
func createBudgetBombXLSX(t *testing.T, xlsxPath string, n, size int) {
	t.Helper()
	f, err := os.Create(xlsxPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for i := 1; i <= n; i++ {
		entry, err := w.Create(fmt.Sprintf("xl/worksheets/sheet%d.xml", i))
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		content := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><v>` +
			strings.Repeat("X", size) +
			`</v></c></row></sheetData></worksheet>`
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

func TestSplitText_ZeroChunkSize(t *testing.T) {
	got := SplitText("any text", 0, 0)
	if len(got) != 1 || got[0] != "any text" {
		t.Errorf("zero chunkSize should return full text, got %v", got)
	}
}

func TestSplitText_NegativeChunkSize(t *testing.T) {
	got := SplitText("hello world", -10, 0)
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("negative chunkSize should return full text, got %v", got)
	}
}

func TestSplitText_OverlapExceedsChunkSize(t *testing.T) {
	text := "aaa bbb ccc ddd eee fff ggg hhh"
	chunks := SplitText(text, 10, 50)
	for i, c := range chunks {
		if len(c) > 10 {
			t.Errorf("chunk %d exceeds chunkSize: len=%d, content=%q", i, len(c), c)
		}
	}
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}
}

func TestSplitText_NoSeparatorFound(t *testing.T) {
	text := strings.Repeat("a", 50)
	chunks := SplitText(text, 20, 5)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for long no-separator text, got %d", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total < len(text) {
		t.Errorf("reconstructed length %d < original %d", total, len(text))
	}
}

func TestSplitText_RecursiveNewlineSplit(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6"
	chunks := SplitText(text, 15, 3)
	for i, c := range chunks {
		if len(c) > 15 {
			t.Errorf("chunk %d exceeds chunkSize: len=%d", i, len(c))
		}
	}
	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d", len(chunks))
	}
}

func TestSplitText_EmptyString(t *testing.T) {
	got := SplitText("", 10, 2)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("empty string should return [\"\"], got %v", got)
	}
}

func TestSplitText_ExactChunkSize(t *testing.T) {
	text := "exact ten chars"
	got := SplitText(text, 15, 3)
	if len(got) != 1 || got[0] != text {
		t.Errorf("text shorter than chunkSize should return single chunk, got %v", got)
	}
}

func TestSplitTextWithSpans_MatchesSplitTextContent(t *testing.T) {
	text := "This is a simple text. It has multiple sentences. We want to test recursive splitting."
	textChunks := SplitText(text, 25, 5)
	spans := SplitTextWithSpans(text, 25, 5)

	if len(spans) != len(textChunks) {
		t.Fatalf("span count %d != chunk count %d", len(spans), len(textChunks))
	}
	for i := range spans {
		if spans[i].Text != textChunks[i] {
			t.Errorf("span %d text mismatch: got %q want %q", i, spans[i].Text, textChunks[i])
		}
		if spans[i].End-spans[i].Start != len(spans[i].Text) {
			t.Errorf("span %d length mismatch: End-Start=%d len(Text)=%d", i, spans[i].End-spans[i].Start, len(spans[i].Text))
		}
	}
}

func TestSplitTextWithSpans_NoOverlapIsByteExact(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6"
	spans := SplitTextWithSpans(text, 15, 0)
	for i, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start > s.End {
			t.Fatalf("span %d out of bounds: %+v (len=%d)", i, s, len(text))
		}
		if text[s.Start:s.End] != s.Text {
			t.Errorf("span %d not byte-exact: text[%d:%d]=%q want %q", i, s.Start, s.End, text[s.Start:s.End], s.Text)
		}
	}
}

func TestSplitTextWithSpans_SpansAreOrderedAndInBounds(t *testing.T) {
	text := strings.Repeat("word ", 200)
	spans := SplitTextWithSpans(text, 40, 10)
	if len(spans) == 0 {
		t.Fatal("expected spans, got none")
	}
	for i, s := range spans {
		if s.Start < 0 || s.End < s.Start || s.End > len(text) {
			t.Errorf("span %d has invalid bounds: %+v (text len=%d)", i, s, len(text))
		}
		if i > 0 && s.Start < spans[i-1].Start {
			t.Errorf("span %d starts before previous span: %d < %d", i, s.Start, spans[i-1].Start)
		}
	}
}

func TestSplitTextWithSpans_ZeroChunkSize(t *testing.T) {
	spans := SplitTextWithSpans("any text", 0, 0)
	if len(spans) != 1 || spans[0].Text != "any text" || spans[0].Start != 0 || spans[0].End != len("any text") {
		t.Errorf("zero chunkSize should return full text span, got %v", spans)
	}
}

func TestSplitTextWithSpans_EmptyString(t *testing.T) {
	spans := SplitTextWithSpans("", 10, 2)
	if len(spans) != 1 || spans[0].Text != "" || spans[0].Start != 0 || spans[0].End != 0 {
		t.Errorf("empty string should return single empty span, got %v", spans)
	}
}

func TestSplitTextWithSpans_NoSeparatorFound(t *testing.T) {
	text := strings.Repeat("a", 50)
	spans := SplitTextWithSpans(text, 20, 5)
	if len(spans) < 2 {
		t.Fatalf("expected multiple spans for long no-separator text, got %d", len(spans))
	}
	for i, s := range spans {
		if text[s.Start:s.End] != s.Text {
			t.Errorf("span %d not byte-exact: text[%d:%d]=%q want %q", i, s.Start, s.End, text[s.Start:s.End], s.Text)
		}
	}
}

func TestExtractXMLText_MalformedXML(t *testing.T) {
	malformed := []byte(`<root><t>good</t><broken>&&&</root>`)
	r := bytes.NewReader(malformed)
	got, err := extractXMLText(r)
	if err != nil {
		t.Fatalf("extractXMLText should not return error on malformed XML, got: %v", err)
	}
	if !strings.Contains(got, "good") {
		t.Errorf("expected extracted text to contain 'good', got %q", got)
	}
}

func TestExtractXMLText_NoTextElements(t *testing.T) {
	xml := []byte(`<root><para>hello</para></root>`)
	r := bytes.NewReader(xml)
	got, err := extractXMLText(r)
	if err != nil {
		t.Fatalf("extractXMLText error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty text for non-t elements, got %q", got)
	}
}

func TestExtractXMLText_EmptyInput(t *testing.T) {
	r := bytes.NewReader(nil)
	got, err := extractXMLText(r)
	if err != nil {
		t.Fatalf("extractXMLText on empty input: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractPPTXSlideText_MalformedXML(t *testing.T) {
	slideXML := `<?xml version="1.0"?><p><t>visible</t><broken>&&&</p>`
	fakeFile := createFakeZipFile(t, slideXML)
	got, err := extractPPTXSlideText(fakeFile, &archiveBudget{remaining: MaxArchiveDecompressedBytes})
	if err != nil {
		t.Fatalf("extractPPTXSlideText should handle malformed XML: %v", err)
	}
	if !strings.Contains(got, "visible") {
		t.Errorf("expected text to contain 'visible', got %q", got)
	}
}

func TestExtractPPTXSlideText_EmptySlide(t *testing.T) {
	slideXML := `<?xml version="1.0"?><p><r><t></t></r></p>`
	fakeFile := createFakeZipFile(t, slideXML)
	got, err := extractPPTXSlideText(fakeFile, &archiveBudget{remaining: MaxArchiveDecompressedBytes})
	if err != nil {
		t.Fatalf("extractPPTXSlideText: %v", err)
	}
	if got != "" {
		t.Errorf("empty slide should produce no text, got %q", got)
	}
}

func TestExtractPPTXSlideText_MultipleParagraphs(t *testing.T) {
	slideXML := `<?xml version="1.0"?><p><t>first</t></p><p><t>second</t></p>`
	fakeFile := createFakeZipFile(t, slideXML)
	got, err := extractPPTXSlideText(fakeFile, &archiveBudget{remaining: MaxArchiveDecompressedBytes})
	if err != nil {
		t.Fatalf("extractPPTXSlideText: %v", err)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("expected both paragraphs, got %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Error("expected newline between paragraphs")
	}
}

func TestExtractPPTXSlideText_NoTextElements(t *testing.T) {
	slideXML := `<?xml version="1.0"?><p><r><rPr/></r></p>`
	fakeFile := createFakeZipFile(t, slideXML)
	got, err := extractPPTXSlideText(fakeFile, &archiveBudget{remaining: MaxArchiveDecompressedBytes})
	if err != nil {
		t.Fatalf("extractPPTXSlideText: %v", err)
	}
	if got != "" {
		t.Errorf("slide without text elements should produce no text, got %q", got)
	}
}

func createFakeZipFile(t *testing.T, xmlContent string) *zip.File {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	entry, err := w.Create("slide1.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write([]byte(xmlContent)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip reader: %v", err)
	}
	return r.File[0]
}

// createZip writes a ZIP archive to disk with the given entries.
func createZip(t *testing.T, zipPath string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range entries {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

// createZipBomb writes a ZIP file containing a single entry whose
// decompressed content exceeds MaxIndexFileSize bytes.
func createZipBomb(t *testing.T, zipPath, entryName string, decompressedSize int) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	entry, err := w.Create(entryName)
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'X'
	}
	written := 0
	for written < decompressedSize {
		n := len(chunk)
		if written+n > decompressedSize {
			n = decompressedSize - written
		}
		if _, err := entry.Write(chunk[:n]); err != nil {
			t.Fatalf("write entry: %v", err)
		}
		written += n
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}
