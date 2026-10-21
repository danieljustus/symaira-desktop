package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const archiveContractFixturePath = "testdata/port/render/archive-contract.json"

// archiveContractSourcePath is the production file whose bytes the oracle pins.
const archiveContractSourcePath = "internal/archive/archive.go"

// archiveContractSourceSHA256 pins archive.go over canonical LF text. Changing
// the file requires an explicit fixture regeneration.
const archiveContractSourceSHA256 = "d9f95ceb810f9a9b125066e35d446643d733e5dc32c4714d3a7f21860a2d56ab"

type archiveContractFixture struct {
	SchemaVersion int                   `json:"schema_version"`
	Oracle        archiveContractOracle `json:"oracle"`
	Cases         []archiveContractCase `json:"cases"`
}

type archiveContractOracle struct {
	Commit       string `json:"commit"`
	GoVersion    string `json:"go_version"`
	Source       string `json:"source"`
	SourceSHA256 string `json:"source_sha256"`
}

// Op selects the function under test. Input is its argument. Want holds the
// observed result; each op fills only the fields it defines.
type archiveContractCase struct {
	ID    string              `json:"id"`
	Op    string              `json:"op"`
	Input string              `json:"input"`
	Want  archiveContractWant `json:"want"`
}

type archiveContractWant struct {
	Strings []string `json:"strings"`
	Bool    bool     `json:"bool"`
	Error   string   `json:"error"`
}

// TestPortArchiveContractFixture captures the pure archive contract from Go
// itself: page splitting, archive naming, the text-layer probe, OCR sidecar
// trimming and the unsupported-format error. Stamping through pdfcpu is not
// covered here. Regenerate deliberately with:
//
//	PORT_RENDER_ARCHIVE_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchiveContractFixture$'
func TestPortArchiveContractFixture(t *testing.T) {
	fixture := buildArchiveContractFixture(t)
	encoded, err := encodeArchiveContractFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArchiveContractFixture(encoded, fixture); err != nil {
		t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
	}
	var mutated archiveContractFixture
	if err := json.Unmarshal(encoded, &mutated); err != nil {
		t.Fatal(err)
	}
	mutated.Cases[0].Want.Strings = append(mutated.Cases[0].Want.Strings, "mutated")
	mutatedBytes, err := encodeArchiveContractFixture(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArchiveContractFixture(mutatedBytes, fixture); err == nil || !strings.Contains(err.Error(), "differs from Go") {
		t.Fatalf("output mutation control = %v, want Go-output rejection", err)
	}

	path := filepath.Join(archiveContractRepoRoot(t), filepath.FromSlash(archiveContractFixturePath))
	if os.Getenv("PORT_RENDER_ARCHIVE_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s with %d Go %s cases", archiveContractFixturePath, len(fixture.Cases), runtime.Version())
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_ARCHIVE_GENERATE=1): %v", path, err)
	}
	if err := validateArchiveContractFixture(current, fixture); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s: %d Go %s cases", archiveContractFixturePath, len(fixture.Cases), runtime.Version())
}

func buildArchiveContractFixture(t *testing.T) archiveContractFixture {
	t.Helper()
	if runtime.Version() != "go1.26.10" {
		t.Fatalf("archive fixture oracle requires Go go1.26.10, running %s", runtime.Version())
	}
	source, err := os.ReadFile(filepath.Join(archiveContractRepoRoot(t), filepath.FromSlash(archiveContractSourcePath))) //nolint:gosec // pinned source file
	if err != nil {
		t.Fatalf("read pinned Go archive source: %v", err)
	}
	sum := archiveContractSHA256(string(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))))
	if sum != archiveContractSourceSHA256 {
		t.Fatalf("Go oracle source %s changed: got %s, want pinned %s", archiveContractSourcePath, sum, archiveContractSourceSHA256)
	}
	fixture := archiveContractFixture{
		SchemaVersion: 1,
		Oracle: archiveContractOracle{
			Commit:       fixtureoracle.Current().Commit,
			GoVersion:    runtime.Version(),
			Source:       archiveContractSourcePath,
			SourceSHA256: sum,
		},
		Cases: archiveContractCorpus(),
	}
	for i := range fixture.Cases {
		fixture.Cases[i].Want = runArchiveContractCase(t, fixture.Cases[i])
	}
	return fixture
}

