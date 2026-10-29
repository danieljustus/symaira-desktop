package parser

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

type extractionInput struct {
	id, extension string
	data          []byte
	oversized     bool
}
type extractionCase struct {
	ID            string                     `json:"id"`
	Extension     string                     `json:"extension"`
	Input         string                     `json:"input_base64"`
	Oversized     bool                       `json:"oversized,omitempty"`
	Text          string                     `json:"text_base64"`
	Error         string                     `json:"error,omitempty"`
	Sections      []csvSectionsFixtureResult `json:"sections"`
	SectionsError string                     `json:"sections_error,omitempty"`
}

func extractionInputs() []extractionInput {
	var inputs []extractionInput
	for _, ext := range []string{".html", ".htm"} {
		for _, s := range []struct{ id, body string }{
			{"empty", ""}, {"entities", "<head><title>hidden</title></head><body><h1>A &amp; B</h1><p>one&nbsp;two &#x65e5;</p><script>bad()</script><style>bad{}</style><div>end<br>line</div>"},
			{"inline", "<p><span>a</span> <b>b</b></p>\n  <p> c\r\nd </p>"},
			{"repair", "<p>first<p>second<table>foster<tr><td>cell</table>end"},
			{"comments", "<!-- hidden --><div>visible</div><noscript>fallback</noscript>"},
		} {
			inputs = append(inputs, extractionInput{s.id + ext, ext, []byte(s.body), false})
		}
	}
	for _, s := range []struct{ id, body string }{
		{"empty", ""}, {"paragraphs", `{\rtf1\ansi Hello\par world\line next\page last\tab cell}`},
		{"destinations", `{\rtf1{\fonttbl hidden}{\colortbl hidden}{\stylesheet hidden}{\*\unknown hidden}Visible {\b bold} end}`},
		{"escaped", `{\rtf1 a\\b\{c\} \'e9 \u1234?}`},
		{"malformed", "}a{b\\\n c\\"}, {"unicode", "{\\rtf1 日本\r\nü}"},
		{"nested-ignore", `{\fonttbl x{\* y}z}tail`},
	} {
		inputs = append(inputs, extractionInput{s.id + ".rtf", ".rtf", []byte(s.body), false})
	}
	for _, ext := range []string{".html", ".rtf"} {
		inputs = append(inputs, extractionInput{"oversized" + ext, ext, bytes.Repeat([]byte("a"), MaxIndexFileSize+1), true})
	}
	return inputs
}

// The corpus is synthesized here; all expected text and sections come only
// from the production ParseFile / ParseFileSections entrypoints.
func TestRetrievalExtractionFixture(t *testing.T) {
	dir := t.TempDir()
	cases := []extractionCase{}
	for _, in := range append(append(extractionInputs(), archiveExtractionInputs(t)...), pdfExtractionInputs()...) {
		path := filepath.Join(dir, in.id)
		if err := os.WriteFile(path, in.data, 0600); err != nil {
			t.Fatal(err)
		}
		c := extractionCase{ID: in.id, Extension: in.extension, Input: base64.StdEncoding.EncodeToString(in.data), Oversized: in.oversized, Sections: []csvSectionsFixtureResult{}}
		if in.oversized {
			c.Input = ""
		}
		text, err := ParseFile(path)
		c.Text = base64.StdEncoding.EncodeToString([]byte(text))
		if err != nil {
			c.Error = strings.ReplaceAll(err.Error(), path, "$PATH")
		}
		sections, err := ParseFileSections(path)
		if err != nil {
			c.SectionsError = strings.ReplaceAll(err.Error(), path, "$PATH")
		}
		for _, s := range sections {
			c.Sections = append(c.Sections, csvSectionsFixtureResult{TextBase64: base64.StdEncoding.EncodeToString([]byte(s.Text)), ValidUTF8: utf8.ValidString(s.Text), Start: s.Start, AnchorKind: s.Anchor.Kind, AnchorValue: s.Anchor.Value, Synthetic: s.Synthetic})
		}
		cases = append(cases, c)
	}
	encoded, err := json.MarshalIndent(struct {
		Schema int              `json:"schema_version"`
		Cases  []extractionCase `json:"cases"`
	}{1, cases}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "../../../../testdata/port/retrieval/document-extraction.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("document extraction fixture stale: run PORT_GENERATE=1 go test ./internal/retrieval/internal/parser -run '^TestRetrievalExtractionFixture$'")
	}
}
