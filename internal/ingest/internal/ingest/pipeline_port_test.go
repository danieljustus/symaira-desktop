package ingest

// Go oracle for the whole one-shot pipeline (RUST-010, INGEST-002/-003 wiring):
// Detect -> hash -> store -> queue -> extraction routing -> archive -> classify
// -> note writer -> annotation sidecar -> store paths. Every scenario runs the
// real Pipeline over real files and records, per step, the Result or error
// text and the engine/hook/log activity, and at the end the complete vault and
// archive trees (bytes and modes) plus the documents, jobs and extractions
// tables. Timestamps are normalized; fake OCR tools are stored in the fixture.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/annotate"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/ocr"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/writer"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const pipelineTesseract = `#!/bin/sh
PATH=/usr/bin:/bin
d=${0%/*}
US=$(printf '\037'); l=T; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l" >> "$d/log"
if [ "$1" = "--list-langs" ]; then printf 'List of available languages (3):\neng\ndeu\nfra\n'; exit 0; fi
printf 'TEXT[%s:%s]' "$2" "$(basename "$3")"
exit 0
`

type pipeFile struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}

type pipePaperless struct {
	DocumentID       int    `json:"document_id"`
	Title            string `json:"title"`
	Created          string `json:"created"`
	Added            string `json:"added"`
	Modified         string `json:"modified"`
	StoragePath      string `json:"storage_path"`
	OriginalFileName string `json:"original_file_name"`
	ArchivedFileName string `json:"archived_file_name"`
	PageCount        int    `json:"page_count"`
	URL              string `json:"url"`
}

type pipeOpts struct {
	PresetCategory        string         `json:"preset_category,omitempty"`
	PresetTags            []string       `json:"preset_tags,omitempty"`
	PresetCorrespondent   string         `json:"preset_correspondent,omitempty"`
	PresetDocumentType    string         `json:"preset_document_type,omitempty"`
	SourcePathOverride    string         `json:"source_path_override,omitempty"`
	ImportedFrom          string         `json:"imported_from,omitempty"`
	ImportRunID           string         `json:"import_run_id,omitempty"`
	SourceURI             string         `json:"source_uri,omitempty"`
	DownloadURI           string         `json:"download_uri,omitempty"`
	Paperless             *pipePaperless `json:"paperless,omitempty"`
	LayoutSubdir          *string        `json:"layout_subdir,omitempty"`
	LayoutBaseName        string         `json:"layout_base_name,omitempty"`
	AllowDuplicateContent bool           `json:"allow_duplicate_content,omitempty"`
	ExistingVaultPath     string         `json:"existing_vault_path,omitempty"`
	ArchivePathOverride   string         `json:"archive_path_override,omitempty"`
	OCRLanguage           string         `json:"ocr_language,omitempty"`
}

type pipeStep struct {
	// Op: ingest, reprocess, write, remove, mkdir, chmod-file, enqueue-reocr, claim-reocr.
	Op      string    `json:"op"`
	Source  string    `json:"source,omitempty"`
	DocID   int64     `json:"doc_id,omitempty"`
	Opts    *pipeOpts `json:"opts,omitempty"`
	Content []byte    `json:"content,omitempty"`
	// Reprocess passes an explicit source path when non-empty (relative to the scenario root).
	ReprocessSource string `json:"reprocess_source,omitempty"`
}

type pipeScenario struct {
	ID string `json:"id"`
	// Engine: "", "fake", "fake-error", "fake-empty", "runner".
	Engine string `json:"engine"`
	// Archive: "", "dir" (<root>/archive), "in-vault" (<root>/vault/archive/ingest),
	// "file" (the archive path is a regular file).
	Archive string `json:"archive"`
	// VaultRoot: "", "vault" (Pipeline.VaultRoot == writer vault), "other".
	VaultRoot string `json:"vault_root"`
	// VaultIsFile makes the writer vault a regular file.
	VaultIsFile bool `json:"vault_is_file,omitempty"`
	// NoWriter leaves Pipeline.Writer nil-equivalent by pointing it at a fresh vault; unused.
	Profile     string      `json:"profile,omitempty"`
	Rules       [][3]string `json:"rules,omitempty"`
	PostIndex   string      `json:"post_index,omitempty"`
	PostExtract string      `json:"post_extract,omitempty"`
	// SidecarBlocked makes <vault>/.symaira a regular file.
	SidecarBlocked bool       `json:"sidecar_blocked,omitempty"`
	Files          []pipeFile `json:"files"`
	Steps          []pipeStep `json:"steps"`
}

type pipeResult struct {
	SourcePath      string   `json:"source_path"`
	SHA256          string   `json:"sha256"`
	Kind            string   `json:"kind"`
	Text            string   `json:"text"`
	MIME            string   `json:"mime"`
	Engine          string   `json:"engine"`
	Language        string   `json:"language"`
	VaultPath       string   `json:"vault_path"`
	ArchivePath     string   `json:"archive_path"`
	Category        string   `json:"category"`
	Tags            []string `json:"tags"`
	Correspondent   string   `json:"correspondent"`
	DocumentType    string   `json:"document_type"`
	SidecarPath     string   `json:"sidecar_path"`
	ExtractionCount int      `json:"extraction_count"`
}

type pipeStepOut struct {
	Op        string      `json:"op"`
	Result    *pipeResult `json:"result"`
	Error     string      `json:"error"`
	Duplicate *struct {
		SourcePath  string `json:"source_path"`
		VaultPath   string `json:"vault_path"`
		ArchivePath string `json:"archive_path"`
		VaultRoot   string `json:"vault_root"`
	} `json:"duplicate"`
	IsDuplicate    bool            `json:"is_duplicate"`
	NoArchived     bool            `json:"no_archived_original"`
	AlreadyRunning *bool           `json:"already_running,omitempty"`
	Job            json.RawMessage `json:"job,omitempty"`
	EngineCalls    []string        `json:"engine_calls"`
	Hooks          []string        `json:"hooks"`
	Logs           []string        `json:"logs"`
}

type fsEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Text string `json:"text,omitempty"`
	B64  string `json:"b64,omitempty"`
}

type pipeCase struct {
	Scenario    pipeScenario    `json:"scenario"`
	Steps       []pipeStepOut   `json:"steps"`
	Tree        []fsEntry       `json:"tree"`
	Documents   json.RawMessage `json:"documents"`
	Jobs        json.RawMessage `json:"jobs"`
	Extractions json.RawMessage `json:"extractions"`
	Transcript  []string        `json:"transcript"`
}

type portFakeEngine struct {
	mode  string
	calls *[]string
	norm  func(string) string
}

func (f *portFakeEngine) Extract(_ context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	*f.calls = append(*f.calls, string(kind)+"|"+f.norm(path))
	switch f.mode {
	case "fake-error":
		return nil, errors.New("engine boom")
	case "fake-empty":
		return &extract.Result{Text: "", MIME: string(kind), Engine: "fake-ocr"}, nil
	}
	return &extract.Result{Text: "OCR text for " + filepath.Base(path) + " (" + string(kind) + ")\nSecond line 2024-05-06 mail@example.com", MIME: string(kind), Engine: "fake-ocr", Language: "eng"}, nil
}

var nowStamp = regexp.MustCompile(`(?m)^ingested_at: \S+$`)

func zipBytes(t *testing.T, parts [][2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, p := range parts {
		w, err := z.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pipelineFixtures(t *testing.T) []pipeFile {
	docx := zipBytes(t, [][2]string{{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`}, {"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`}, {"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hello docx rechnung 42</w:t></w:r></w:p></w:body></w:document>`}})
	xlsx := zipBytes(t, [][2]string{{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`}, {"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`}, {"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>`}, {"xl/sharedStrings.xml", `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>cell one</t></si></sst>`}, {"xl/worksheets/sheet1.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c t="s"><v>0</v></c><c><v>7</v></c></row></sheetData></worksheet>`}})
	pptx := zipBytes(t, [][2]string{{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`}, {"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/></Relationships>`}, {"ppt/presentation.xml", `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`}, {"ppt/slides/slide1.xml", `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Slide text</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`}})
	odf := func(mime string) []byte {
		return zipBytes(t, [][2]string{{"mimetype", mime}, {"content.xml", `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"><office:body><office:text><text:p>Open document text</text:p></office:text></office:body></office:document-content>`}})
	}
	epub := zipBytes(t, [][2]string{{"mimetype", "application/epub+zip"}, {"OEBPS/ch1.xhtml", `<html><body><p>Chapter one</p></body></html>`}})
	eml := "From: a@example.com\r\nTo: b@example.com\r\nSubject: Rechnung 7\r\nDate: Mon, 02 Jan 2006 15:04:05 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBody of the mail\r\n"
	return []pipeFile{
		{Name: "a.txt", Content: []byte("Invoice for ACME\nTotal 12.50 EUR on 2024-01-31\n")},
		{Name: "b.md", Content: []byte("# Title\n\nrechnung nr 5\n")},
		{Name: "c.csv", Content: []byte("a,b\n1,2\n")},
		{Name: "same-as-a.txt", Content: []byte("Invoice for ACME\nTotal 12.50 EUR on 2024-01-31\n")},
		{Name: "empty.txt", Content: nil},
		{Name: "crlf.txt", Content: []byte("line one\r\nline two\r\n")},
		{Name: "no-newline.txt", Content: []byte("no trailing newline")},
		{Name: "invalid.txt", Content: []byte("ok \xff\xfe bytes\n")},
		{Name: "Rechnung März.txt", Content: []byte("Rechnung Nr. 12 für März\n")},
		{Name: "q: a#b'\"c.txt", Content: []byte("special name\n")},
		{Name: "page.html", Content: []byte("<html><body><h1>Head</h1><p>Para &amp; more</p></body></html>")},
		{Name: "doc.rtf", Content: []byte(`{\rtf1\ansi Hello \b rtf\b0 world.}`)},
		{Name: "doc.docx", Content: docx},
		{Name: "sheet.xlsx", Content: xlsx},
		{Name: "deck.pptx", Content: pptx},
		{Name: "doc.odt", Content: odf("application/vnd.oasis.opendocument.text")},
		{Name: "book.epub", Content: epub},
		{Name: "mail.eml", Content: []byte(eml)},
		{Name: "bad.docx", Content: zipBytes(t, [][2]string{{"word/other.xml", "<x/>"}})},
		{Name: "scan.png", Content: []byte("\x89PNG\r\n\x1a\nIHDR-bytes-a")},
		{Name: "photo.jpg", Content: []byte("\xff\xd8\xff\xe0JFIF-bytes")},
		{Name: "scan.tiff", Content: []byte("II*\x00tiff-bytes")},
		{Name: "pic.webp", Content: []byte("RIFF\x00\x00\x00\x00WEBPvp8")},
		{Name: "photo.heic", Content: []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heic")},
		{Name: "file.pdf", Content: []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")},
		{Name: "book.mobi", Content: []byte("BOOKMOBI-bytes")},
		{Name: "legacy.doc", Content: []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")},
		{Name: "unknown.zzz", Content: []byte("data")},
		{Name: "invoice-profile.txt", Content: []byte("Rechnung Nr. 4711\nInvoice INV-2024-9 total EUR 1234,50 due date: 31.03.2024\nIBAN DE89370400440532013000 mail billing@acme.example https://acme.example/pay\nAcme Beratung GmbH\nCall +49 30 1234 5678\nCase #998 Aktenzeichen 5555 Frist: 01.04.2024 Termin: 02.04.2024\nBetween: Alpha Corp\nContractor: Beta Ltd.\n")},
		{Name: "unicode-profile.txt", Content: []byte("Größe 12.03.2024 – schön € 5,00 ünïcode text und mehr dazu 2024-12-31 ")},
		{Name: "long-snippet.txt", Content: []byte(strings.Repeat("filler word ", 30) + "2024-02-29 " + strings.Repeat("tail word ", 30))},
	}
}

func pipelineScenarios(t *testing.T) []pipeScenario {
	files := pipelineFixtures(t)
	lit := func(s string) *string { return &s }
	ing := func(src string) pipeStep { return pipeStep{Op: "ingest", Source: src} }
	ingOpts := func(src string, o pipeOpts) pipeStep { return pipeStep{Op: "ingest", Source: src, Opts: &o} }
	rules := [][3]string{{"invoice", "category", "Finance"}, {"rechnung", "tag", "de"}, {"acme", "correspondent", "ACME GmbH"}, {"contract", "document_type", "Contract"}}
	sc := []pipeScenario{}
	add := func(s pipeScenario) {
		s.Files = files
		sc = append(sc, s)
	}
	// Plain text kinds, flat layout, no archive.
	add(pipeScenario{ID: "text-no-archive", Steps: []pipeStep{ing("a.txt"), ing("b.md"), ing("c.csv"), ing("empty.txt"), ing("crlf.txt"), ing("no-newline.txt")}})
	add(pipeScenario{ID: "text-archive-dir", Archive: "dir", Rules: rules, Steps: []pipeStep{ing("a.txt"), ing("b.md"), ing("c.csv")}})
	add(pipeScenario{ID: "archive-in-vault-relative", Archive: "in-vault", VaultRoot: "vault", Rules: rules, Steps: []pipeStep{ing("a.txt"), ing("b.md")}})
	add(pipeScenario{ID: "archive-in-vault-without-vault-root", Archive: "in-vault", Steps: []pipeStep{ing("a.txt")}})
	add(pipeScenario{ID: "archive-outside-vault-absolute", Archive: "dir", VaultRoot: "vault", Steps: []pipeStep{ing("a.txt")}})
	add(pipeScenario{ID: "archive-other-vault-root", Archive: "dir", VaultRoot: "other", Steps: []pipeStep{ing("a.txt")}})
	add(pipeScenario{ID: "invalid-utf8-text", Archive: "dir", Rules: rules, Steps: []pipeStep{ing("invalid.txt")}})
	add(pipeScenario{ID: "unicode-and-special-names", Archive: "dir", Steps: []pipeStep{ing("Rechnung März.txt"), ing("q: a#b'\"c.txt")}})

	// Duplicates.
	add(pipeScenario{ID: "duplicate-then-allow", Archive: "dir", Steps: []pipeStep{ing("a.txt"), ing("a.txt"), ing("same-as-a.txt"), ingOpts("same-as-a.txt", pipeOpts{AllowDuplicateContent: true}), ingOpts("a.txt", pipeOpts{AllowDuplicateContent: true})}})
	add(pipeScenario{ID: "duplicate-with-vault-root", Archive: "dir", VaultRoot: "vault", Steps: []pipeStep{ing("a.txt"), ing("a.txt")}})

	// Structured kinds.
	add(pipeScenario{ID: "structured-kinds", Engine: "fake", Archive: "dir", Rules: rules, Steps: []pipeStep{ing("page.html"), ing("doc.rtf"), ing("doc.docx"), ing("sheet.xlsx"), ing("deck.pptx"), ing("doc.odt"), ing("book.epub"), ing("mail.eml"), ing("bad.docx")}})
	// Image and PDF kinds through the injected engine.
	add(pipeScenario{ID: "engine-ok", Engine: "fake", Archive: "dir", Rules: rules, Steps: []pipeStep{ing("scan.png"), ing("photo.jpg"), ing("scan.tiff"), ing("pic.webp"), ing("photo.heic"), ing("file.pdf")}})
	add(pipeScenario{ID: "engine-error-and-retry", Engine: "fake-error", Archive: "dir", Steps: []pipeStep{ing("file.pdf"), ing("file.pdf")}})
	add(pipeScenario{ID: "engine-empty-text", Engine: "fake-empty", Archive: "dir", Steps: []pipeStep{ing("scan.png")}})
	add(pipeScenario{ID: "engine-missing", Steps: []pipeStep{ing("scan.png"), ing("file.pdf"), ing("a.txt"), ing("doc.docx")}})
	add(pipeScenario{ID: "unsupported-kinds-with-engine", Engine: "fake", Steps: []pipeStep{ing("book.mobi"), ing("legacy.doc")}})
	add(pipeScenario{ID: "unsupported-kinds-without-engine", Steps: []pipeStep{ing("book.mobi"), ing("legacy.doc")}})
	add(pipeScenario{ID: "source-errors", Steps: []pipeStep{ing("missing.txt"), ing("sub"), ing("unknown.zzz"), {Op: "mkdir", Source: "sub"}, ing("sub")}})

	// Metadata passthrough, layouts, Paperless.
	opts := pipeOpts{PresetCategory: "Preset: cat", PresetTags: []string{"t1", "yes", "123", "a: b", "- x", "#c"}, PresetCorrespondent: "Müller & Söhne", PresetDocumentType: "Letter", SourcePathOverride: "/original/path with space/a.txt", ImportedFrom: "paperless", ImportRunID: "run-1", SourceURI: "https://example.invalid/doc/1?a=b&c=d", DownloadURI: "https://example.invalid/dl/1"}
	add(pipeScenario{ID: "options-passthrough", Archive: "dir", Rules: rules, Steps: []pipeStep{ingOpts("a.txt", opts)}})
	pl := pipePaperless{DocumentID: 17, Title: "Title: with colon", Created: "2024-03-04T05:06:07Z", Added: "2024-03-05T00:00:00.123456789Z", Modified: "2024-03-06T10:11:12+02:00", StoragePath: "Finance/2024", OriginalFileName: "orig.pdf", ArchivedFileName: "arch.pdf", PageCount: 3, URL: "https://paperless.invalid/documents/17/"}
	add(pipeScenario{ID: "paperless-meta-full", Archive: "dir", Steps: []pipeStep{ingOpts("a.txt", pipeOpts{Paperless: &pl, ImportedFrom: "paperless"})}})
	add(pipeScenario{ID: "paperless-meta-sparse", Archive: "dir", Steps: []pipeStep{ingOpts("b.md", pipeOpts{Paperless: &pipePaperless{DocumentID: 3}})}})
	add(pipeScenario{ID: "layout-subdir-and-collisions", Archive: "dir", Steps: []pipeStep{
		ingOpts("a.txt", pipeOpts{LayoutSubdir: lit("Finance/2024"), LayoutBaseName: "Invoice"}),
		ingOpts("same-as-a.txt", pipeOpts{LayoutSubdir: lit("Finance/2024"), LayoutBaseName: "Invoice", AllowDuplicateContent: true}),
		ingOpts("same-as-a.txt", pipeOpts{LayoutSubdir: lit("Finance/2024"), LayoutBaseName: "Invoice", AllowDuplicateContent: true}),
		ingOpts("b.md", pipeOpts{LayoutSubdir: lit(""), LayoutBaseName: ""}),
		ingOpts("c.csv", pipeOpts{LayoutSubdir: lit("x/y"), LayoutBaseName: "..."}),
		ingOpts("crlf.txt", pipeOpts{LayoutSubdir: lit("../escape"), LayoutBaseName: "a/b"}),
		ingOpts("no-newline.txt", pipeOpts{LayoutSubdir: lit("Ä Ö/ü"), LayoutBaseName: " <q>?:name* "}),
	}})

	// Annotation profiles and hooks.
	for _, p := range []string{"generic", "invoice", "contract", "jobcenter"} {
		add(pipeScenario{ID: "profile-" + p, Archive: "dir", Profile: p, PostExtract: "ok", Steps: []pipeStep{ing("invoice-profile.txt"), ing("unicode-profile.txt"), ing("long-snippet.txt"), ing("empty.txt")}})
	}
	add(pipeScenario{ID: "profile-unknown", Profile: "nope", Steps: []pipeStep{ing("invoice-profile.txt")}})
	add(pipeScenario{ID: "profile-no-matches", Profile: "invoice", Steps: []pipeStep{ing("c.csv")}})
	add(pipeScenario{ID: "profile-sidecar-blocked", Profile: "generic", SidecarBlocked: true, PostExtract: "ok", Steps: []pipeStep{ing("invoice-profile.txt")}})
	add(pipeScenario{ID: "profile-post-extract-error", Profile: "generic", PostExtract: "error", Steps: []pipeStep{ing("invoice-profile.txt")}})
	add(pipeScenario{ID: "profile-with-engine", Engine: "fake", Profile: "generic", Archive: "dir", Steps: []pipeStep{ing("scan.png")}})
	add(pipeScenario{ID: "post-index-ok", PostIndex: "ok", Steps: []pipeStep{ing("a.txt"), ing("a.txt")}})
	add(pipeScenario{ID: "post-index-error", PostIndex: "error", Steps: []pipeStep{ing("a.txt")}})

	// Writer and archive failures.
	add(pipeScenario{ID: "vault-is-a-file", VaultIsFile: true, Steps: []pipeStep{ing("a.txt"), ing("a.txt")}})
	add(pipeScenario{ID: "archive-is-a-file", Archive: "file", Steps: []pipeStep{ing("a.txt")}})
	add(pipeScenario{ID: "very-long-source-name", Steps: []pipeStep{{Op: "write", Source: strings.Repeat("n", 251) + ".txt", Content: []byte("long name\n")}, ing(strings.Repeat("n", 251) + ".txt")}})

	// Reprocess.
	rp := func(id int64) pipeStep { return pipeStep{Op: "reprocess", DocID: id} }
	add(pipeScenario{ID: "reprocess-engine", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Rules: rules, Profile: "generic", Steps: []pipeStep{
		ing("file.pdf"),
		{Op: "append-note", Source: "file.pdf", Content: []byte("reviewed: true\ncustom_list:\n    - a\n    - b\ncount: 3\ntitle: My own title\n")},
		{Op: "set-lang", Content: []byte("deu")},
		rp(1),
		rp(1),
		rp(99),
		{Op: "reprocess", DocID: 1, ReprocessSource: "file.pdf"},
	}})
	userFields := []struct{ id, yaml string }{
		{"date", "date: 2024-05-01\n"},
		{"datetime", "when: 2024-05-01T10:00:00+02:00\nwhen2: 2024-05-01 10:00:00\n"},
		{"quoted-date", "quoted: \"2024-05-01\"\nsingle: '2024-05-01T10:00:00Z'\n"},
		{"numbers", "n: 1.50\ni: 007\nh: 0x1F\nb: yes\nnul: ~\nempty:\n"},
		{"multiline", "multi: |\n    line a\n    line b\nfolded: >\n    one\n    two\n"},
		{"nested", "nested:\n    a: 1\n    b: [x, y]\n    c:\n        - k: v\n"},
		{"flow", "flow: {a: 1, b: [2, 3]}\nlist: [a, 'b c', \"d\"]\n"},
		{"unicode-keys", "\"ключ\": значение\n\"key with space\": 1\nz_last: end\nA_upper: 1\n10x: ten\n2x: two\n"},
		{"strings-needing-quotes", "colon: \"a: b\"\nhash: \"# c\"\nlead: \" lead\"\ntrail: \"trail \"\nq: \"it's\"\nbs: \"a\\\\b\"\n"},
		{"anchors", "base: &b {x: 1}\ncopy: *b\n"},
		{"tagged-and-binary", "bin: !!binary aGVsbG8=\nstr: !!str 123\n"},
		{"duplicate-key", "dup: 1\ndup: 2\n"},
	}
	for _, uf := range userFields {
		add(pipeScenario{ID: "reprocess-user-fields-" + uf.id, Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "append-note", Source: "file.pdf", Content: []byte(uf.yaml)}, rp(1)}})
	}
	add(pipeScenario{ID: "reprocess-runner-language", Engine: "runner", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{
		ing("scan.png"),
		rp(1),
		{Op: "set-lang", Content: []byte("deu+eng")},
		rp(1),
		{Op: "set-lang", Content: []byte("\"  fra  \"")},
		rp(1),
		{Op: "set-lang", Content: []byte("")},
		rp(1),
	}})
	add(pipeScenario{ID: "reprocess-no-archive", Engine: "fake", Steps: []pipeStep{ing("file.pdf"), rp(1)}})
	add(pipeScenario{ID: "reprocess-hash-mismatch", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "corrupt-archive", DocID: 1}, rp(1)}})
	add(pipeScenario{ID: "reprocess-archive-missing", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "remove-archive", DocID: 1}, rp(1)}})
	add(pipeScenario{ID: "reprocess-engine-error", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "set-engine", Content: []byte("fake-error")}, rp(1), rp(1), {Op: "set-engine", Content: []byte("fake")}, rp(1)}})
	add(pipeScenario{ID: "reprocess-note-missing", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "remove-note", DocID: 1}, rp(1)}})
	add(pipeScenario{ID: "reprocess-note-without-frontmatter", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "replace-note", DocID: 1, Content: []byte("no frontmatter here\n")}, rp(1)}})
	add(pipeScenario{ID: "reprocess-note-unterminated", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "replace-note", DocID: 1, Content: []byte("---\nsource_path: x\n")}, rp(1)}})
	add(pipeScenario{ID: "reprocess-note-bad-yaml", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "replace-note", DocID: 1, Content: []byte("---\nkey: [unclosed\n---\n\nbody\n")}, rp(1)}})
	add(pipeScenario{ID: "reprocess-already-running", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "enqueue-reocr", DocID: 1}, rp(1)}})
	add(pipeScenario{ID: "reprocess-directory-source", Engine: "fake", Archive: "in-vault", VaultRoot: "vault", Steps: []pipeStep{ing("file.pdf"), {Op: "mkdir", Source: "adir"}, {Op: "reprocess", DocID: 1, ReprocessSource: "adir"}}})
	return sc
}

func snapshotTree(t *testing.T, root string, rels []string, norm func(string) string) []fsEntry {
	out := []fsEntry{}
	for _, rel := range rels {
		base := filepath.Join(root, rel)
		if _, err := os.Lstat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			r, _ := filepath.Rel(root, path)
			e := fsEntry{Path: filepath.ToSlash(r), Mode: fmt.Sprintf("%04o", info.Mode().Perm())}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				e.Type = "symlink"
			case d.IsDir():
				e.Type = "dir"
			default:
				e.Type = "file"
				b, err := os.ReadFile(path) //nolint:gosec // test scratch
				if err != nil {
					return err
				}
				if utf8.Valid(b) && !bytes.Contains(b, []byte{0}) {
					e.Text = nowStamp.ReplaceAllString(norm(string(b)), "ingested_at: <NOW>")
				} else {
					b = bytes.ReplaceAll(b, []byte(root), []byte("<ROOT>"))
					b = regexp.MustCompile(`(?m)^ingested_at: \S+$`).ReplaceAll(b, []byte("ingested_at: <NOW>"))
					e.B64 = base64.StdEncoding.EncodeToString(b)
				}
			}
			out = append(out, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func normJSON(t *testing.T, v any, norm func(string) string) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s := norm(string(b))
	s = regexp.MustCompile(`"(created_at|updated_at)":"[^"]*"`).ReplaceAllString(s, `"$1":"<TS>"`)
	return json.RawMessage(s)
}

func runPipelineScenario(t *testing.T, sc pipeScenario) pipeCase {
	root := t.TempDir()
	for _, d := range []string{"src", "tools"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	vault := filepath.Join(root, "vault")
	tmpSuffix := regexp.MustCompile(`\.tmp-\d+`)
	norm := func(s string) string {
		return tmpSuffix.ReplaceAllString(strings.ReplaceAll(s, root, "<ROOT>"), ".tmp-N")
	}
	for _, f := range sc.Files {
		if err := os.WriteFile(filepath.Join(root, "src", f.Name), f.Content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "tesseract"), []byte(pipelineTesseract), 0o700); err != nil { //nolint:gosec // fake tool must be executable
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "tools", "tesseract"), 0o700); err != nil { //nolint:gosec // fake tool must be executable
		t.Fatal(err)
	}
	if sc.VaultIsFile {
		if err := os.WriteFile(vault, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if sc.SidecarBlocked {
		if err := os.WriteFile(filepath.Join(vault, ".symaira"), []byte("blocker"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, "db", "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, r := range sc.Rules {
		if _, err := st.AddRule(ctx, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	var engineCalls, hooks []string
	var logBuf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) }()

	p := &Pipeline{Store: st, Writer: &writer.NoteWriter{Vault: vault}, ExtractionProfile: sc.Profile}
	switch sc.Engine {
	case "fake", "fake-error", "fake-empty":
		p.Engine = &portFakeEngine{mode: sc.Engine, calls: &engineCalls, norm: norm}
	case "runner":
		p.Engine = &ocr.Runner{Tesseract: filepath.Join(root, "tools", "tesseract"), PDFToPPM: "pdftoppm-unused", SIPS: "sips-unused", OCRLang: "eng"}
	}
	switch sc.Archive {
	case "dir":
		p.ArchiveDir = filepath.Join(root, "archive")
	case "in-vault":
		p.ArchiveDir = filepath.Join(vault, "archive", "ingest")
	case "file":
		p.ArchiveDir = filepath.Join(root, "archive-file")
		if err := os.WriteFile(p.ArchiveDir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	switch sc.VaultRoot {
	case "vault":
		p.VaultRoot = vault
	case "other":
		p.VaultRoot = filepath.Join(root, "other-vault")
	}
	switch sc.PostIndex {
	case "ok":
		p.PostIndex = func(_ context.Context, path string) error { hooks = append(hooks, "index|"+norm(path)); return nil }
	case "error":
		p.PostIndex = func(_ context.Context, path string) error {
			hooks = append(hooks, "index|"+norm(path))
			return errors.New("index boom")
		}
	}
	switch sc.PostExtract {
	case "ok", "error":
		mode := sc.PostExtract
		p.PostExtract = func(_ context.Context, r *Result, ex []annotate.Extraction) error {
			hooks = append(hooks, fmt.Sprintf("extract|%s|%d|%d", filepath.Base(r.SourcePath), r.ExtractionCount, len(ex)))
			if mode == "error" {
				return errors.New("extract boom")
			}
			return nil
		}
	}

	toTime := func(s string) time.Time {
		if s == "" {
			return time.Time{}
		}
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	toOpts := func(o *pipeOpts) *IngestOptions {
		if o == nil {
			return nil
		}
		io := &IngestOptions{
			PresetCategory: o.PresetCategory, PresetTags: o.PresetTags, PresetCorrespondent: o.PresetCorrespondent, PresetDocumentType: o.PresetDocumentType,
			SourcePathOverride: o.SourcePathOverride, ImportedFrom: o.ImportedFrom, ImportRunID: o.ImportRunID, SourceURI: o.SourceURI, DownloadURI: o.DownloadURI,
			AllowDuplicateContent: o.AllowDuplicateContent, ExistingVaultPath: o.ExistingVaultPath, ArchivePathOverride: o.ArchivePathOverride, OCRLanguage: o.OCRLanguage,
		}
		if o.Paperless != nil {
			io.Paperless = &writer.PaperlessMeta{DocumentID: o.Paperless.DocumentID, Title: o.Paperless.Title, Created: toTime(o.Paperless.Created), Added: toTime(o.Paperless.Added), Modified: toTime(o.Paperless.Modified), StoragePath: o.Paperless.StoragePath, OriginalFileName: o.Paperless.OriginalFileName, ArchivedFileName: o.Paperless.ArchivedFileName, PageCount: o.Paperless.PageCount, URL: o.Paperless.URL}
		}
		if o.LayoutSubdir != nil {
			io.Layout = &writer.NoteLayout{Subdir: *o.LayoutSubdir, BaseName: o.LayoutBaseName}
		}
		return io
	}
	docPaths := func(id int64) (note, archive string) {
		d, err := st.ByID(ctx, id)
		if err != nil {
			return "", ""
		}
		if d.VaultPath != nil {
			note = *d.VaultPath
		}
		if d.ArchivePath != nil {
			archive = *d.ArchivePath
			if !filepath.IsAbs(archive) && p.VaultRoot != "" {
				archive = filepath.Join(p.VaultRoot, archive)
			}
		}
		return note, archive
	}

	c := pipeCase{Scenario: sc, Steps: []pipeStepOut{}}
	for _, step := range sc.Steps {
		engineCalls, hooks = nil, nil
		logBuf.Reset()
		out := pipeStepOut{Op: step.Op}
		src := filepath.Join(root, "src", step.Source)
		switch step.Op {
		case "ingest":
			res, err := p.Ingest(ctx, src, toOpts(step.Opts))
			if err != nil {
				out.Error = norm(err.Error())
				var de *DuplicateError
				if errors.As(err, &de) {
					out.Duplicate = &struct {
						SourcePath  string `json:"source_path"`
						VaultPath   string `json:"vault_path"`
						ArchivePath string `json:"archive_path"`
						VaultRoot   string `json:"vault_root"`
					}{norm(de.SourcePath), norm(de.VaultPath), norm(de.ArchivePath), norm(de.VaultRoot)}
				}
				out.IsDuplicate = errors.Is(err, ErrDuplicate)
			} else {
				out.Result = summarize(res, norm)
			}
		case "reprocess":
			source := ""
			if step.ReprocessSource != "" {
				source = filepath.Join(root, "src", step.ReprocessSource)
				if step.ReprocessSource == "adir" {
					source = filepath.Join(root, "src", "adir")
				}
			}
			rr, err := p.Reprocess(ctx, step.DocID, source, toOpts(step.Opts))
			if err != nil {
				out.Error = norm(err.Error())
				out.NoArchived = errors.Is(err, ErrNoArchivedOriginal)
			} else {
				running := rr.AlreadyRunning
				out.AlreadyRunning = &running
				if rr.Result != nil {
					out.Result = summarize(rr.Result, norm)
				}
				out.Job = normJSON(t, rr.Job, norm)
			}
		case "write":
			if err := os.WriteFile(src, step.Content, 0o600); err != nil {
				t.Fatal(err)
			}
		case "mkdir":
			if err := os.MkdirAll(src, 0o700); err != nil {
				t.Fatal(err)
			}
		case "enqueue-reocr":
			if _, _, err := st.EnqueueReprocessJob(ctx, step.DocID); err != nil {
				t.Fatal(err)
			}
		case "append-note":
			note, _ := docPaths(1)
			if note == "" {
				t.Fatal("append-note: no note")
			}
			b, err := os.ReadFile(note) //nolint:gosec // test scratch
			if err != nil {
				t.Fatal(err)
			}
			// Insert user-owned fields before the closing frontmatter delimiter.
			idx := bytes.Index(b[4:], []byte("\n---\n"))
			if idx < 0 {
				t.Fatal("append-note: no frontmatter")
			}
			idx += 4 + 1
			nb := append(append(append([]byte{}, b[:idx]...), step.Content...), b[idx:]...)
			if err := os.WriteFile(note, nb, 0o600); err != nil {
				t.Fatal(err)
			}
		case "set-engine":
			fe, ok := p.Engine.(*portFakeEngine)
			if !ok {
				t.Fatal("set-engine needs a fake engine")
			}
			fe.mode = string(step.Content)
		case "set-lang":
			note, _ := docPaths(1)
			b, err := os.ReadFile(note) //nolint:gosec // test scratch
			if err != nil {
				t.Fatal(err)
			}
			re := regexp.MustCompile(`(?m)^ocr_language:.*\n`)
			line := ""
			if len(step.Content) > 0 {
				line = "ocr_language: " + string(step.Content) + "\n"
			}
			var nb []byte
			if re.Match(b) {
				nb = re.ReplaceAll(b, []byte(line))
			} else {
				idx := bytes.Index(b[4:], []byte("\n---\n")) + 5
				nb = append(append(append([]byte{}, b[:idx]...), line...), b[idx:]...)
			}
			if err := os.WriteFile(note, nb, 0o600); err != nil {
				t.Fatal(err)
			}
		case "replace-note":
			note, _ := docPaths(step.DocID)
			if err := os.WriteFile(note, step.Content, 0o600); err != nil {
				t.Fatal(err)
			}
		case "remove-note":
			note, _ := docPaths(step.DocID)
			if err := os.Remove(note); err != nil {
				t.Fatal(err)
			}
		case "corrupt-archive":
			_, arch := docPaths(step.DocID)
			if err := os.WriteFile(arch, []byte("corrupted"), 0o600); err != nil {
				t.Fatal(err)
			}
		case "remove-archive":
			_, arch := docPaths(step.DocID)
			if err := os.Remove(arch); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown op %q", step.Op)
		}
		out.EngineCalls = append([]string{}, engineCalls...)
		out.Hooks = append([]string{}, hooks...)
		out.Logs = []string{}
		if s := logBuf.String(); s != "" {
			for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
				out.Logs = append(out.Logs, norm(line))
			}
		}
		c.Steps = append(c.Steps, out)
	}
	c.Tree = snapshotTree(t, root, []string{"vault", "archive", "archive-file"}, norm)
	docs, err := st.ListDocuments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Documents = normJSON(t, docs, norm)
	jobs, err := st.ListJobs(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	c.Jobs = normJSON(t, jobs, norm)
	exs := map[int64][]annotate.Extraction{}
	for _, d := range docs {
		ex, err := st.ListExtractions(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		exs[d.ID] = ex
	}
	c.Extractions = normJSON(t, exs, norm)
	c.Transcript = []string{}
	if b, err := os.ReadFile(filepath.Join(root, "tools", "log")); err == nil { //nolint:gosec // test scratch
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			c.Transcript = append(c.Transcript, norm(line))
		}
	}
	return c
}

func summarize(r *Result, norm func(string) string) *pipeResult {
	pr := &pipeResult{SourcePath: norm(r.SourcePath), SHA256: r.SHA256, Kind: string(r.Kind), VaultPath: norm(r.VaultPath), ArchivePath: norm(r.ArchivePath), Category: r.Category, Tags: r.Tags, Correspondent: r.Correspondent, DocumentType: r.DocumentType, SidecarPath: norm(r.SidecarPath), ExtractionCount: r.ExtractionCount}
	if r.Extract != nil {
		pr.Text, pr.MIME, pr.Engine, pr.Language = r.Extract.Text, r.Extract.MIME, r.Extract.Engine, r.Extract.Language
	}
	return pr
}

func TestPortIngestPipelineContract(t *testing.T) {
	cases := []pipeCase{}
	steps := 0
	for _, sc := range pipelineScenarios(t) {
		c := runPipelineScenario(t, sc)
		steps += len(c.Steps)
		cases = append(cases, c)
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"pipeline.go", "ingest.go", "pipeline_port_test.go", "../writer/writer.go", "../annotate/extract.go", "../annotate/profile.go", "../annotate/writer.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[strings.TrimPrefix(f, "../")] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"tesseract":      pipelineTesseract,
		"cases":          cases,
		"normalizations": []string{
			"scenario temp root replaced by <ROOT>",
			"ingested_at frontmatter value replaced by <NOW>; job created_at/updated_at by <TS>",
		},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/pipeline.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
	} else {
		old, e := os.ReadFile(out) //nolint:gosec // repository fixture
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest pipeline fixture drift; regenerate deliberately")
		}
	}
	t.Logf("froze %d pipeline scenarios (%d steps)", len(cases), steps)
}