// runArchiveContractCase calls the production function for the case's op.
// File-reading ops write the input to a disposable file first, so the oracle
// exercises the real reader rather than a copy of its logic.
func runArchiveContractCase(t *testing.T, c archiveContractCase) archiveContractWant {
	t.Helper()
	var want archiveContractWant
	switch c.Op {
	case "split_pages":
		want.Strings = append([]string{}, splitPages(c.Input)...)
	case "archive_path":
		want.Strings = []string{ArchivePath(c.Input)}
	case "has_text_layer":
		want.Bool = HasTextLayer(archiveContractTempFile(t, c))
	case "ocr_text":
		text, err := OCRTextFromFile(archiveContractTempFile(t, c))
		if err != nil {
			t.Fatalf("case %q: %v", c.ID, err)
		}
		want.Strings = []string{text}
	case "generate_unsupported":
		err := Generate(c.Input, filepath.Join(t.TempDir(), "out.pdf"), "text")
		if err == nil {
			t.Fatalf("case %q: Generate accepted %q; supported formats are not part of this fixture", c.ID, c.Input)
		}
		want.Error = err.Error()
	default:
		t.Fatalf("case %q: unknown op %q", c.ID, c.Op)
	}
	if want.Strings == nil {
		want.Strings = []string{}
	}
	return want
}

func archiveContractTempFile(t *testing.T, c archiveContractCase) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(path, []byte(c.Input), 0o600); err != nil {
		t.Fatalf("case %q: write input: %v", c.ID, err)
	}
	return path
}

func archiveContractCorpus() []archiveContractCase {
	return []archiveContractCase{
		{ID: "split-empty", Op: "split_pages", Input: ""},
		{ID: "split-whitespace-only", Op: "split_pages", Input: " \n\t "},
		{ID: "split-single", Op: "split_pages", Input: "hello"},
		{ID: "split-markers", Op: "split_pages", Input: "one\n\n--- Page ---\n\ntwo\n\n--- Page ---\n\nthree"},
		{ID: "split-marker-keeps-piece-spaces", Op: "split_pages", Input: "  a  \n\n--- Page ---\n\n  b  "},
		{ID: "split-marker-not-exact", Op: "split_pages", Input: "x--- Page ---y"},
		{ID: "split-trailing-marker-trimmed", Op: "split_pages", Input: "a\n\n--- Page ---\n\n"},
		{ID: "split-formfeeds", Op: "split_pages", Input: "a\fb\f\fc"},
		{ID: "split-marker-wins-over-formfeed", Op: "split_pages", Input: "a\f\n\n--- Page ---\n\nb"},
		{ID: "split-outer-whitespace-trimmed", Op: "split_pages", Input: "\n\n a \n"},
		{ID: "split-interior-unicode-space-kept", Op: "split_pages", Input: "Grüße  x"},
		{ID: "archive-path-pdf", Op: "archive_path", Input: "dir/doc.pdf"},
		{ID: "archive-path-no-extension", Op: "archive_path", Input: "doc"},
		{ID: "archive-path-dotfile", Op: "archive_path", Input: ".hidden"},
		{ID: "archive-path-dot-in-directory", Op: "archive_path", Input: "dir.v2/noext"},
		{ID: "archive-path-dot-directory-only", Op: "archive_path", Input: "a/b.c/d"},
		{ID: "archive-path-trailing-dot", Op: "archive_path", Input: "trailing."},
		{ID: "archive-path-double-extension", Op: "archive_path", Input: "a.tar.gz"},
		{ID: "archive-path-hidden-with-extension", Op: "archive_path", Input: "dir/.x.md"},
		{ID: "text-layer-empty", Op: "has_text_layer", Input: ""},
		{ID: "text-layer-bt-and-et", Op: "has_text_layer", Input: "BT /F1 12 Tf ET"},
		{ID: "text-layer-bt-only", Op: "has_text_layer", Input: "BT only"},
		{ID: "text-layer-reversed-order", Op: "has_text_layer", Input: "ET then BT"},
		{ID: "text-layer-lowercase-rejected", Op: "has_text_layer", Input: "bt et"},
		{ID: "text-layer-control-bytes", Op: "has_text_layer", Input: "\u0000BT\u0001ET"},
		{ID: "ocr-trims-spaces", Op: "ocr_text", Input: "  text \n"},
		{ID: "ocr-only-whitespace", Op: "ocr_text", Input: "\t\n"},
		{ID: "ocr-interior-newlines-kept", Op: "ocr_text", Input: "a\nb"},
		{ID: "ocr-trims-nbsp", Op: "ocr_text", Input: " x "},
		{ID: "generate-unsupported-txt", Op: "generate_unsupported", Input: "scan.txt"},
		{ID: "generate-unsupported-uppercase", Op: "generate_unsupported", Input: "SCAN.DOC"},
		{ID: "generate-unsupported-no-extension", Op: "generate_unsupported", Input: "noext"},
		{ID: "generate-unsupported-dot-in-directory", Op: "generate_unsupported", Input: "dir.d/file"},
		{ID: "generate-unsupported-unicode-extension", Op: "generate_unsupported", Input: "x.Ü"},
	}
}

