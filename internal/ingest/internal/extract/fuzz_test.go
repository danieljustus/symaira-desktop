package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzExtract exercises every structured reader over arbitrary bytes. The
// fixed archive limits (limits.go) bound memory, the per-call context timeout
// bounds runtime, and any panic or hang is a failure. In normal `go test`
// mode only the seed corpus runs, which is what CI executes on every push.
func FuzzExtract(f *testing.F) {
	kinds := []Kind{
		KindHTML, KindRTF, KindDOCX, KindXLSX, KindPPTX,
		KindODT, KindODS, KindODP, KindEPUB, KindEML,
	}
	// Seed with the malformed corpus so mutations start from hostile inputs.
	_ = filepath.WalkDir("testdata/malformed", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if data, err := os.ReadFile(path); err == nil {
			f.Add(data)
		}
		return nil
	})
	// Seed with valid packages so the fuzzer mutates from known-good input.
	f.Add(validZIPSeed(f, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>seed</w:t></w:r></w:p></w:body></w:document>`,
	}))
	f.Add(validZIPSeed(f, map[string]string{
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c><v>1</v></c></row></sheetData></worksheet>`,
	}))
	f.Add(validZIPSeed(f, map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:body><office:text><text:p xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">seed</text:p></office:text></office:body></office:document-content>`}))
	f.Add([]byte(`<html><body><p>Hello</p></body></html>`))
	f.Add([]byte(`{\rtf1 hello \par world}`))
	f.Add([]byte("Subject: seed\r\n\r\nbody"))
	f.Add([]byte("PK\x03\x04truncated-zip"))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzz.bin")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, kind := range kinds {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = ReadStructuredKind(ctx, path, kind)
			cancel()
		}
	})
}

// validZIPSeed builds a deterministic zip for fuzz seeding.
func validZIPSeed(f *testing.F, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			f.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			f.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}
