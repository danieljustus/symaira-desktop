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
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const exportMarkdownFixturePath = "testdata/port/render/export-markdown.json"

// Pinned over canonical LF text; a source change must regenerate deliberately.
var exportMarkdownOracleSources = map[string]string{
	"internal/export/export.go":         noteHTMLOracleSourceSHA256,
	"internal/export/search_results.go": "1600b78dfbfa37ac6845b094ae761e61b4a108dedb5709175677b5aa60f4bdda",
}

type exportMarkdownFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Oracle        exportMarkdownOracle `json:"oracle"`
	Cases         []exportMarkdownCase `json:"cases"`
}

type exportMarkdownOracle struct {
	Commit    string            `json:"commit"`
	GoVersion string            `json:"go_version"`
	Sources   map[string]string `json:"sources"`
}

// Exactly one of Search, Table or Links is set per case.
type exportMarkdownCase struct {
	ID     string                `json:"id"`
	Search *exportSearchInput    `json:"search,omitempty"`
	Table  *exportTableInput     `json:"table,omitempty"`
	Links  *string               `json:"links,omitempty"`
	Want   exportMarkdownOutcome `json:"want"`
}

type exportSearchInput struct {
	Query   string                 `json:"query"`
	Title   string                 `json:"title"`
	Created string                 `json:"created"`
	Format  string                 `json:"format"`
	Hits    []exportSearchHitInput `json:"hits"`
}

type exportSearchHitInput struct {
	Path            string                    `json:"path"`
	Title           string                    `json:"title"`
	Snippet         string                    `json:"snippet"`
	Score           float64                   `json:"score"`
	Anchor          *retrieval.LocationAnchor `json:"anchor"`
	MetadataMatches []string                  `json:"metadata_matches"`
}

type exportTableInput struct {
	Columns []string            `json:"columns"`
	Rows    []map[string]string `json:"rows"`
}

type exportMarkdownOutcome struct {
	Output string   `json:"output"`
	Error  string   `json:"error"`
	Links  []string `json:"links"`
}

// TestPortRenderExportMarkdownFixture captures the pure Markdown export
// helpers (search result sets, view tables, plain wikilinks) from Go itself.
// Regenerate deliberately with:
//
//	PORT_RENDER_EXPORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderExportMarkdownFixture$'
func TestPortRenderExportMarkdownFixture(t *testing.T) {
	fixture := buildExportMarkdownFixture(t)
	encoded, err := encodeExportMarkdownFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExportMarkdownFixture(encoded, fixture); err != nil {
		t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
	}
	var mutated exportMarkdownFixture
	if err := json.Unmarshal(encoded, &mutated); err != nil {
		t.Fatal(err)
	}
	mutated.Cases[0].Want.Output += "mutated"
	mutatedBytes, err := encodeExportMarkdownFixture(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExportMarkdownFixture(mutatedBytes, fixture); err == nil || !strings.Contains(err.Error(), "differs from Go") {
		t.Fatalf("output mutation control = %v, want Go-output rejection", err)
	}

	path := filepath.Join(exportMarkdownRepoRoot(t), filepath.FromSlash(exportMarkdownFixturePath))
	if os.Getenv("PORT_RENDER_EXPORT_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s with %d Go %s cases", exportMarkdownFixturePath, len(fixture.Cases), runtime.Version())
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_EXPORT_GENERATE=1): %v", path, err)
	}
	if err := validateExportMarkdownFixture(current, fixture); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s: %d Go %s cases", exportMarkdownFixturePath, len(fixture.Cases), runtime.Version())
}

