package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const pdfFrontmatterFixturePath = "testdata/port/render/pdf-frontmatter.json"

type pdfFrontmatterFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Oracle        pdfFrontmatterOracle `json:"oracle"`
	Cases         []pdfFrontmatterCase `json:"cases"`
}

type pdfFrontmatterOracle struct {
	Commit       string `json:"commit"`
	GoVersion    string `json:"go_version"`
	Source       string `json:"source"`
	SourceSHA256 string `json:"source_sha256"`
}

// Frontmatter is the YAML text of the note's frontmatter block (without the
// fences), decoded by yaml.v3 exactly as the vault decodes it. Output is the
// block pdfFrontmatter emits, or the encoding error it returns.
type pdfFrontmatterCase struct {
	ID          string `json:"id"`
	Frontmatter string `json:"frontmatter"`
	Title       string `json:"title"`
	Output      string `json:"output"`
	Error       string `json:"error"`
}

// TestPortRenderPDFFrontmatterFixture captures pdfFrontmatter from Go itself.
// Regenerate deliberately with:
//
//	PORT_RENDER_PDF_FRONTMATTER_GENERATE=1 GOTOOLCHAIN=go1.26.9 go test -count=1 ./internal/export -run '^TestPortRenderPDFFrontmatterFixture$'
func TestPortRenderPDFFrontmatterFixture(t *testing.T) {
	fixture := buildPDFFrontmatterFixture(t)
	encoded, err := encodePDFFrontmatterFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePDFFrontmatterFixture(encoded, fixture); err != nil {
		t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
	}
	var mutated pdfFrontmatterFixture
	if err := json.Unmarshal(encoded, &mutated); err != nil {
		t.Fatal(err)
	}
	mutated.Cases[0].Output += "mutated"
	mutatedBytes, err := encodePDFFrontmatterFixture(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePDFFrontmatterFixture(mutatedBytes, fixture); err == nil || !strings.Contains(err.Error(), "differs from Go") {
		t.Fatalf("output mutation control = %v, want Go-output rejection", err)
	}

	path := filepath.Join(exportMarkdownRepoRoot(t), filepath.FromSlash(pdfFrontmatterFixturePath))
	if os.Getenv("PORT_RENDER_PDF_FRONTMATTER_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s with %d Go %s cases", pdfFrontmatterFixturePath, len(fixture.Cases), runtime.Version())
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_PDF_FRONTMATTER_GENERATE=1): %v", path, err)
	}
	if err := validatePDFFrontmatterFixture(current, fixture); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s: %d Go %s cases", pdfFrontmatterFixturePath, len(fixture.Cases), runtime.Version())
}

func buildPDFFrontmatterFixture(t *testing.T) pdfFrontmatterFixture {
	t.Helper()
	if runtime.Version() != noteHTMLOracleGoVersion {
		t.Fatalf("PDF frontmatter oracle requires Go %s, running %s", noteHTMLOracleGoVersion, runtime.Version())
	}
	source, err := os.ReadFile(filepath.Join(exportMarkdownRepoRoot(t), "internal", "export", "export.go")) //nolint:gosec // pinned source file
	if err != nil {
		t.Fatalf("read pinned Go export source: %v", err)
	}
	sum := noteHTMLSHA256(string(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))))
	if sum != noteHTMLOracleSourceSHA256 {
		t.Fatalf("Go oracle source export.go changed: got %s, want pinned %s", sum, noteHTMLOracleSourceSHA256)
	}
	fixture := pdfFrontmatterFixture{
		SchemaVersion: 1,
		Oracle: pdfFrontmatterOracle{
			Commit:       fixtureoracle.Current().Commit,
			GoVersion:    runtime.Version(),
			Source:       "internal/export/export.go",
			SourceSHA256: sum,
		},
		Cases: pdfFrontmatterCorpus(),
	}
	for i := range fixture.Cases {
		fixture.Cases[i].Output, fixture.Cases[i].Error = runPDFFrontmatterCase(fixture.Cases[i])
	}
	return fixture
}

