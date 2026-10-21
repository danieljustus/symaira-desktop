package archive

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const archiveStampFixturePath = "testdata/port/render/archive-stamp.json"

type archiveStampFixture struct {
	SchemaVersion int                   `json:"schema_version"`
	Oracle        archiveContractOracle `json:"oracle"`
	Cases         []archiveStampCase    `json:"cases"`
}

// InputPDF is the base64 of a pdfcpu-generated input, so the Rust port reads
// the bytes the Go oracle stamped.
type archiveStampCase struct {
	ID       string           `json:"id"`
	InputPDF string           `json:"input_pdf"`
	OCRText  string           `json:"ocr_text"`
	Want     archiveStampWant `json:"want"`
}

// ErrorKind is the text before the first colon of Generate's error, which is
// stable where the full message carries a temporary path.
type archiveStampWant struct {
	OK        bool               `json:"ok"`
	ErrorKind string             `json:"error_kind"`
	PageCount int                `json:"page_count"`
	Valid     bool               `json:"valid"`
	Pages     []archiveStampPage `json:"pages"`
}

// Stamped means pdftotext recovers the page's stamp text. Text is the sorted
// letters and digits pdftotext recovers from the whole page, so it also pins
// text that lands on a page it was not meant for. Width and height are the
// MediaBox size from pdfcpu.
type archiveStampPage struct {
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	Stamped bool    `json:"stamped"`
	Text    string  `json:"text"`
}