func buildExportMarkdownFixture(t *testing.T) exportMarkdownFixture {
	t.Helper()
	if runtime.Version() != noteHTMLOracleGoVersion {
		t.Fatalf("export fixture oracle requires Go %s, running %s", noteHTMLOracleGoVersion, runtime.Version())
	}
	root := exportMarkdownRepoRoot(t)
	sources := make(map[string]string, len(exportMarkdownOracleSources))
	for rel, want := range exportMarkdownOracleSources {
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // pinned source file
		if err != nil {
			t.Fatalf("read pinned Go export oracle source: %v", err)
		}
		sum := sha256.Sum256(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")))
		got := hex.EncodeToString(sum[:])
		if got != want {
			t.Fatalf("Go oracle source %s changed: got %s, want pinned %s", rel, got, want)
		}
		sources[rel] = got
	}
	fixture := exportMarkdownFixture{
		SchemaVersion: 1,
		Oracle:        exportMarkdownOracle{Commit: fixtureoracle.Current().Commit, GoVersion: runtime.Version(), Sources: sources},
		Cases:         exportMarkdownCorpus(),
	}
	for i := range fixture.Cases {
		fixture.Cases[i].Want = runExportMarkdownCase(fixture.Cases[i])
	}
	return fixture
}

func runExportMarkdownCase(c exportMarkdownCase) exportMarkdownOutcome {
	var out exportMarkdownOutcome
	switch {
	case c.Search != nil:
		hits := make([]SearchHit, len(c.Search.Hits))
		for i, h := range c.Search.Hits {
			hits[i] = SearchHit(h)
		}
		rendered, err := SearchResults(c.Search.Query, c.Search.Title, c.Search.Created, hits, c.Search.Format)
		out.Output = string(rendered)
		if err != nil {
			out.Error = err.Error()
		}
	case c.Table != nil:
		var b strings.Builder
		writeMarkdownTable(&b, c.Table.Columns, c.Table.Rows)
		out.Output = b.String()
	case c.Links != nil:
		out.Links = PlainLinks(*c.Links)
	}
	if out.Links == nil {
		out.Links = []string{}
	}
	return out
}

func exportMarkdownCorpus() []exportMarkdownCase {
	const created = "2026-10-19T08:00:00Z"
	search := func(id, query, title, format string, hits ...exportSearchHitInput) exportMarkdownCase {
		return exportMarkdownCase{ID: "search-" + id, Search: &exportSearchInput{Query: query, Title: title, Created: created, Format: format, Hits: hits}}
	}
	hit := exportSearchHitInput{Path: "notes/Alpha Beta.md", Title: "Alpha", Snippet: "first line\nsecond line", Score: 0.5}
	anchored := hit
	anchored.Anchor = &retrieval.LocationAnchor{Kind: "page", Value: "3"}
	anchored.MetadataMatches = []string{"tags", "au`thor"}
	hostile := exportSearchHitInput{
		Path:    "dir/[[x]]|y#z/&ü %?.md",
		Title:   "## \r\n#Heading\nwith break",
		Snippet: "crlf\r\nline\n\n> nested",
		Score:   -0.0000005,
		Anchor:  &retrieval.LocationAnchor{Kind: "heading", Value: "A/B [c]|d ü&`e`"},
	}
	halfAnchor := hit
	halfAnchor.Anchor = &retrieval.LocationAnchor{Kind: "page"}
	halfAnchor.Title = "   "
	halfAnchor.Score = 123456.7890125
	links := func(id, body string) exportMarkdownCase { return exportMarkdownCase{ID: "links-" + id, Links: &body} }
	table := func(id string, columns []string, rows ...map[string]string) exportMarkdownCase {
		return exportMarkdownCase{ID: "table-" + id, Table: &exportTableInput{Columns: columns, Rows: rows}}
	}
	return []exportMarkdownCase{
		search("empty-markdown", "q", "", "markdown"),
		search("empty-md-alias", "", "  ", " MD "),
		search("empty-pdf", "q", "Report", "PDF"),
		search("one-hit", "alpha", "Results", "markdown", hit),
		search("one-hit-pdf", "alpha", "Results", "pdf", hit),
		search("many-hits", "x", "Many", "markdown", hit, anchored, hostile, halfAnchor),
		search("unsupported-format", "q", "t", "html"),
		search("unsupported-unicode-format", "q", "t", "İΣ\u200b\t"),
		search("yaml-keyword-title", "true", "null", "markdown"),
		search("yaml-number-title", "1e3", "0x1F", "markdown"),
		search("yaml-indicator-title", "- dash", "#hash: colon", "markdown"),
		search("yaml-quote-title", "it's \"quoted\" \\ back", "'single' and \"double\"", "markdown"),
		search("yaml-space-title", " lead", "trail ", "markdown"),
		search("yaml-multiline-title", "line1\nline2", "first\nsecond\n", "markdown"),
		search("yaml-timestamp-title", "2026-10-19", "2026-10-19", "markdown"),
		search("yaml-control-query", "\x00\x07\x08\t\v\f\r\x1b\x7f\u0080\u0085\u00a0\u2028\u2029\ufeff\ufffe\U0001f600é", "ctl\u0085x", "markdown"),
		search("yaml-supplementary-title", "q", "Notiz \U0001f600 \u00e9", "markdown"),
		search("yaml-tab-title", "tab\tq", "tab\ttitle", "markdown"),
		search("yaml-long-query", strings.Repeat("word ", 40)+"end", strings.Repeat("title ", 30)+"end", "markdown"),
		search("backtick-query", "a`b\r\nc", "T", "markdown"),
		table("empty", nil),
		table("sorted-extra-columns", []string{"title", "path", "title"},
			map[string]string{"path": "a.md", "title": "A|B", "zeta": "z"},
			map[string]string{"path": "b.md", "alpha": "x\ny"}),
		table("no-columns-row", nil, map[string]string{}),
		table("unicode-columns", []string{"Ä", "a", "B"}, map[string]string{"Ä": "ä", "a": "||"}),
		links("none", "no links here"),
		links("dedupe-and-strip", "[[A|label]] [[A#part]] [[ B ]] ![[Embed]] [[#only]] [[|empty]]"),
		links("unterminated", "[[open [[inner]] ]]"),
		links("unicode", "[[Über Notiz]] [[日本#見出し|ラベル]]"),
	}
}

func encodeExportMarkdownFixture(fixture exportMarkdownFixture) ([]byte, error) {
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fixture: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateExportMarkdownFixture(encoded []byte, expected exportMarkdownFixture) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual exportMarkdownFixture
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
		got, err := json.Marshal(c.Want)
		if err != nil {
			return err
		}
		want, err := json.Marshal(runExportMarkdownCase(c))
		if err != nil {
			return err
		}
		if c.ID != expected.Cases[i].ID || !bytes.Equal(got, want) {
			return fmt.Errorf("case %q output differs from Go", c.ID)
		}
	}
	canonical, err := encodeExportMarkdownFixture(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}

func exportMarkdownRepoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(filepath.Dir(noteHTMLFixtureSourcePath(t)), "..", ".."))
}
