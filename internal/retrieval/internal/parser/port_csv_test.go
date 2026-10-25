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

type csvSectionsFixture struct {
	SchemaVersion int                      `json:"schema_version"`
	MaxFileBytes  int64                    `json:"max_file_bytes"`
	Cases         []csvSectionsFixtureCase `json:"cases"`
}

type csvSectionsFixtureCase struct {
	ID          string                     `json:"id"`
	InputBase64 string                     `json:"input_base64"`
	Sections    []csvSectionsFixtureResult `json:"sections"`
	Error       string                     `json:"error,omitempty"`
	// SizeLimit marks the case whose input is MaxFileBytes+1 bytes of 'a'.
	// The bytes are not stored; the Rust replay rebuilds them.
	SizeLimit bool `json:"size_limit,omitempty"`
}

type csvSectionsFixtureResult struct {
	TextBase64  string `json:"text_base64"`
	Text        string `json:"text,omitempty"`
	ValidUTF8   bool   `json:"valid_utf8"`
	Start       int    `json:"start"`
	AnchorKind  string `json:"anchor_kind"`
	AnchorValue string `json:"anchor_value"`
	Synthetic   bool   `json:"synthetic"`
}

type csvSectionsInput struct {
	id    string
	input []byte
}

func csvSectionsInputs() []csvSectionsInput {
	long := strings.Repeat("x", 5000)
	return []csvSectionsInput{
		{"empty-file", nil},
		{"whitespace-only", []byte("  \n\t\n")},
		{"simple-lf", []byte("a,b\n1,2\n")},
		{"simple-crlf", []byte("a,b\r\n1,2\r\n")},
		{"no-trailing-newline", []byte("a,b\n1,2")},
		{"trailing-lone-cr", []byte("a,b\r")},
		{"blank-lines-skipped", []byte("a\n\n\nb\r\n\r\nc\n")},
		{"quoted-comma", []byte("\"x,y\",z\n")},
		{"quoted-newline", []byte("\"line1\nline2\",b\n")},
		{"quoted-crlf-normalized", []byte("\"a\r\nb\",c\r\n")},
		{"escaped-quotes", []byte("\"say \"\"hi\"\"\",b\n")},
		{"ragged-rows", []byte("a,b,c\n1\n1,2,3,4\n")},
		{"empty-fields", []byte(",,\n")},
		{"empty-quoted-fields", []byte("\"\",\"\"\n")},
		{"trailing-comma", []byte("a,\n")},
		{"bare-quote-keeps-prior-rows", []byte("a,b\nc\"d,e\nf,g\n")},
		{"quote-then-garbage", []byte("ok,1\n\"a\"b,c\n1,2\n")},
		{"unterminated-quote-at-eof", []byte("a,b\n\"open,1\n2,3\n")},
		{"unterminated-quote-no-newline", []byte("a,\"b")},
		{"unterminated-quote-first-row", []byte("\"abc\n")},
		{"leading-space-before-quote", []byte("a, \"b\"\n")},
		{"newline-only-quoted-field", []byte("\"\n\"\n")},
		{"quoted-field-at-eof", []byte("\"a\"")},
		{"utf8-bom-is-content", append([]byte{0xEF, 0xBB, 0xBF}, []byte("a,b\n")...)},
		{"unicode-fields", []byte("ü,ß\n日本,語\n")},
		{"tab-inside-field", []byte("a\tb,c\n")},
		{"lone-cr-inside-field", []byte("a\rb,c\n")},
		{"nul-byte-inside-field", []byte("a\x00b,c\n")},
		{"outer-whitespace-trimmed", []byte(" a , b \n")},
		{"unicode-space-trimmed", []byte("\u00a0x\u3000\n")},
		{"line-longer-than-reader-buffer", []byte(long + ",y\nz\n")},
		{"quoted-field-longer-than-reader-buffer", []byte("\"" + long + "\n" + long + "\",y\nz\n")},
		{"many-rows", []byte(strings.Repeat("k,v\n", 2500))},
		{"invalid-utf8-field", []byte("a,\xff\n")},
	}
}

// TestRetrievalCSVSectionsFixture drives the shipped Go ParseFileSections
// route for .csv (encoding/csv with FieldsPerRecord=-1, tab-joined rows, the
// first read error silently ends the file). Only the Go parser produces the
// expected sections; the Rust replay compares them byte for byte.
func TestRetrievalCSVSectionsFixture(t *testing.T) {
	dir := t.TempDir()
	fixture := csvSectionsFixture{SchemaVersion: 1, MaxFileBytes: MaxIndexFileSize}
	for _, input := range csvSectionsInputs() {
		fixture.Cases = append(fixture.Cases, runCSVSectionsCase(t, dir, input.id, input.input, false))
	}
	oversize := bytes.Repeat([]byte("a"), MaxIndexFileSize+1)
	sizeCase := runCSVSectionsCase(t, dir, "size-limit-exceeded", oversize, true)
	sizeCase.InputBase64 = ""
	fixture.Cases = append(fixture.Cases, sizeCase)

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(file), "../../../../testdata/port/retrieval/csv-sections.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	fixtureRoot, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixtureRoot.Close(); err != nil {
			t.Errorf("close fixture root: %v", err)
		}
	})
	current, err := fixtureRoot.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("CSV sections fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./internal/retrieval/internal/parser -run '^TestRetrievalCSVSectionsFixture$'")
	}
}

func runCSVSectionsCase(t *testing.T, dir, id string, input []byte, sizeLimit bool) csvSectionsFixtureCase {
	t.Helper()
	path := filepath.Join(dir, id+".csv")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	result := csvSectionsFixtureCase{
		ID:          id,
		InputBase64: base64.StdEncoding.EncodeToString(input),
		SizeLimit:   sizeLimit,
		Sections:    []csvSectionsFixtureResult{},
	}
	sections, err := ParseFileSections(path)
	if err != nil {
		result.Error = strings.ReplaceAll(err.Error(), path, "$PATH")
		return result
	}
	for _, section := range sections {
		entry := csvSectionsFixtureResult{
			TextBase64:  base64.StdEncoding.EncodeToString([]byte(section.Text)),
			ValidUTF8:   utf8.ValidString(section.Text),
			Start:       section.Start,
			AnchorKind:  section.Anchor.Kind,
			AnchorValue: section.Anchor.Value,
			Synthetic:   section.Synthetic,
		}
		if entry.ValidUTF8 {
			entry.Text = section.Text
		}
		result.Sections = append(result.Sections, entry)
	}
	return result
}
