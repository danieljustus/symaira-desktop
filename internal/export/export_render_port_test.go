package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const (
	noteHTMLOracleGoVersion    = "go1.26.6"
	noteHTMLOracleSource       = "internal/export/export.go"
	noteHTMLOracleSourceSHA256 = "fb244cc8ce36b8e4e8199b1e9183cf009f107b3a3d4ec14bf7a5afe10ee60861"
	noteHTMLFixturePath        = "testdata/port/render/html-note.json"
)

var noteHTMLOracleCommit = func() string {
	return fixtureoracle.Current().Commit
}

type noteHTMLFixture struct {
	SchemaVersion int            `json:"schema_version"`
	Oracle        noteHTMLOracle `json:"oracle"`
	Cases         []noteHTMLCase `json:"cases"`
}

type noteHTMLOracle struct {
	Commit       string `json:"commit"`
	GoVersion    string `json:"go_version"`
	Source       string `json:"source"`
	SourceSHA256 string `json:"source_sha256"`
}

type noteHTMLCase struct {
	ID             string `json:"id"`
	Markdown       string `json:"markdown"`
	MarkdownSHA256 string `json:"markdown_sha256"`
	HTML           string `json:"html"`
	HTMLSHA256     string `json:"html_sha256"`
}

// TestPortRenderHTMLFixture captures noteToHTML itself, not a second renderer.
// Regenerate deliberately with:
//
//	PORT_RENDER_HTML_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/export -run '^TestPortRenderHTMLFixture$'
func TestPortRenderHTMLFixture(t *testing.T) {
	fixture := buildNoteHTMLFixture(t)
	encoded, err := encodeNoteHTMLFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNoteHTMLFixture(encoded, fixture); err != nil {
		t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
	}
	assertNoteHTMLFixtureMutationControls(t, fixture, encoded)
	t.Log("PASS mutation controls: output hash, case ID, and rehashed wrong output rejected")

	path := noteHTMLFixturePathFromTest(t)
	if os.Getenv("PORT_RENDER_HTML_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // test fixture directory
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		current, err := os.ReadFile(path) //nolint:gosec // read back test fixture file
		if err != nil {
			t.Fatalf("read generated fixture: %v", err)
		}
		if err := validateNoteHTMLFixture(current, fixture); err != nil {
			t.Fatalf("generated fixture readback failed: %v", err)
		}
		t.Logf("GENERATED %s with %d Go %s cases; ids=%s; source_sha256=%s", noteHTMLFixturePath, len(fixture.Cases), runtime.Version(), strings.Join(noteHTMLCaseIDs(fixture.Cases), ","), fixture.Oracle.SourceSHA256)
		return
	}

	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_HTML_GENERATE=1): %v", path, err)
	}
	if err := validateNoteHTMLFixture(current, fixture); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("Go oracle fixture bytes are not canonical; regenerate deliberately")
	}
	t.Logf("PASS %s: %d Go %s cases; ids=%s; source_sha256=%s", noteHTMLFixturePath, len(fixture.Cases), runtime.Version(), strings.Join(noteHTMLCaseIDs(fixture.Cases), ","), fixture.Oracle.SourceSHA256)
}

func buildNoteHTMLFixture(t *testing.T) noteHTMLFixture {
	t.Helper()
	if runtime.Version() != noteHTMLOracleGoVersion {
		t.Fatalf("HTML fixture oracle requires Go %s, running %s", noteHTMLOracleGoVersion, runtime.Version())
	}
	sourcePath := filepath.Join(filepath.Dir(noteHTMLFixtureSourcePath(t)), "export.go")
	source, err := os.ReadFile(sourcePath) //nolint:gosec // pinned source file
	if err != nil {
		t.Fatalf("read pinned Go HTML oracle source: %v", err)
	}
	// Git may check text out with CRLF on Windows; the source digest is over
	// canonical LF text while all Markdown and HTML fixture fields remain exact.
	source = bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	sourceSum := sha256.Sum256(source)
	sourceHash := hex.EncodeToString(sourceSum[:])
	if sourceHash != noteHTMLOracleSourceSHA256 {
		t.Fatalf("Go oracle source changed: got %s, want pinned %s", sourceHash, noteHTMLOracleSourceSHA256)
	}

	fixture := noteHTMLFixture{
		SchemaVersion: 1,
		Oracle: noteHTMLOracle{
			Commit:       noteHTMLOracleCommit(),
			GoVersion:    runtime.Version(),
			Source:       noteHTMLOracleSource,
			SourceSHA256: sourceHash,
		},
	}
	for _, input := range noteHTMLCorpus() {
		html := noteToHTML(input.markdown)
		fixture.Cases = append(fixture.Cases, noteHTMLCase{
			ID:             input.id,
			Markdown:       input.markdown,
			MarkdownSHA256: noteHTMLSHA256(input.markdown),
			HTML:           html,
			HTMLSHA256:     noteHTMLSHA256(html),
		})
	}
	return fixture
}

type noteHTMLInput struct {
	id       string
	markdown string
}

