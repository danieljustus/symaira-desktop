package extract

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// zipBytes builds a zip archive in memory for detection fixtures.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name     string
		data     []byte
		ext      string
		wantKind Kind
	}{
		{"pdf", []byte("%PDF-1.4"), ".pdf", KindPDF},
		{"png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00}, ".png", KindPNG},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, ".jpg", KindJPEG},
		{"tiff_le", []byte("II*\x00"), ".tiff", KindTIFF},
		{"webp", []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}, ".webp", KindWebP},
		{"heic", []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, ".heic", KindHEIC},
		{"text", []byte("hello world"), ".txt", KindText},
		{"csv", []byte("date,amount\n2026-07-02,12.34\n"), ".csv", KindCSV},
		{"markdown", []byte("# hi"), ".md", KindMarkdown},
		{"html", []byte("<html><body>hi</body></html>"), ".html", KindHTML},
		{"rtf", []byte("{\\rtf1 hi}"), ".rtf", KindRTF},
		{"docx", zipBytes(t, map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`}), ".docx", KindDOCX},
		{"xlsx", zipBytes(t, map[string]string{"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`}), ".xlsx", KindXLSX},
		{"pptx", zipBytes(t, map[string]string{"ppt/presentation.xml": `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`}), ".pptx", KindPPTX},
		{"odt", zipBytes(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text"}), ".odt", KindODT},
		{"ods", zipBytes(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.spreadsheet"}), ".ods", KindODS},
		{"odp", zipBytes(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.presentation"}), ".odp", KindODP},
		{"epub", zipBytes(t, map[string]string{"mimetype": "application/epub+zip"}), ".epub", KindEPUB},
		{"eml", []byte("Subject: hi\n\nbody"), ".eml", KindEML},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+tc.ext)
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := Detect(path)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got != tc.wantKind {
				t.Fatalf("kind = %q, want %q", got, tc.wantKind)
			}
		})
	}
}

func TestDetect_Unknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.bin")
	if err := os.WriteFile(path, []byte("\x00\x01\x02\x03"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(path); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestReadText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	want := "hello world"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ReadText(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != want {
		t.Fatalf("text = %q, want %q", res.Text, want)
	}
	if res.Engine != "text" {
		t.Fatalf("engine = %q, want text", res.Engine)
	}
}

func TestReadTextKind_PreservesCSVKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transactions.csv")
	want := "date,amount\n2026-07-02,12.34\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ReadTextKind(nil, path, KindCSV)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != want {
		t.Fatalf("text = %q, want %q", res.Text, want)
	}
	if res.MIME != string(KindCSV) {
		t.Fatalf("MIME = %q, want %q", res.MIME, KindCSV)
	}
	if res.Engine != "text" {
		t.Fatalf("engine = %q, want text", res.Engine)
	}
}

func TestIsExplicitlyUnsupported(t *testing.T) {
	if IsExplicitlyUnsupported(KindPDF) {
		t.Error("IsExplicitlyUnsupported(KindPDF) = true, want false")
	}
	if IsExplicitlyUnsupported(KindText) {
		t.Error("IsExplicitlyUnsupported(KindText) = true, want false")
	}
}

func TestUnsupportedFormatError(t *testing.T) {
	err := UnsupportedFormatError(KindPDF)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if got := err.Error(); got == "" {
		t.Error("error message is empty")
	}
}