// TestPortArchiveStampFixture captures Generate's stamp outcome for PDF inputs:
// page count, validation, MediaBox sizes, per-page stamp recovery by pdftotext,
// and the error kind. It compares semantic facts, not bytes, because pdfcpu's
// object layout cannot be reproduced exactly. Regenerate deliberately with:
//
//	PORT_RENDER_ARCHIVE_STAMP_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchiveStampFixture$'
//
// Regeneration needs pdftotext. Windows has no Poppler in CI, so there the
// test validates the stored oracle without regenerating it.
func TestPortArchiveStampFixture(t *testing.T) {
	generate := os.Getenv("PORT_RENDER_ARCHIVE_STAMP_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1"
	path := filepath.Join(archiveContractRepoRoot(t), filepath.FromSlash(archiveStampFixturePath))
	if !generate && runtime.GOOS == "windows" {
		current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
		if err != nil {
			t.Fatalf("read fixture %s: %v", path, err)
		}
		var stored archiveStampFixture
		if err := json.Unmarshal(current, &stored); err != nil {
			t.Fatalf("decode stored oracle: %v", err)
		}
		canonical, err := encodeArchiveStampFixture(stored)
		if err != nil || !bytes.Equal(current, canonical) {
			t.Fatalf("stored oracle is not canonical: %v", err)
		}
		t.Logf("PASS %s on Windows: stored Go oracle validated, not regenerated (no Poppler)", archiveStampFixturePath)
		return
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Fatalf("archive stamp oracle needs pdftotext (Poppler): %v", err)
	}
	fixture := buildArchiveStampFixture(t)
	encoded, err := encodeArchiveStampFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArchiveStampFixture(encoded, fixture); err != nil {
		t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
	}
	var mutated archiveStampFixture
	if err := json.Unmarshal(encoded, &mutated); err != nil {
		t.Fatal(err)
	}
	mutated.Cases[0].Want.PageCount++
	mutatedBytes, err := encodeArchiveStampFixture(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArchiveStampFixture(mutatedBytes, fixture); err == nil || !strings.Contains(err.Error(), "differs from Go") {
		t.Fatalf("page-count mutation control = %v, want Go-output rejection", err)
	}
	if generate {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s with %d Go %s cases", archiveStampFixturePath, len(fixture.Cases), runtime.Version())
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_ARCHIVE_STAMP_GENERATE=1): %v", path, err)
	}
	if err := validateArchiveStampFixture(current, fixture); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s: %d Go %s cases", archiveStampFixturePath, len(fixture.Cases), runtime.Version())
}

func buildArchiveStampFixture(t *testing.T) archiveStampFixture {
	t.Helper()
	if runtime.Version() != "go1.26.10" {
		t.Fatalf("archive stamp oracle requires Go go1.26.10, running %s", runtime.Version())
	}
	one := archiveStampInputPDF(t, 1, "")
	two := archiveStampInputPDF(t, 2, "")
	three := archiveStampInputPDF(t, 3, "")
	a4 := archiveStampInputPDF(t, 1, "form:A4P, pos:c, sc:1")
	fixture := archiveStampFixture{
		SchemaVersion: 1,
		Oracle: archiveContractOracle{
			Commit:       fixtureoracle.Current().Commit,
			GoVersion:    runtime.Version(),
			Source:       archiveContractSourcePath,
			SourceSHA256: archiveContractSourceSHA256,
		},
		Cases: []archiveStampCase{
			{ID: "single-page-text", InputPDF: one, OCRText: "hello"},
			{ID: "two-pages-marker-text", InputPDF: two, OCRText: "alpha\n\n--- Page ---\n\nbeta"},
			{ID: "two-pages-text-first-only", InputPDF: two, OCRText: "only"},
			{ID: "two-pages-blank-text-no-stamp", InputPDF: two, OCRText: " \n\t "},
			{ID: "two-pages-formfeed-text", InputPDF: two, OCRText: "fa\fbb"},
			{ID: "two-pages-trailing-marker-single", InputPDF: two, OCRText: "x\n\n--- Page ---\n\n"},
			// pdfcpu selects every page when a watermark names only pages the
			// document does not have, so text for page 3 lands on both pages.
			{ID: "two-pages-ocr-has-three-pages", InputPDF: two, OCRText: "a\n\n--- Page ---\n\nb\n\n--- Page ---\n\nc"},
			{ID: "three-pages-blank-middle", InputPDF: three, OCRText: "a\n\n--- Page ---\n\n \n\n--- Page ---\n\nc"},
			{ID: "two-pages-multiline-text", InputPDF: two, OCRText: "hello\nworld foo"},
			{ID: "two-pages-latin1-text", InputPDF: two, OCRText: "Gr\u00fc\u00dfe"},
			{ID: "two-pages-winansi-specials", InputPDF: two, OCRText: "5\u20ac \u201cq\u201d"},
			{ID: "two-pages-tab-and-cr", InputPDF: two, OCRText: "a\tb\rc"},
			{ID: "two-pages-unmappable-runes", InputPDF: two, OCRText: "\u65e5\u672c\u8a9e x \U0001F600"},
			{ID: "two-pages-delimiters", InputPDF: two, OCRText: "a(b)c\\d"},
			{ID: "a4-page-text", InputPDF: a4, OCRText: "hello world"},
			{ID: "corrupt-input", InputPDF: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\ngarbage")), OCRText: "hello"},
		},
	}
	for i := range fixture.Cases {
		want, err := archiveStampObserve(fixture.Cases[i])
		if err != nil {
			t.Fatalf("case %q: %v", fixture.Cases[i].ID, err)
		}
		fixture.Cases[i].Want = want
	}
	return fixture
}

// archiveStampInputPDF builds an input with pdfcpu's own image import, the
// tool the archive step uses, and returns its base64 bytes.
func archiveStampInputPDF(t *testing.T, pages int, importSpec string) string {
	t.Helper()
	dir := t.TempDir()
	var images []string
	for i := 0; i < pages; i++ {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("page-%d.png", i))
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		images = append(images, path)
	}
	out := filepath.Join(dir, "input.pdf")
	var imp *pdfcpu.Import
	if importSpec != "" {
		var err error
		if imp, err = pdfcpu.ParseImportDetails(importSpec, types.POINTS); err != nil {
			t.Fatalf("parse import details %q: %v", importSpec, err)
		}
	}
	if err := api.ImportImagesFile(context.Background(), images, out, imp, nil); err != nil {
		t.Fatalf("import input images: %v", err)
	}
	data, err := os.ReadFile(out) //nolint:gosec // disposable test output
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// archiveStampObserve runs Generate on one case in a disposable directory and
// records the semantic facts the Rust port must reproduce.
func archiveStampObserve(c archiveStampCase) (archiveStampWant, error) {
	input, err := base64.StdEncoding.DecodeString(c.InputPDF)
	if err != nil {
		return archiveStampWant{}, fmt.Errorf("decode input: %w", err)
	}
	dir, err := os.MkdirTemp("", "archive-stamp-")
	if err != nil {
		return archiveStampWant{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	inPath := filepath.Join(dir, "input.pdf")
	outPath := filepath.Join(dir, "output.pdf")
	if err := os.WriteFile(inPath, input, 0o600); err != nil {
		return archiveStampWant{}, err
	}
	if err := Generate(inPath, outPath, c.OCRText); err != nil {
		return archiveStampWant{ErrorKind: strings.SplitN(err.Error(), ":", 2)[0]}, nil
	}
	count, err := api.PageCountFile(context.Background(), outPath)
	if err != nil {
		return archiveStampWant{}, fmt.Errorf("page count: %w", err)
	}
	valid := api.ValidateFile(context.Background(), outPath, nil, nil) == nil
	dims, err := api.PageDimsFile(context.Background(), outPath)
	if err != nil {
		return archiveStampWant{}, fmt.Errorf("page dims: %w", err)
	}
	want := archiveStampWant{OK: true, PageCount: count, Valid: valid}
	for page := 1; page <= count; page++ {
		text, err := pdftotextPage(outPath, page)
		if err != nil {
			return archiveStampWant{}, err
		}
		want.Pages = append(want.Pages, archiveStampPage{
			Width:   dims[page-1].Width,
			Height:  dims[page-1].Height,
			Stamped: archiveStampPresent(text, c.OCRText, page),
			Text:    sortedAlnum(text),
		})
	}
	return want, nil
}

// pdftotextPage returns the text Poppler recovers from one page.
func pdftotextPage(path string, page int) (string, error) {
	out, err := exec.Command("pdftotext", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), path, "-").Output() //nolint:gosec // fixed tool, disposable paths
	if err != nil {
		return "", fmt.Errorf("pdftotext page %d: %w", page, err)
	}
	return string(out), nil
}

// archiveStampPresent reports whether the page text carries the letters and
// digits of the stamp text Generate assigns to that page. Poppler splits the
// 45-degree stamp into glyph fragments and reorders them, so word order and
// punctuation are not checked, only the letter and digit counts.
func archiveStampPresent(pageText, ocrText string, page int) bool {
	stamp := alnumCounts(archiveStampPageText(ocrText, page))
	if len(stamp) == 0 {
		return false
	}
	found := alnumCounts(pageText)
	for r, n := range stamp {
		if found[r] < n {
			return false
		}
	}
	return true
}

// sortedAlnum is the page's letters and digits in byte order.
func sortedAlnum(text string) string {
	var runes []rune
	for r, n := range alnumCounts(text) {
		for i := 0; i < n; i++ {
			runes = append(runes, r)
		}
	}
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	return string(runes)
}

func alnumCounts(text string) map[rune]int {
	counts := map[rune]int{}
	for _, r := range text {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			counts[r]++
		}
	}
	return counts
}

// archiveStampPageText is the text Generate stamps onto page n (1-based).
func archiveStampPageText(ocrText string, page int) string {
	pages := splitPages(ocrText)
	if page-1 >= len(pages) {
		return ""
	}
	return strings.TrimSpace(pages[page-1])
}

func encodeArchiveStampFixture(fixture archiveStampFixture) ([]byte, error) {
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fixture: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateArchiveStampFixture(encoded []byte, expected archiveStampFixture) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual archiveStampFixture
	if err := decoder.Decode(&actual); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("fixture has trailing JSON data: %v", err)
	}
	if len(actual.Cases) != len(expected.Cases) {
		return fmt.Errorf("case count = %d, want %d", len(actual.Cases), len(expected.Cases))
	}
	for i, c := range actual.Cases {
		want := expected.Cases[i]
		// pdfcpu image import embeds timestamps, so the stored input bytes are the
		// oracle input. Only IDs and OCR text are compared against a fresh build.
		if c.ID != want.ID || c.OCRText != want.OCRText {
			return fmt.Errorf("case %q input or order changed", c.ID)
		}
		got, err := json.Marshal(c.Want)
		if err != nil {
			return err
		}
		observed, err := archiveStampObserve(c)
		if err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
		wantBytes, err := json.Marshal(observed)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, wantBytes) {
			return fmt.Errorf("case %q output differs from Go", c.ID)
		}
	}
	canonical, err := encodeArchiveStampFixture(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}
