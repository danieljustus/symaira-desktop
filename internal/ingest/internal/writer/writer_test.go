package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteNote(t *testing.T) {
	vault := t.TempDir()
	source := "/tmp/scans/invoice.pdf"
	w := &NoteWriter{Vault: vault}
	path, err := w.WriteNote(source, "deadbeef", "application/pdf", "tesseract", "hello", "", time.Unix(0, 0).UTC(), "invoice", []string{"financial"}, "Acme Corp", "Invoice", "", "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	want := filepath.Join(vault, "invoice.pdf.md")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fileMode(t, path); mode != 0o600 {
		t.Fatalf("note mode = %o, want 0600", mode)
	}
	out := string(data)
	if !strings.HasPrefix(out, "---\n") {
		t.Fatal("missing frontmatter start")
	}
	for _, needle := range []string{"source_path:", "sha256: deadbeef", "ocr_engine: tesseract", "category: invoice", "tags:", "- financial", "correspondent: Acme Corp", "document_type: Invoice", "hello"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("output missing %q", needle)
		}
	}
}

func TestWriteNote_PaperlessMeta(t *testing.T) {
	vault := t.TempDir()
	source := "/tmp/scans/migrated-invoice.pdf"
	w := &NoteWriter{Vault: vault}

	created := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	added := time.Date(2024, 3, 2, 10, 0, 0, 0, time.UTC)
	modified := time.Date(2024, 3, 5, 11, 30, 0, 0, time.UTC)

	pm := &PaperlessMeta{
		DocumentID:       42,
		Title:            "Migrated Invoice",
		Created:          created,
		Added:            added,
		Modified:         modified,
		StoragePath:      "Invoices/2024",
		OriginalFileName: "invoice-original.pdf",
		ArchivedFileName: "invoice-archived.pdf",
		PageCount:        3,
		URL:              "https://paperless.local/documents/42",
	}

	path, err := w.WriteNote(source, "deadbeef", "application/pdf", "tesseract", "hello", "", time.Unix(0, 0).UTC(), "invoice", []string{"financial"}, "Acme Corp", "Invoice", "", "", "", "", pm, nil)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	for _, needle := range []string{
		"paperless:",
		"document_id: 42",
		"title: Migrated Invoice",
		"created: 2024-03-01T09:00:00Z",
		"added: 2024-03-02T10:00:00Z",
		"modified: 2024-03-05T11:30:00Z",
		"storage_path: Invoices/2024",
		"original_file_name: invoice-original.pdf",
		"archived_file_name: invoice-archived.pdf",
		"page_count: 3",
		"url: https://paperless.local/documents/42",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("output missing %q\n%s", needle, out)
		}
	}
}

func TestWriteNote_PaperlessTraceabilityFields(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}
	path, err := w.WriteNote(
		"paperless://documents/42/download",
		"deadbeef",
		"application/pdf",
		"tesseract",
		"hello",
		"",
		time.Unix(0, 0).UTC(),
		"",
		nil,
		"",
		"",
		"paperless",
		"paperless-20260703T100000Z",
		"paperless://documents/42",
		"paperless://documents/42/download",
		&PaperlessMeta{DocumentID: 42},
		&NoteLayout{BaseName: "doc-42"},
	)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, needle := range []string{
		"source_path: paperless://documents/42/download",
		"imported_from: paperless",
		"import_run_id: paperless-20260703T100000Z",
		"source_uri: paperless://documents/42",
		"download_uri: paperless://documents/42/download",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("output missing %q:\n%s", needle, out)
		}
	}
}

func TestWriteNote_NoPaperlessMeta_OmitsBlock(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	path, err := w.WriteNote("/tmp/plain.txt", "abc", "text/plain", "", "body", "", time.Now().UTC(), "", nil, "", "", "", "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "paperless:") {
		t.Fatalf("expected no paperless block for a non-migrated note:\n%s", string(data))
	}
}

func TestWriteNote_Atomic(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}
	path, err := w.WriteNote("/tmp/a.txt", "abc", "text/plain", "", "body", "", time.Now().UTC(), "", nil, "", "", "", "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("note not written: %v", err)
	}
	matches, err := filepath.Glob(vault + "/a.txt.tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestWriteNote_Golden(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	fixedTime := time.Date(2026, 6, 26, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		sourcePath string
		sha256     string
		mime       string
		ocrEngine  string
		text       string
	}{
		{
			name:       "text",
			sourcePath: "/tmp/invoice.txt",
			sha256:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			mime:       "text/plain",
			ocrEngine:  "",
			text:       "Hello World",
		},
		{
			name:       "image",
			sourcePath: "/tmp/invoice.png",
			sha256:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			mime:       "image/png",
			ocrEngine:  "tesseract",
			text:       "Extracted text from image",
		},
		{
			name:       "pdf",
			sourcePath: "/tmp/invoice.pdf",
			sha256:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			mime:       "application/pdf",
			ocrEngine:  "pdftoppm+tesseract",
			text:       "Extracted text from PDF",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := w.WriteNote(tc.sourcePath, tc.sha256, tc.mime, tc.ocrEngine, tc.text, "", fixedTime, "", nil, "", "", "", "", "", "", nil, nil)
			if err != nil {
				t.Fatalf("WriteNote failed: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read generated note: %v", err)
			}

			goldenPath := filepath.Join("testdata", tc.name+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "true" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("failed to create testdata dir: %v", err)
				}
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("failed to write golden file: %v", err)
				}
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("failed to read golden file (run UPDATE_GOLDEN=true go test ./internal/writer to generate): %v", err)
			}

			if string(got) != string(want) {
				t.Errorf("generated note does not match golden file %s\nGOT:\n%s\nWANT:\n%s", goldenPath, string(got), string(want))
			}
		})
	}
}

func TestSanitizeStoragePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nested", "Finance/Invoices", filepath.Join("Finance", "Invoices")},
		{"backslashes", `Steuern\2024`, filepath.Join("Steuern", "2024")},
		{"traversal dropped", "../../etc/passwd", filepath.Join("etc", "passwd")},
		{"leading slash", "/abs/path", filepath.Join("abs", "path")},
		{"dot segments dropped", "a/./b/../c", filepath.Join("a", "b", "c")},
		{"reserved chars replaced", `a:b*c?/d`, filepath.Join("a_b_c_", "d")},
		{"empty", "", ""},
		{"only separators", "///", ""},
		{"trailing dots", "report./final.", filepath.Join("report", "final")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeStoragePath(tc.in); got != tc.want {
				t.Errorf("SanitizeStoragePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeStoragePath_NeverEscapesVault(t *testing.T) {
	vault := t.TempDir()
	for _, in := range []string{"../../../etc", "/etc/passwd", `..\..\windows`, "a/../../b"} {
		sub := SanitizeStoragePath(in)
		full := filepath.Join(vault, sub)
		rel, err := filepath.Rel(vault, full)
		if err != nil {
			t.Fatalf("Rel: %v", err)
		}
		if strings.HasPrefix(rel, "..") {
			t.Errorf("SanitizeStoragePath(%q) escaped the vault: rel=%q", in, rel)
		}
	}
}

func TestWriteNote_LayoutSubdirAndBaseName(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}
	layout := &NoteLayout{Subdir: filepath.Join("Finance", "Invoices"), BaseName: "acme-invoice"}

	path, err := w.WriteNote("/tmp/symingest-import-xyz.tmp.pdf", "abc", "application/pdf", "tesseract", "body", "", time.Now().UTC(), "", nil, "", "", "", "", "", "", nil, layout)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	want := filepath.Join(vault, "Finance", "Invoices", "acme-invoice.md")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("note not written at expected layout path: %v", err)
	}
	if mode := fileMode(t, want); mode != 0o600 {
		t.Fatalf("note mode = %o, want 0600", mode)
	}
	if mode := fileMode(t, filepath.Dir(want)); mode != 0o700 {
		t.Fatalf("layout directory mode = %o, want 0700", mode)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestWriteNote_LayoutCollisionDeterministic(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}
	layout := &NoteLayout{Subdir: "Docs", BaseName: "scan"}

	first, err := w.WriteNote("/tmp/a.pdf", "h1", "application/pdf", "", "one", "", time.Now().UTC(), "", nil, "", "", "", "", "", "", nil, layout)
	if err != nil {
		t.Fatalf("WriteNote first: %v", err)
	}
	second, err := w.WriteNote("/tmp/b.pdf", "h2", "application/pdf", "", "two", "", time.Now().UTC(), "", nil, "", "", "", "", "", "", nil, layout)
	if err != nil {
		t.Fatalf("WriteNote second: %v", err)
	}

	if first != filepath.Join(vault, "Docs", "scan.md") {
		t.Errorf("first = %q, want .../Docs/scan.md", first)
	}
	if second != filepath.Join(vault, "Docs", "scan-2.md") {
		t.Errorf("second = %q, want .../Docs/scan-2.md (deterministic collision suffix)", second)
	}
	if first == second {
		t.Fatal("colliding notes must not share a path")
	}
}

func TestWriteNote_NilLayoutStaysFlat(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}
	path, err := w.WriteNote("/tmp/scans/invoice.pdf", "h", "application/pdf", "", "body", "", time.Now().UTC(), "", nil, "", "", "", "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if path != filepath.Join(vault, "invoice.pdf.md") {
		t.Errorf("path = %q, want flat .../invoice.pdf.md", path)
	}
}

func TestUpdateNoteSidecar(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	path, err := w.WriteNote("/tmp/scans/invoice.pdf", "sidecar-hash", "application/pdf", "tesseract", "Invoice body here", "/archive/sidecar-hash.pdf", time.Unix(0, 0).UTC(), "invoice", []string{"financial"}, "", "", "", "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	sidecarPath := filepath.Join(vault, ".symaira", "extractions", "sidecar-hash.jsonl")
	if err := w.UpdateNoteSidecar(path, sidecarPath, 4); err != nil {
		t.Fatalf("UpdateNoteSidecar: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	out := string(data)

	if !strings.Contains(out, "sidecar_path:") {
		t.Fatal("output missing sidecar_path")
	}
	if !strings.Contains(out, sidecarPath) {
		t.Fatalf("output missing sidecar path %q", sidecarPath)
	}
	if !strings.Contains(out, "extraction_count: 4") {
		t.Fatal("output missing extraction_count: 4")
	}
	if !strings.Contains(out, "Invoice body here") {
		t.Fatal("body was lost during sidecar update")
	}
}

func TestUpdateNoteSidecar_MissingFrontmatter(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	path := filepath.Join(vault, "no-frontmatter.md")
	if err := os.WriteFile(path, []byte("just body\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := w.UpdateNoteSidecar(path, "/sidecar.jsonl", 1)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestUpdateNoteSidecar_NonexistentFile(t *testing.T) {
	vault := t.TempDir()
	w := &NoteWriter{Vault: vault}

	err := w.UpdateNoteSidecar(filepath.Join(vault, "nonexistent.md"), "/sidecar.jsonl", 1)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