// runPDFFrontmatterCase decodes the frontmatter like the vault does, then
// calls pdfFrontmatter. A decode failure is a fixture bug, not a Go outcome.
func runPDFFrontmatterCase(c pdfFrontmatterCase) (string, string) {
	front := map[string]interface{}{}
	if strings.TrimSpace(c.Frontmatter) != "" {
		if err := yaml.Unmarshal([]byte(c.Frontmatter), &front); err != nil {
			return "", "decode: " + err.Error()
		}
	}
	out, err := pdfFrontmatter(front, c.Title)
	if err != nil {
		return "", err.Error()
	}
	return out, ""
}

func pdfFrontmatterCorpus() []pdfFrontmatterCase {
	return []pdfFrontmatterCase{
		{ID: "empty-no-title", Frontmatter: "", Title: ""},
		{ID: "empty-with-title", Frontmatter: "", Title: "Report"},
		{ID: "explicit-title-wins", Frontmatter: "title: Own\n", Title: "Derived"},
		{ID: "title-from-document", Frontmatter: "tags: [a]\n", Title: "Derived"},
		{ID: "report-profile-fields", Frontmatter: "title: Angebot\nlanguage: de\nsubject: \"Re: Vertrag\"\n", Title: ""},
		{ID: "behoerde-recipient", Frontmatter: "recipient:\n  name: Amt\n  street: Hauptstraße 1\n", Title: "Antrag"},
		{ID: "scalars", Frontmatter: "count: 42\nratio: 0.5\nenabled: true\nnothing: null\nempty: \"\"\n", Title: "S"},
		{ID: "negative-and-exponent", Frontmatter: "neg: -7\nexp: 1.5e3\nhex: 0x1F\noct: 0o17\n", Title: "N"},
		{ID: "quoted-looking-strings", Frontmatter: "a: \"true\"\nb: \"123\"\nc: \"null\"\nd: \"1e3\"\n", Title: "Q"},
		{ID: "indicator-strings", Frontmatter: "a: \"#hash\"\nb: \"- dash\"\nc: \"key: value\"\nd: \"trail \"\ne: \" lead\"\n", Title: "I"},
		{ID: "multiline-string", Frontmatter: "note: |\n  first line\n  second line\n", Title: "M"},
		{ID: "lists", Frontmatter: "tags:\n  - alpha\n  - \"beta gamma\"\n  - 3\nempty: []\n", Title: "L"},
		{ID: "nested-maps", Frontmatter: "outer:\n  inner:\n    deep: value\n  sibling: 2\nemptymap: {}\n", Title: "Nest"},
		{ID: "list-of-maps", Frontmatter: "items:\n  - name: a\n    qty: 1\n  - name: b\n    qty: 2\n", Title: "LM"},
		{ID: "key-order-natural", Frontmatter: "item10: x\nitem2: y\nItem1: z\nalpha: w\n_under: v\n", Title: "Order"},
		{ID: "unicode-and-escapes", Frontmatter: "name: \"Grüße \\t tab\"\nemoji: \"\\U0001F600\"\n", Title: "Ü"},
		{ID: "key-needs-quoting", Frontmatter: "\"a: b\": 1\n\"yes\": 2\n", Title: "K"},
		{ID: "title-empty-string-in-frontmatter", Frontmatter: "title: \"\"\n", Title: "Filled"},
		{ID: "property-shaped-keys", Frontmatter: "type: text\ndefault: x\nlabel: L\noptions: [a]\ndescription: d\n", Title: "P"},
		{ID: "view-shaped-keys", Frontmatter: "source: s\nname: n\nid: i\ntype: table\ncolumns: [c]\n", Title: "V"},
		{ID: "title-with-crlf", Frontmatter: "", Title: "line one\nline two"},
	}
}

func encodePDFFrontmatterFixture(fixture pdfFrontmatterFixture) ([]byte, error) {
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fixture: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validatePDFFrontmatterFixture(encoded []byte, expected pdfFrontmatterFixture) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual pdfFrontmatterFixture
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
		if c.ID != want.ID || c.Frontmatter != want.Frontmatter || c.Title != want.Title {
			return fmt.Errorf("case %q input or order changed", c.ID)
		}
		out, errText := runPDFFrontmatterCase(c)
		if c.Output != out || c.Error != errText {
			return fmt.Errorf("case %q output differs from Go", c.ID)
		}
	}
	canonical, err := encodePDFFrontmatterFixture(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}