func noteHTMLCorpus() []noteHTMLInput {
	return []noteHTMLInput{
		{id: "empty", markdown: ""},
		{id: "trailing-newline", markdown: "# Tail\n"},
		{id: "all-five-html-entities", markdown: `& < > " '`},
		{id: "go-unicode-trim", markdown: "\u0085\u00a0## Titre\u00a0\u0085\n\u2028paragraph with separators\u2028"},
		{id: "crlf", markdown: "  # CRLF\r\n- first\r\n\r\nbody\r\n"},
		{id: "headings-lists-transitions", markdown: "# One\n## Two\n### Three\n- first\n## heading does not close list\n* second\nplain closes list\n- opens again\n#### literal paragraph closes list\n"},
		{id: "nested-list-prefix-trim", markdown: "- * both prefixes\n* - retain dash\n"},
		{id: "plain-wikilinks", markdown: "See [[Other Note|label]] and [[Other#Part]]."},
		{id: "multiple-blank-lines", markdown: "\n  \n\t\n"},
	}
}

func encodeNoteHTMLFixture(fixture noteHTMLFixture) ([]byte, error) {
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fixture: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateNoteHTMLFixture(encoded []byte, expected noteHTMLFixture) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual noteHTMLFixture
	if err := decoder.Decode(&actual); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("fixture has trailing JSON data: %v", err)
	}
	if actual.SchemaVersion != expected.SchemaVersion {
		return fmt.Errorf("schema_version = %d, want %d", actual.SchemaVersion, expected.SchemaVersion)
	}
	if actual.Oracle != expected.Oracle {
		return fmt.Errorf("oracle metadata changed: got %#v, want %#v", actual.Oracle, expected.Oracle)
	}
	wantIDs := noteHTMLCaseIDs(expected.Cases)
	gotIDs := noteHTMLCaseIDs(actual.Cases)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		return fmt.Errorf("case IDs/order changed: got %v, want %v", gotIDs, wantIDs)
	}
	for index, actualCase := range actual.Cases {
		wantCase := expected.Cases[index]
		if actualCase.Markdown != wantCase.Markdown {
			return fmt.Errorf("case %q Markdown input changed", actualCase.ID)
		}
		if actualCase.MarkdownSHA256 != noteHTMLSHA256(actualCase.Markdown) {
			return fmt.Errorf("case %q Markdown SHA-256 mismatch", actualCase.ID)
		}
		if actualCase.HTMLSHA256 != noteHTMLSHA256(actualCase.HTML) {
			return fmt.Errorf("case %q HTML SHA-256 mismatch", actualCase.ID)
		}
		if actualCase.HTML != noteToHTML(actualCase.Markdown) {
			return fmt.Errorf("case %q HTML bytes differ from Go noteToHTML", actualCase.ID)
		}
		if actualCase.HTML != wantCase.HTML || actualCase.HTMLSHA256 != wantCase.HTMLSHA256 {
			return fmt.Errorf("case %q output differs from regenerated Go oracle", actualCase.ID)
		}
	}
	canonical, err := encodeNoteHTMLFixture(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}

func assertNoteHTMLFixtureMutationControls(t *testing.T, fixture noteHTMLFixture, valid []byte) {
	t.Helper()
	if err := validateNoteHTMLFixture(valid, fixture); err != nil {
		t.Fatalf("valid fixture rejected before mutation controls: %v", err)
	}

	var changedHash noteHTMLFixture
	if err := json.Unmarshal(valid, &changedHash); err != nil {
		t.Fatal(err)
	}
	changedHash.Cases[0].HTML += "mutated"
	mutatedHTML, err := encodeNoteHTMLFixture(changedHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNoteHTMLFixture(mutatedHTML, fixture); err == nil || !strings.Contains(err.Error(), "HTML SHA-256 mismatch") {
		t.Fatalf("HTML/hash mutation control = %v, want specific hash rejection", err)
	}

	var changedCase noteHTMLFixture
	if err := json.Unmarshal(valid, &changedCase); err != nil {
		t.Fatal(err)
	}
	changedCase.Cases[0].ID = "mutated-case-id"
	mutatedCase, err := encodeNoteHTMLFixture(changedCase)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNoteHTMLFixture(mutatedCase, fixture); err == nil || !strings.Contains(err.Error(), "case IDs/order changed") {
		t.Fatalf("case-ID mutation control = %v, want specific case-set rejection", err)
	}

	var changedOracleOutput noteHTMLFixture
	if err := json.Unmarshal(valid, &changedOracleOutput); err != nil {
		t.Fatal(err)
	}
	changedOracleOutput.Cases[0].HTML += "mutated"
	changedOracleOutput.Cases[0].HTMLSHA256 = noteHTMLSHA256(changedOracleOutput.Cases[0].HTML)
	mutatedOracleOutput, err := encodeNoteHTMLFixture(changedOracleOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNoteHTMLFixture(mutatedOracleOutput, fixture); err == nil || !strings.Contains(err.Error(), "HTML bytes differ from Go noteToHTML") {
		t.Fatalf("rehashed output mutation control = %v, want Go-output rejection", err)
	}
}

func noteHTMLSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func noteHTMLCaseIDs(cases []noteHTMLCase) []string {
	ids := make([]string, len(cases))
	for index, item := range cases {
		ids[index] = item.ID
	}
	return ids
}

func noteHTMLFixtureSourcePath(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Go oracle test source")
	}
	return source
}

func noteHTMLFixturePathFromTest(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(filepath.Join(filepath.Dir(noteHTMLFixtureSourcePath(t)), "..", ".."))
	return filepath.Join(root, filepath.FromSlash(noteHTMLFixturePath))
}
