package extract

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadDOCX_EntryCountCap verifies that an archive with more entries than
// maxZipEntries is rejected at open, before any decompression.
func TestReadDOCX_EntryCountCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flood.docx")
	files := make(map[string]string, maxZipEntries+1)
	for i := 0; i < maxZipEntries+1; i++ {
		files[fmt.Sprintf("part%04d", i)] = "x"
	}
	writeZip(t, path, files)
	_, err := ReadStructuredKind(context.Background(), path, KindDOCX)
	if !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("err = %v, want ErrArchiveLimit (entry count cap)", err)
	}
}

// TestReadDOCX_EntryTooLarge verifies that a single entry above the per-entry
// cap errors instead of being truncated or fully decompressed.
func TestReadDOCX_EntryTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bomb.docx")
	big := strings.Repeat("A", maxZipEntryBytes+1)
	writeZip(t, path, map[string]string{"word/document.xml": big})
	_, err := ReadStructuredKind(context.Background(), path, KindDOCX)
	if !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("err = %v, want ErrArchiveLimit (per-entry cap)", err)
	}
}

// TestReadXLSX_ArchiveTotalCap verifies the multi-part amplifier: each sheet
// stays under the per-entry cap, but the running archive total exceeds
// maxZipTotalBytes and the archive is rejected instead of accumulated.
func TestReadXLSX_ArchiveTotalCap(t *testing.T) {
	const sheets = 5
	// One row of ~30 bytes; repeated until the sheet part exceeds the share
	// of the archive total while staying under the per-entry cap.
	row := `<row><c t="s"><v>0</v></c></row>`
	rowsPerSheet := maxZipTotalBytes/sheets/len(row) + 10
	sheetXML := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
		strings.Repeat(row, rowsPerSheet) + `</sheetData></worksheet>`
	if int64(len(sheetXML)) >= maxZipEntryBytes {
		t.Fatalf("test sheet %d bytes exceeds per-entry cap, adjust the fixture", len(sheetXML))
	}

	path := filepath.Join(t.TempDir(), "many-sheets.xlsx")
	files := map[string]string{
		"xl/sharedStrings.xml": `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>v</t></si></sst>`,
	}
	for i := 0; i < sheets; i++ {
		files[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = sheetXML
	}
	writeZip(t, path, files)

	_, err := ReadStructuredKind(context.Background(), path, KindXLSX)
	if !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("err = %v, want ErrArchiveLimit (archive total cap)", err)
	}
}

// TestArchiveLimit_NotTruncated verifies that a part just under the per-entry
// cap still extracts normally (the cap does not reject legitimate documents).
func TestArchiveLimit_UnderCapExtracts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.docx")
	body := strings.Repeat("<w:p><w:r><w:t>x</w:t></w:r></w:p>", 100)
	writeZip(t, path, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`,
	})
	res, err := ReadStructuredKind(context.Background(), path, KindDOCX)
	if err != nil {
		t.Fatalf("ReadStructuredKind: %v", err)
	}
	if res.Text == "" {
		t.Fatal("expected extracted text for an archive under the caps")
	}
}