func encodeArchiveContractFixture(fixture archiveContractFixture) ([]byte, error) {
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fixture: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateArchiveContractFixture(encoded []byte, expected archiveContractFixture) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual archiveContractFixture
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
		if c.ID != want.ID || c.Op != want.Op || c.Input != want.Input {
			return fmt.Errorf("case %q input or order changed", c.ID)
		}
		got, err := json.Marshal(c.Want)
		if err != nil {
			return err
		}
		wantBytes, err := json.Marshal(runArchiveContractCaseNoTest(c))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, wantBytes) {
			return fmt.Errorf("case %q output differs from Go", c.ID)
		}
	}
	canonical, err := encodeArchiveContractFixture(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}

// runArchiveContractCaseNoTest re-derives the Go outcome for validation. It
// cannot call t.Fatal, so an unexpected failure is reported as a mismatch.
func runArchiveContractCaseNoTest(c archiveContractCase) archiveContractWant {
	var want archiveContractWant
	switch c.Op {
	case "split_pages":
		want.Strings = append([]string{}, splitPages(c.Input)...)
	case "archive_path":
		want.Strings = []string{ArchivePath(c.Input)}
	case "has_text_layer", "ocr_text":
		dir, err := os.MkdirTemp("", "archive-contract-")
		if err != nil {
			return archiveContractWant{Error: err.Error()}
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "input.bin")
		if err := os.WriteFile(path, []byte(c.Input), 0o600); err != nil {
			return archiveContractWant{Error: err.Error()}
		}
		if c.Op == "has_text_layer" {
			want.Bool = HasTextLayer(path)
		} else {
			text, err := OCRTextFromFile(path)
			if err != nil {
				return archiveContractWant{Error: err.Error()}
			}
			want.Strings = []string{text}
		}
	case "generate_unsupported":
		err := Generate(c.Input, filepath.Join(os.TempDir(), "archive-contract-unused.pdf"), "text")
		if err != nil {
			want.Error = err.Error()
		}
	}
	if want.Strings == nil {
		want.Strings = []string{}
	}
	return want
}

func archiveContractSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func archiveContractRepoRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Go archive contract test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
