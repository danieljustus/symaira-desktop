// Command renderdslgen freezes the production Go SymDraw DSL parser contract
// (internal/draw/parse: Source, ParseMermaid, ParseJSON) as a Rust replay
// fixture. Every case records the exact accepted IR bytes (json.Marshal of the
// parsed ir.Diagram) or the complete typed ParseError, including the
// err.Error() string. It also freezes the Go Unicode tables the parser's
// strings/regexp/%q semantics depend on, so the Rust port is table-driven
// rather than approximating Go's unicode package.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOutput  = "testdata/port/render/dsl-parse.json"
	defaultTables  = "crates/symdesk-render/src/draw/go_unicode.rs"
	minedMinLength = 4
)

// minedSources are the Go test files whose string literals are replayed
// through the production parser, so every construct the Go tests exercise is
// part of the differential without hand-copying.
var minedSources = []string{
	"internal/draw/parse/errors_test.go",
	"internal/draw/parse/json_test.go",
	"internal/draw/parse/mermaid_test.go",
	"internal/draw/parse/sequence_test.go",
	"internal/draw/ir/ir_test.go",
	"internal/draw/draw_test.go",
	"internal/draw/golden_test.go",
	"internal/draw/sequence_pipeline_test.go",
}

type fixture struct {
	SchemaVersion int        `json:"schema_version"`
	Oracle        oracle     `json:"oracle"`
	Entries       []string   `json:"entries"`
	Cases         []testCase `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type testCase struct {
	ID          string     `json:"id"`
	Entry       string     `json:"entry"`
	Input       string     `json:"input"`
	Accepted    bool       `json:"accepted"`
	DiagramJSON string     `json:"diagram_json,omitempty"`
	Error       *errorCase `json:"error,omitempty"`
}

type errorCase struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint"`
	Line    int    `json:"line"`
	Text    string `json:"text"`
}

func main() {
	output := flag.String("output", defaultOutput, "fixture path")
	tables := flag.String("tables", defaultTables, "generated Go Unicode table path")
	check := flag.Bool("check", false, "fail if fixture or table differs")
	commit := flag.String("oracle-commit", fixtureoracle.Defaults().Commit, "Go oracle commit")
	release := flag.String("oracle-release", fixtureoracle.Defaults().Release, "Go oracle release")
	flag.Parse()
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		fatal("resolve fixture source: %v", err)
	}
	if err := fixtureoracle.ValidateSource(root, inventory.Oracle{Commit: *commit, Release: *release}); err != nil {
		fatal("verify selected fixture source: %v", err)
	}

	result := fixture{SchemaVersion: 1, Oracle: oracle{Commit: *commit, Release: *release}, Entries: []string{"source", "mermaid", "json"}}
	seen := map[string]bool{}
	add := func(id, entry, input string) {
		if !utf8.ValidString(input) {
			// The fixture is JSON text, which cannot carry invalid UTF-8
			// faithfully; refuse rather than silently substitute.
			fatal("%s: input is not valid UTF-8", id)
		}
		key := entry + "\x00" + input
		if seen[key] {
			return
		}
		seen[key] = true
		result.Cases = append(result.Cases, run(id, entry, input))
	}
	for _, item := range handCorpus() {
		add(item.id, item.entry, item.input)
	}
	for _, item := range minedCorpus(root) {
		add(item.id, item.entry, item.input)
	}
	for _, item := range generatedCorpus(result.Cases) {
		add(item.id, item.entry, item.input)
	}
	ids := map[string]bool{}
	for _, c := range result.Cases {
		if ids[c.ID] {
			fatal("duplicate case id %s", c.ID)
		}
		ids[c.ID] = true
	}

	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal("marshal fixture: %v", err)
	}
	content = append(content, '\n')
	tableSource := renderTables()
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil || !bytes.Equal(current, content) {
			fatal("render DSL parse fixture is stale; regenerate deliberately")
		}
		currentTables, err := os.ReadFile(*tables)
		if err != nil || !bytes.Equal(currentTables, []byte(tableSource)) {
			fatal("render DSL Go Unicode table is stale; regenerate deliberately")
		}
		fmt.Printf("PASS render DSL parse fixture (%d cases)\n", len(result.Cases))
		return
	}
	for path, data := range map[string][]byte{*output: content, *tables: []byte(tableSource)} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			fatal("create directory: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			fatal("write %s: %v", path, err)
		}
	}
	fmt.Printf("PASS render DSL parse fixture generated (%d cases)\n", len(result.Cases))
}

func run(id, entry, input string) testCase {
	entryCase := testCase{ID: id, Entry: entry, Input: input}
	var diag any
	var err error
	switch entry {
	case "source":
		d, e := parse.Source(input)
		diag, err = d, e
		if e != nil {
			diag = nil
		}
	case "mermaid":
		d, e := parse.ParseMermaid(input)
		diag, err = d, e
		if e != nil {
			diag = nil
		}
	case "json":
		d, e := parse.ParseJSON([]byte(input))
		diag, err = d, e
		if e != nil {
			diag = nil
		}
	default:
		fatal("%s: unknown entry %s", id, entry)
	}
	if err != nil {
		var parseErr *parse.ParseError
		if !errors.As(err, &parseErr) {
			fatal("%s: expected typed ParseError, got %T: %v", id, err, err)
		}
		entryCase.Error = &errorCase{Stage: parseErr.Stage, Message: parseErr.Message, Detail: parseErr.Detail, Hint: parseErr.Hint, Line: parseErr.Line, Text: parseErr.Error()}
		return entryCase
	}
	encoded, err := json.Marshal(diag)
	if err != nil {
		fatal("%s: marshal Go oracle result: %v", id, err)
	}
	entryCase.Accepted = true
	entryCase.DiagramJSON = string(encoded)
	return entryCase
}

type corpusCase struct{ id, entry, input string }

// minedCorpus returns every string literal from the Go parser tests, in a
// deterministic order, as Source-entry cases.
func minedCorpus(root string) []corpusCase {
	var out []corpusCase
	dedupe := map[string]bool{}
	for _, rel := range minedSources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			fatal("parse %s: %v", rel, err)
		}
		var literals []string
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !utf8.ValidString(value) || len(strings.TrimSpace(value)) < minedMinLength {
				return true
			}
			literals = append(literals, value)
			return true
		})
		base := strings.TrimSuffix(filepath.Base(rel), "_test.go")
		n := 0
		for _, value := range literals {
			if dedupe[value] {
				continue
			}
			dedupe[value] = true
			out = append(out, corpusCase{fmt.Sprintf("mined-%s-%03d", base, n), "source", value})
			n++
		}
	}
	return out
}

func handCorpus() []corpusCase {
	var out []corpusCase
	add := func(id, entry, input string) { out = append(out, corpusCase{id, entry, input}) }
	named := func(prefix string, entry string, inputs map[string]string) {
		keys := make([]string, 0, len(inputs))
		for k := range inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(prefix+"-"+k, entry, inputs[k])
		}
	}

	// --- Existing JSON IR corpus (kept identical to renderirgen) ---
	named("ir", "json", map[string]string{
		"valid-graph":          `{"kind":"graph","direction":"LR","title":"Services","width":800,"height":400,"nodes":[{"id":"api","label":"API","shape":"round","width":120,"height":48,"style":{"fill":"#fff","stroke":"#333","stroke_width":2,"text_color":"#111","opacity":0.9,"dash_array":"2 2"}},{"id":"db","label":"Store","shape":"cylinder"}],"edges":[{"from":"api","to":"db","label":"writes","style":"dashed","arrow":"single","color":"#666"}],"groups":[{"id":"backend","label":"Backend","members":["api","db"],"style":{"fill":"#eee","stroke":"#ccc","stroke_width":1}}]}`,
		"valid-chart":          `{"kind":"chart","chart":{"type":"bar","title":"Usage","legend":true,"series":[{"name":"Requests","color":"blue","data":[{"label":"Jan","y":12},{"x":2,"y":18,"label":"Feb","color":"green"}]}],"x_axis":{"title":"Month","labels":["Jan","Feb"],"min":0,"max":2},"y_axis":{"title":"Count","min":0}}}`,
		"empty":                ``,
		"malformed":            `{"kind":"graph",}`,
		"unknown-top-level":    `{"kind":"graph","unsupported":true}`,
		"unknown-nested-style": `{"kind":"graph","nodes":[{"id":"a","style":{"glow":true}}]}`,
		"invalid-kind":         `{"kind":"unknown"}`,
		"duplicate-id":         `{"kind":"graph","nodes":[{"id":"a"},{"id":"a"}]}`,
		"dangling-edge":        `{"kind":"graph","nodes":[{"id":"a"}],"edges":[{"from":"a","to":"missing"}]}`,
		"negative-dimension":   `{"kind":"graph","nodes":[{"id":"a","width":-1}]}`,
	})

	// --- JSON: syntax, type, and decoder-quirk errors ---
	named("json", "json", map[string]string{
		"whitespace":          " \n\t ",
		"empty-object":        `{}`,
		"null":                `null`,
		"array":               `[]`,
		"number":              `5`,
		"string":              `"a"`,
		"true":                `true`,
		"object-open":         `{`,
		"unterminated-string": `{"kind":"gra`,
		"trailing-comma":      `{"kind":"graph",}`,
		"missing-colon":       `{"kind" "graph"}`,
		"missing-comma":       `{"kind":"graph" "title":"x"}`,
		"bad-literal":         `{"kind":tru}`,
		"bad-literal-null":    `{"kind":nul}`,
		"leading-zero":        `{"width":01}`,
		"dot-no-digit":        `{"width":1.}`,
		"exp-no-digit":        `{"width":1e}`,
		"minus-only":          `{"width":-}`,
		"bad-escape":          `{"kind":"\x"}`,
		"bad-unicode-escape":  `{"kind":"\u12G4"}`,
		"control-in-string":   "{\"kind\":\"a\nb\"}",
		"tab-in-string":       "{\"kind\":\"a\tb\"}",
		"nul-in-string":       "{\"kind\":\"a\x00b\"}",
		"bom":                 "\ufeff{\"kind\":\"graph\"}",
		"trailing-garbage":    `{"kind":"graph"} garbage`,
		"trailing-object":     `{"kind":"graph"}{"kind":"x"}`,
		"trailing-comma-arr":  `{"kind":"graph","nodes":[{"id":"a"},]}`,
		"unclosed-array":      `{"kind":"graph","nodes":[{"id":"a"}`,
		"close-bracket":       `{"kind":"graph"]`,
		"nested-unclosed":     `{"kind":"graph","custom":{"a":[1,2,{"b":`,
		"key-not-string":      `{kind:"graph"}`,
		"single-quote":        `{'kind':'graph'}`,
		"unicode-bad-char":    `{"kind":"graph"}é`,
		"unicode-key-bad":     `{é:1}`,
		"type-kind-number":    `{"kind":5}`,
		"type-width-string":   `{"kind":"graph","width":"x"}`,
		"type-title-bool":     `{"kind":"graph","title":true}`,
		"type-title-obj":      `{"kind":"graph","title":{}}`,
		"type-title-arr":      `{"kind":"graph","title":[]}`,
		"type-nodes-object":   `{"kind":"graph","nodes":{}}`,
		"type-nodes-number":   `{"kind":"graph","nodes":5}`,
		"type-node-number":    `{"kind":"graph","nodes":[1]}`,
		"type-node-id-num":    `{"kind":"graph","nodes":[{"id":1}]}`,
		"type-node-width-str": `{"kind":"graph","nodes":[{"id":"a","width":"a"}]}`,
		"type-style-opacity":  `{"kind":"graph","nodes":[{"id":"a","style":{"opacity":"x"}}]}`,
		"type-style-array":    `{"kind":"graph","nodes":[{"id":"a","style":[]}]}`,
		"type-edge-from":      `{"kind":"graph","edges":[{"from":1,"to":"a"}]}`,
		"type-group-members":  `{"kind":"graph","groups":[{"members":"a"}]}`,
		"type-group-member-n": `{"kind":"graph","groups":[{"members":[1]}]}`,
		"type-chart-string":   `{"kind":"chart","chart":"x"}`,
		"type-chart-legend":   `{"kind":"chart","chart":{"type":"bar","legend":"yes","series":[{"name":"a","data":[]}]}}`,
		"type-series-data":    `{"kind":"chart","chart":{"type":"bar","series":[{"name":"a","data":[{"y":"q"}]}]}}`,
		"type-axis-min":       `{"kind":"chart","chart":{"type":"bar","series":[{"name":"a","data":[]}],"x_axis":{"min":"a"}}}`,
		"type-axis-labels":    `{"kind":"chart","chart":{"type":"bar","series":[{"name":"a","data":[]}],"y_axis":{"labels":[1]}}}`,
		"type-custom-array":   `{"kind":"custom","custom":[]}`,
		"type-custom-string":  `{"kind":"custom","custom":"x"}`,
		"overflow-width":      `{"kind":"graph","width":1e999}`,
		"overflow-neg":        `{"kind":"graph","width":-1e999}`,
		"underflow-width":     `{"kind":"graph","width":1e-999}`,
		"first-error-wins":    `{"kind":"graph","bogus":1,"width":"x"}`,
		"type-then-unknown":   `{"kind":"graph","width":"x","bogus":1}`,
		"unknown-in-chart":    `{"kind":"chart","chart":{"type":"bar","wat":1,"series":[{"name":"a","data":[]}]}}`,
		"unknown-in-series":   `{"kind":"chart","chart":{"type":"bar","series":[{"name":"a","data":[],"zz":1}]}}`,
		"unknown-in-point":    `{"kind":"chart","chart":{"type":"bar","series":[{"name":"a","data":[{"y":1,"zz":1}]}]}}`,
		"unknown-in-axis":     `{"kind":"chart","chart":{"type":"bar","series":[{"name":"a","data":[]}],"x_axis":{"zz":1}}}`,
		"unknown-in-edge":     `{"kind":"graph","edges":[{"from":"a","to":"b","zz":1}]}`,
		"unknown-in-group":    `{"kind":"graph","groups":[{"members":["a"],"zz":1}]}`,
		"unknown-group-style": `{"kind":"graph","groups":[{"members":["a"],"style":{"zz":1}}]}`,
		"unknown-key-quote":   `{"kind":"graph","a\"b":1}`,
		"unknown-key-unicode": `{"kind":"graph","ключ":1}`,
		"unknown-key-escape":  `{"kind":"graph","\u0041":1}`,
		"unknown-contains":    `{"kind":"graph","unknown field x":1}`,
		"case-insens-kind":    `{"KIND":"graph"}`,
		"case-insens-nested":  `{"Kind":"graph","NODES":[{"ID":"a","Style":{"STROKE_WIDTH":3}}]}`,
		"case-kelvin":         "{\"Kind\":\"graph\",\"\u212Aind\":\"tree\"}",
		"case-long-s":         "{\"kind\":\"graph\",\"\u017Ftyle\":1}",
		"dup-keys-last-wins":  `{"kind":"graph","kind":"tree","title":"a","title":"b"}`,
		"dup-keys-obj-merge":  `{"kind":"graph","nodes":[{"id":"a","style":{"fill":"x"}}],"nodes":[{"width":2}]}`,
		"dup-keys-chart":      `{"kind":"chart","chart":{"type":"bar","title":"t"},"chart":{"series":[{"name":"a","data":[]}]}}`,
		"dup-style-merge":     `{"kind":"graph","nodes":[{"id":"a","style":{"fill":"x"},"style":{"stroke":"y"}}]}`,
		"dup-custom-merge":    `{"kind":"custom","custom":{"a":1,"b":2},"custom":{"b":3,"c":4}}`,
		"null-fields":         `{"kind":"graph","title":null,"nodes":null,"chart":null,"custom":null,"width":null}`,
		"null-node-element":   `{"kind":"graph","nodes":[null,{"id":"a"}]}`,
		"null-point-opacity":  `{"kind":"graph","nodes":[{"id":"a","style":{"opacity":null,"stroke_width":null}}]}`,
		"surrogate-lone":      `{"kind":"graph","title":"\ud800x"}`,
		"surrogate-pair":      `{"kind":"graph","title":"\ud83d\ude00"}`,
		"surrogate-low-only":  `{"kind":"graph","title":"\udc00"}`,
		"escapes":             `{"kind":"graph","title":"a\"b\\c\/d\b\f\n\r\t\u0001\u007f"}`,
		"html-chars":          `{"kind":"graph","title":"<a href='x'>&amp;</a>\u2028\u2029"}`,
		"unicode-title":       `{"kind":"graph","title":"Größe 日本語 😀 é"}`,
		"numbers":             `{"kind":"graph","width":1e3,"height":-0,"nodes":[{"id":"a","x":1E+2,"y":0.1},{"id":"b","x":1e21,"y":1e-7},{"id":"c","x":123456789012345678901234567890,"y":1.5e300},{"id":"d","x":0.000001,"y":0.0000001},{"id":"e","x":100000000000000000000,"y":4.9e-324},{"id":"f","x":1.7976931348623157e308,"y":5e-7}]}`,
		"number-opacity":      `{"kind":"graph","nodes":[{"id":"a","style":{"opacity":0,"stroke_width":0.5}}]}`,
		"custom-nested":       `{"kind":"custom","custom":{"z":[1,"a",true,null,{"b":2.5,"a":[]}],"a":{"y":1e21,"x":"<&>"},"n":-0.0,"s":"é\u2028"}}`,
		"custom-empty":        `{"kind":"custom","custom":{}}`,
		"sequence-kind":       `{"kind":"sequence","custom":{"actors":[{"ID":"A"}],"messages":[]}}`,
		"timeline-kind":       `{"kind":"timeline"}`,
		"tree-kind":           `{"kind":"tree","nodes":[{"id":"r"},{"id":"c"}],"edges":[{"from":"r","to":"c"}]}`,
		"all-shapes":          `{"kind":"graph","nodes":[{"id":"1","shape":"rect"},{"id":"2","shape":"round"},{"id":"3","shape":"circle"},{"id":"4","shape":"cylinder"},{"id":"5","shape":"diamond"},{"id":"6","shape":"pill"},{"id":"7","shape":"stadium"},{"id":"8","shape":"subroutine"},{"id":"9","shape":"hexagon"},{"id":"10","shape":"asymmetric"}]}`,
		"bad-shape":           `{"kind":"graph","nodes":[{"id":"a","shape":"blob"}]}`,
		"all-directions":      `{"kind":"graph","direction":"TD"}`,
		"dir-TB":              `{"kind":"graph","direction":"TB"}`,
		"dir-BT":              `{"kind":"graph","direction":"BT"}`,
		"dir-RL":              `{"kind":"graph","direction":"RL"}`,
		"bad-direction":       `{"kind":"graph","direction":"lr"}`,
		"missing-kind":        `{"title":"x"}`,
		"kind-empty-string":   `{"kind":""}`,
		"node-empty-id":       `{"kind":"graph","nodes":[{"id":""}]}`,
		"node-blank-id":       `{"kind":"graph","nodes":[{"id":"  "}]}`,
		"node-missing-id":     `{"kind":"graph","nodes":[{"label":"x"}]}`,
		"dup-id-trimmed":      `{"kind":"graph","nodes":[{"id":"a"},{"id":" a "}]}`,
		"node-neg-height":     `{"kind":"graph","nodes":[{"id":"a","height":-0.5}]}`,
		"edge-empty-from":     `{"kind":"graph","edges":[{"from":"","to":"a"}]}`,
		"edge-empty-to":       `{"kind":"graph","edges":[{"from":"a","to":" "}]}`,
		"edge-no-nodes":       `{"kind":"graph","edges":[{"from":"a","to":"b"}]}`,
		"edge-missing-from":   `{"kind":"graph","nodes":[{"id":"a"}],"edges":[{"from":"z","to":"a"}]}`,
		"edge-trimmed-ref":    `{"kind":"graph","nodes":[{"id":"a"},{"id":"b"}],"edges":[{"from":" a ","to":"b"}]}`,
		"edge-bad-style":      `{"kind":"graph","edges":[{"from":"a","to":"b","style":"wavy"}]}`,
		"edge-bad-arrow":      `{"kind":"graph","edges":[{"from":"a","to":"b","arrow":"big"}]}`,
		"edge-all-kinds":      `{"kind":"graph","nodes":[{"id":"a"},{"id":"b"}],"edges":[{"from":"a","to":"b","style":"solid","arrow":"none"},{"from":"a","to":"b","style":"dotted","arrow":"double"},{"from":"a","to":"b","style":"thick","arrow":"cross"},{"from":"a","to":"b","arrow":"circle","color":"red"}]}`,
		"group-empty-members": `{"kind":"graph","groups":[{"members":[]}]}`,
		"group-no-members":    `{"kind":"graph","groups":[{"id":"g"}]}`,
		"group-missing-node":  `{"kind":"graph","nodes":[{"id":"a"}],"groups":[{"members":["a","q"]}]}`,
		"group-no-nodes-ok":   `{"kind":"graph","groups":[{"members":["q"]}]}`,
		"chart-no-spec":       `{"kind":"chart"}`,
		"chart-graph-kind":    `{"kind":"graph","chart":{"type":"pie","series":[{"name":"a","data":[{"y":1}]}]}}`,
		"chart-bad-type":      `{"kind":"chart","chart":{"type":"radar","series":[{"name":"a","data":[]}]}}`,
		"chart-no-type":       `{"kind":"chart","chart":{"series":[{"name":"a","data":[]}]}}`,
		"chart-no-series":     `{"kind":"chart","chart":{"type":"line"}}`,
		"chart-empty-series":  `{"kind":"chart","chart":{"type":"line","series":[]}}`,
		"chart-types":         `{"kind":"chart","chart":{"type":"donut","series":[{"name":"a","data":[{"y":1,"x":0},{"y":2,"x":-1.5,"label":"b","color":"c"}]},{"name":"b","data":[]}]}}`,
		"chart-pie":           `{"kind":"chart","chart":{"type":"pie","series":[{"name":"s","color":"red","data":[{"y":0}]}]}}`,
		"chart-scatter":       `{"kind":"chart","chart":{"type":"scatter","series":[{"name":"s","data":[{"x":1,"y":2}]}]}}`,
		"chart-area":          `{"kind":"chart","chart":{"type":"area","title":"A","series":[{"name":"s","data":null}],"x_axis":{"min":0,"max":0},"y_axis":{"max":10.5,"labels":[]}}}`,
		"version-theme":       `{"version":"2.0","kind":"graph","theme":"dark","title":""}`,
		"deep-nesting":        `{"kind":"custom","custom":` + strings.Repeat(`{"a":`, 200) + `1` + strings.Repeat(`}`, 200) + `}`,
		"leading-whitespace":  "  \n\t{\"kind\":\"graph\"}  \n",
		"crlf":                "{\r\n\"kind\":\r\n\"graph\"\r\n}\r\n",
	})
	add("json-depth-ok", "json", `{"kind":"custom","custom":{"a":`+strings.Repeat("[", 9998)+strings.Repeat("]", 9998)+`}}`)
	add("json-depth-exceeded", "json", `{"kind":"custom","custom":{"a":`+strings.Repeat("[", 9999)+strings.Repeat("]", 9999)+`}}`)
	add("json-long-title", "json", `{"kind":"graph","title":"`+strings.Repeat("x", 20000)+`"}`)
	{
		var b strings.Builder
		b.WriteString(`{"kind":"graph","nodes":[`)
		for i := 0; i < 300; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"n%d","label":"Node %d"}`, i, i)
		}
		b.WriteString(`],"edges":[`)
		for i := 1; i < 300; i++ {
			if i > 1 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"from":"n%d","to":"n%d"}`, i-1, i)
		}
		b.WriteString(`]}`)
		add("json-many-nodes", "json", b.String())
	}

	// --- Source dispatch ---
	named("source", "source", map[string]string{
		"json-leading-space":  "   {\"kind\":\"graph\"}",
		"json-leading-nl":     "\n\n{\"kind\":\"graph\"}\n",
		"json-bad":            "{ nope",
		"json-nbsp-prefix":    "\u00a0{\"kind\":\"graph\"}",
		"mermaid-bracket":     "[1]",
		"mermaid-quote":       "\"graph TD\"",
		"mermaid-empty":       "",
		"mermaid-blank":       " \n ",
		"json-after-comment":  "%% c\n{\"kind\":\"graph\"}",
		"json-chart":          `{"kind":"chart","chart":{"type":"pie","series":[{"name":"a","data":[{"y":1}]}]}}`,
		"mermaid-in-json-key": `{"graph TD":1}`,
	})

	// --- Mermaid: headers, directions, frontmatter, whitespace ---
	named("mm-header", "mermaid", map[string]string{
		"empty":                 "",
		"blank-lines":           "\n\n  \n\t\n",
		"comment-only":          "%% just a comment",
		"comment-then-graph":    "%% c\n\ngraph TD\nA --> B",
		"graph-only":            "graph",
		"graph-td":              "graph TD",
		"flowchart-only":        "flowchart",
		"flowchart-lr":          "flowchart LR\nA-->B",
		"graph-tb":              "graph TB\nA-->B",
		"graph-bt":              "graph BT\nA-->B",
		"graph-rl":              "graph RL\nA-->B",
		"graph-lowercase-dir":   "graph lr\nA-->B",
		"graph-upper":           "GRAPH TD\nA-->B",
		"flowchart-mixed":       "FlowChart Td\nA-->B",
		"bad-direction":         "graph XY\nA-->B",
		"bad-direction-unicode": "graph ÄÖ\nA-->B",
		"extra-words":           "graph TD extra words here\nA-->B",
		"unknown-header":        "foo bar\nA-->B",
		"unknown-header-unic":   "überschrift TD\nA-->B",
		"header-is-edge":        "A --> B",
		"graph-prefix-word":     "graphs TD\nA-->B",
		"graph-colon":           "graph: TD\nA-->B",
		"header-with-semicolon": "graph TD; A-->B",
		"header-trailing-semi":  "graph TD;\nA-->B",
		"header-comment":        "graph TD %% comment\nA-->B",
		"crlf":                  "graph TD\r\nA --> B\r\nB --> C\r\n",
		"cr-only":               "graph TD\rA --> B",
		"tabs":                  "\tgraph\tTD\t\n\tA\t-->\tB\t\n",
		"nbsp":                  "graph\u00a0TD\nA --> B",
		"nbsp-indent":           "\u00a0\u00a0graph TD\n\u00a0A --> B\u00a0",
		"nel":                   "graph TD\u0085A --> B",
		"bom":                   "\ufeffgraph TD\nA --> B",
		"line-sep":              "graph TD\u2028A --> B",
		"ideographic-space":     "graph\u3000TD\nA --> B",
		"title-fm":              "---\ntitle: My Flow\ntheme: dark\n---\ngraph LR\nA --> B",
		"title-fm-quotes":       "---\ntitle: \"Quoted\"\ntheme: 'neon'\n---\ngraph LR\nA --> B",
		"title-fm-spaces":       "  ---  \n  title:   spaced   \n  ---  \ngraph LR\nA --> B",
		"fm-unclosed":           "---\ntitle: x\ngraph LR\nA --> B",
		"fm-only":               "---\ntitle: x\n---",
		"fm-empty":              "---\n---\ngraph TD\nA",
		"fm-other-keys":         "---\nauthor: me\ntitle: t\n---\ngraph TD\nA",
		"fm-title-then-body":    "---\ntitle: fm\n---\ngraph TD\ntitle body\nA",
		"fm-crlf":               "---\r\ntitle: crlf\r\n---\r\ngraph TD\r\nA --> B",
		"fm-not-first":          "\n---\ntitle: x\n---\ngraph TD\nA",
		"fm-unicode":            "---\ntitle: Größe 日本\n---\ngraph TD\nA",
		"fm-colon-nospace":      "---\ntitle:tight\n---\ngraph TD\nA",
	})

	// Unsupported diagram families (each alternative of the Go regexp).
	for _, h := range []string{
		"pie", "pie title Pets", "pieX", "pie-x", "classDiagram", "classDiagram-v2", "classDiagram-v2x", "classdiagram-v3", "CLASSDIAGRAM",
		"stateDiagram", "stateDiagram-v2", "statediagram-v2 extra", "erDiagram", "gantt", "gantt\n dateFormat X", "gitGraph", "journey",
		"mindmap", "quadrantChart", "xychart-beta", "xychart", "xychartx", "requirementDiagram", "C4Context", "C4Container", "C4Component",
		"C4Dynamic", "C4Deployment", "architecture-beta", "architecture", "packet-beta", "packet", "kanban", "block-beta", "block", "blockx",
		"sankey-beta", "sankey", "Pie", "PIE chart", "pie_", "pie1", "pie.", "pie\u00e9", "pie\u00a0x", "\u017ftateDiagram", "\u017fankey", "ban\u017fai",
		"\u212Aanban", "bloc\u212A", "bloc\u212A x", "bloc\u212A-beta", "block\u212A", "gant", "sequence", "sequenceDiagram-v2",
	} {
		add("mm-unsup-"+slug(h), "mermaid", h+"\nA --> B")
	}
	add("mm-unsup-after-comment", "mermaid", "%% c\n   pie\nA")
	add("mm-unsup-after-fm", "mermaid", "---\ntitle: x\n---\n\npie")
	add("mm-unsup-crlf", "mermaid", "pie title X\r\n\"a\": 1")

	// Unsupported directives (Go regexp alternatives, case-folded, \b rules).
	for _, d := range []string{
		"click A callback", "click A", "CLICK A href", "style A fill:#f9f", "style", "Style\tA fill", "classDef red fill:#f00", "classDef", "classdef x",
		"class A red", "class", "CLASS A b", "linkStyle 0 stroke:red", "linkstyle default", "accTitle: x", "accTitle:", "accDescr: x", "accDescr {", "direction LR",
		"direction", "interpolate basis", "Interpolate x", "styles A", "classy A", "style1 --> B", "class_ --> B", "click-me --> B", "click.me --> B",
		"direction\u00e9", "click\u00a0A", "\u017ftyle A", "\u212Alick A", "clic\u212A A", "classDef\u212A",
		"%%{init: {\"theme\": \"dark\"}}%%", "%%{init: x}%% A --> B", "A --> B %%{init: y}%%", "%%{ init: x}%%", "%%{INIT: x}%%", "A --> B %%{init:",
		"A ~~~ B", "A~~~B", "A --> B ~~~ C", "A[x~~~y]", "\"~~~\"",
	} {
		add("mm-dir-"+slug(d), "mermaid", "graph TD\nA --> B\n"+d)
		add("mm-dir-first-"+slug(d), "mermaid", "graph TD\n"+d)
	}
	add("mm-dir-in-semicolon", "mermaid", "graph TD\nA --> B; style A fill:red; C")
	add("mm-dir-in-subgraph", "mermaid", "graph TD\nsubgraph s\nstyle A fill:red\nend")

	// Title statements.
	named("mm-title", "mermaid", map[string]string{
		"title-space":   "graph TD\ntitle My Title\nA --> B",
		"title-colon":   "graph TD\ntitle: Colon Title\nA --> B",
		"title-upper":   "graph TD\nTITLE Upper\nA",
		"title-quoted":  "graph TD\ntitle \"Quoted\"\nA",
		"title-single":  "graph TD\ntitle 'S'\nA",
		"title-first":   "graph TD\ntitle One\ntitle Two\nA",
		"title-vs-fm":   "---\ntitle: FM\n---\ngraph TD\ntitle Body\nA",
		"title-alone":   "graph TD\ntitle\nA",
		"title-colon-n": "graph TD\ntitle:\nA",
		"title-empty-s": "graph TD\ntitle  \nA",
		"title-nospace": "graph TD\ntitle:tight\nA",
		"title-unicode": "graph TD\ntitle Größe 日本 😀\nA",
		"title-dotless": "graph TD\nt\u0130tle Dotted\nA",
		"title-dotted2": "graph TD\n\u0130\u0130\u0130 x\nA",
		"title-semi":    "graph TD\ntitle A; B\nA",
		"title-quote":   "graph TD\ntitle it's\nA",
		"title-in-sg":   "graph TD\nsubgraph s\ntitle T\nA\nend",
		"title-ctrl":    "graph TD\ntitle a\tb\nA",
	})

	// Shapes.
	named("mm-shape", "mermaid", map[string]string{
		"all":                   "graph TD\na[Rectangle]\nb(Round)\nc([Stadium])\nd[[Subroutine]]\ne[(Cylinder)]\nf((Circle))\ng>Asymmetric]\nh{Diamond}\ni{{Hexagon}}",
		"bare-id":               "graph TD\nA",
		"bare-ids-chain":        "graph TD\nA\nB\nC",
		"empty-rect":            "graph TD\nA[]",
		"empty-round":           "graph TD\nA()",
		"empty-diamond":         "graph TD\nA{}",
		"empty-circle":          "graph TD\nA(())",
		"empty-hex":             "graph TD\nA{{}}",
		"empty-stadium":         "graph TD\nA([])",
		"empty-cyl":             "graph TD\nA[()]",
		"empty-sub":             "graph TD\nA[[]]",
		"empty-asym":            "graph TD\nA>]",
		"spaces-label":          "graph TD\nA[   padded   ]",
		"quoted-double":         "graph TD\nA[\"Quoted Label\"]",
		"quoted-single":         "graph TD\nA['Quoted Label']",
		"quoted-escapes":        "graph TD\nA[\"a\\nb\\tc\\\"d\\\\e\\'f\\qg\"]",
		"quoted-brackets":       "graph TD\nA[\"Label with [brackets] and (parens) and {braces}\"]",
		"quoted-close-in":       "graph TD\nA[\"has ] inside\"]",
		"quoted-semicolon":      "graph TD\nA[\"a;b\"]",
		"quoted-comment":        "graph TD\nA[\"a %% not comment\"]",
		"quoted-trailing":       "graph TD\nA[\"x\" y]",
		"quoted-only-quote":     "graph TD\nA[\"]",
		"quote-partial":         "graph TD\nA[\"abc]",
		"quote-apostrophe":      "graph TD\nA[Don't]",
		"quote-apos-closed":     "graph TD\nA[Don't] --> B[It's]",
		"quote-unclosed-id":     "graph TD\nA\"B --> C",
		"backslash-end":         "graph TD\nA[\"x\\\"]",
		"wikilink":              "graph TD\nA[[Target Note]]",
		"wikilink-display":      "graph TD\nA[[Target|Display]]",
		"wikilink-rect":         "graph TD\nA[[[Target]]]",
		"wikilink-display-rect": "graph TD\nA[[[Target|Shown]]]",
		"wikilink-empty-pipe":   "graph TD\nA[[Target|]]",
		"wikilink-in-round":     "graph TD\nA([[Target]])",
		"wikilink-spaces":       "graph TD\nA[[  Spaced Target  |  Shown  ]]",
		"wikilink-multi-pipe":   "graph TD\nA[[T|a|b]]",
		"wikilink-quoted":       "graph TD\nA[\"[[Target]]\"]",
		"nested-brackets":       "graph TD\nA[a[b]c]",
		"nested-parens":         "graph TD\nA(a(b)c)",
		"round-in-rect":         "graph TD\nA[(x)]",
		"unclosed-rect":         "graph TD\nA[Label",
		"unclosed-round":        "graph TD\nA(Label",
		"unclosed-diamond":      "graph TD\nA{Label",
		"unclosed-circle":       "graph TD\nA((Label)",
		"unclosed-sub":          "graph TD\nA[[Label]",
		"unclosed-hex":          "graph TD\nA{{Label}",
		"unclosed-asym":         "graph TD\nA>Label",
		"unclosed-stadium":      "graph TD\nA([Label)",
		"unclosed-cyl":          "graph TD\nA[(Label]",
		"junk-after":            "graph TD\nA[x]junk",
		"junk-after-edge":       "graph TD\nA[x]junk --> B",
		"space-then-node":       "graph TD\nA[x] B[y]",
		"double-circle":         "graph TD\nA(((x)))",
		"double-circle-edge":    "graph TD\nA --> B(((x)))",
		"parallelogram":         "graph TD\nA[/x/]",
		"parallelogram-alt":     "graph TD\nA[\\x\\]",
		"trapezoid":             "graph TD\nA[/x\\]",
		"trapezoid-alt":         "graph TD\nA[\\x/]",
		"slash-label":           "graph TD\nA[a/b]",
		"slash-first-edge":      "graph TD\nB --> A[/x/]",
		"asym-only":             "graph TD\nA>Label with > inside]",
		"gt-start":              "graph TD\n>x]",
		"hex-nested":            "graph TD\nA{{x{y}z}}",
		"diamond-question":      "graph TD\nA{Is it?}",
		"shape-spaces-before":   "graph TD\nA [Spaced]",
		"shape-space-round":     "graph TD\nA   (Round)",
		"shape-redefine":        "graph TD\nA[One]\nA[Two]\nA(Round)",
		"shape-redefine-label":  "graph TD\nA\nA[Later]\nA((Circle))",
		"shape-redefine-id":     "graph TD\nA --> B\nA[Named]\nB{{Hex}}",
		"shape-redefine-note":   "graph TD\nA[Plain]\nA[[Note]]\nA[[Other|X]]",
		"shape-redefine-rect":   "graph TD\nA[One]\nA[One]\nA((C))",
		"shape-label-eq-id":     "graph TD\nA[A]\nA[Real]",
		"id-unicode":            "graph TD\nÄrger --> Öl\n日本 --> 語",
		"id-hyphen":             "graph TD\nmy-node --> other-node",
		"id-dots-edge":          "graph TD\na.b --> c.d",
		"id-digits":             "graph TD\n1 --> 2\n10 --> 20",
		"id-emoji":              "graph TD\n😀 --> 😁",
		"id-combining":          "graph TD\ne\u0301 --> a\u0308",
		"id-rtl":                "graph TD\nשלום --> مرحبا",
		"id-o-prefix":           "graph TD\nfoo--bar",
		"id-x-prefix":           "graph TD\nbox--bar",
		"id-o-edge":             "graph TD\nfoo--obar",
		"id-keyword-end":        "graph TD\nend",
		"id-keyword-endx":       "graph TD\nendx --> B",
		"id-subgraph-word":      "graph TD\nsubgraph",
		"id-subgraph-only-edge": "graph TD\nsubgraph --> B",
		"id-equals":             "graph TD\nA=B",
		"id-slash":              "graph TD\nA/B --> C",
		"id-colon":              "graph TD\nA:B --> C",
		"id-hash":               "graph TD\n#A --> B",
		"id-quote-start":        "graph TD\n\"A\" --> B",
		"id-star":               "graph TD\n* --> B",
		"id-pipe-start":         "graph TD\n|A --> B",
		"id-bracket-start":      "graph TD\n[A] --> B",
		"id-paren-start":        "graph TD\n(A) --> B",
		"id-brace-start":        "graph TD\n{A} --> B",
		"id-amp-start":          "graph TD\n& A",
		"id-amp-alone":          "graph TD\n&",
		"id-semi-only":          "graph TD\n;",
		"id-semis":              "graph TD\n;;;",
		"id-semi-between":       "graph TD\nA;;B",
		"id-semi-edge":          "graph TD\nA --> B;C --> D;",
		"id-dash-start":         "graph TD\n-A --> B",
		"id-dash-only":          "graph TD\n-",
		"id-dashdash-only":      "graph TD\n--",
		"id-arrow-only":         "graph TD\n-->",
		"id-arrow-first":        "graph TD\n--> B",
		"id-dotdot":             "graph TD\n.. B",
		"id-eq-only":            "graph TD\n==",
		"id-long":               "graph TD\n" + strings.Repeat("x", 5000) + " --> " + strings.Repeat("y", 5000),
		"label-long":            "graph TD\nA[" + strings.Repeat("label ", 3000) + "]",
		"label-unicode":         "graph TD\nA[Größe — 日本語 😀 é\u0301]",
		"label-controls":        "graph TD\nA[x\ty]",
		"label-html":            "graph TD\nA[<b>bold</b> & \"q\"]",
		"label-html-quoted":     "graph TD\nA[\"<b>bold</b> & q\"]",
		"label-lineseps":        "graph TD\nA[x\u2028y\u2029z]",
		"label-nul":             "graph TD\nA[x\x00y]",
		"label-del":             "graph TD\nA[x\x7fy]",
	})

	// Edges.
	connectors := []string{"-->", "---", "--o", "--x", "o--o", "x--x", "<-->", "-.->", "-..->", "-.-", "-..-", "==>", "===", "..->", "..>", "...", "..", "o--", "x--", "--", "-.", "==", "-", "->", "=>", "<--", "<-", "--->", "====>", "-.-.->", "~~", "~>", "<==>", "--|>", "..-", "<-.->"}
	for _, c := range connectors {
		add("mm-conn-"+slug(c), "mermaid", "graph TD\nA "+c+" B")
		add("mm-conn-tight-"+slug(c), "mermaid", "graph TD\nA"+c+"B")
		add("mm-conn-pipe-"+slug(c), "mermaid", "graph TD\nA "+c+"|label| B")
		add("mm-conn-pipe-sp-"+slug(c), "mermaid", "graph TD\nA "+c+" |  spaced  | B")
		add("mm-conn-end-"+slug(c), "mermaid", "graph TD\nA "+c)
		add("mm-conn-multi-"+slug(c), "mermaid", "graph LR\nA & B "+c+" C & D")
	}
	named("mm-edge", "mermaid", map[string]string{
		"labeled-solid":        "graph TD\nA -- text --> B",
		"labeled-open":         "graph TD\nA -- text --- B",
		"labeled-dotted":       "graph TD\nA -. text .-> B",
		"labeled-dotted-open":  "graph TD\nA -. text -.- B",
		"labeled-thick":        "graph TD\nA == text ==> B",
		"labeled-thick-open":   "graph TD\nA == text === B",
		"labeled-circle":       "graph TD\nA -- text --o B",
		"labeled-cross":        "graph TD\nA -- text --x B",
		"labeled-double":       "graph TD\nA -- text <--> B",
		"labeled-quoted":       "graph TD\nA -- \"quoted text\" --> B",
		"labeled-single":       "graph TD\nA -- 'q' --> B",
		"labeled-spaces":       "graph TD\nA --   padded   --> B",
		"labeled-no-end":       "graph TD\nA -- text B",
		"labeled-no-end-2":     "graph TD\nA -. text B",
		"labeled-no-end-3":     "graph TD\nA == text B",
		"labeled-empty":        "graph TD\nA --  --> B",
		"labeled-two-ends":     "graph TD\nA -- one --> two --- B",
		"labeled-order":        "graph TD\nA -- x --- y --> B",
		"labeled-arrow-in":     "graph TD\nA -- a --> b --> B",
		"labeled-dotted-mix":   "graph TD\nA -. x ==> B",
		"labeled-thick-mix":    "graph TD\nA == x --> B",
		"labeled-solid-dot":    "graph TD\nA -- x .-> B",
		"labeled-then-pipe":    "graph TD\nA -- x -->|y| B",
		"labeled-multi-space":  "graph TD\nA -- x  --> B",
		"labeled-tab":          "graph TD\nA --\tx --> B",
		"labeled-unicode":      "graph TD\nA -- Größe 日本 --> B",
		"labeled-chain":        "graph TD\nA -- x --> B -- y --> C",
		"pipe-label":           "graph TD\nA -->|label| B",
		"pipe-space":           "graph TD\nA --> |label| B",
		"pipe-quoted":          "graph TD\nA -->|\"quoted\"| B",
		"pipe-single":          "graph TD\nA -->|'q'| B",
		"pipe-empty":           "graph TD\nA -->|| B",
		"pipe-unclosed":        "graph TD\nA -->|label B",
		"pipe-unclosed-end":    "graph TD\nA -->|",
		"pipe-only-pipe":       "graph TD\nA --> |",
		"pipe-two":             "graph TD\nA -->|a|b| B",
		"pipe-with-shape":      "graph TD\nA -->|yes| B[Box]",
		"pipe-label-brackets":  "graph TD\nA -->|[x]| B",
		"pipe-unicode":         "graph TD\nA -->|日本| B",
		"missing-target":       "graph TD\nA -->",
		"missing-target-sp":    "graph TD\nA --> ",
		"missing-target-semi":  "graph TD\nA -->;B",
		"missing-after-pipe":   "graph TD\nA -->|x|",
		"missing-source":       "graph TD\n--> B",
		"double-arrow":         "graph TD\nA --> --> B",
		"arrow-then-bad":       "graph TD\nA --> [x]",
		"arrow-then-amp":       "graph TD\nA --> & B",
		"amp-trailing":         "graph TD\nA & --> B",
		"amp-source":           "graph TD\nA & B --> C",
		"amp-target":           "graph TD\nA --> B & C",
		"amp-both":             "graph TD\nA & B --> C & D & E",
		"amp-chain":            "graph TD\nA & B --> C & D --> E & F",
		"amp-shapes":           "graph TD\nA[One] & B(Two) --> C{Three}",
		"amp-dup":              "graph TD\nA & A --> B",
		"amp-nospace":          "graph TD\nA&B-->C&D",
		"amp-end":              "graph TD\nA --> B &",
		"amp-end-semi":         "graph TD\nA --> B &;",
		"chain":                "graph TD\nA --> B --> C --> D",
		"chain-mixed":          "graph TD\nA --> B -.-> C ==> D --- E",
		"chain-shapes":         "graph TD\nA[Start] --> B{Choice} -->|yes| C((Done))",
		"chain-self":           "graph TD\nA --> A",
		"chain-self-label":     "graph TD\nA -->|loop| A",
		"chain-cycle":          "graph TD\nA --> B --> A",
		"edge-dup":             "graph TD\nA --> B\nA --> B",
		"edge-semis":           "graph TD\nA --> B; B --> C; C --> D",
		"edge-semi-spaces":     "graph TD\n  A --> B  ;  B --> C  ;  ",
		"trailing-token":       "graph TD\nA --> B extra",
		"trailing-token-2":     "graph TD\nA --> B[x] extra stuff",
		"trailing-after-chain": "graph TD\nA --> B --> C )",
		"unexpected-paren":     "graph TD\nA --> B)",
		"unexpected-brace":     "graph TD\nA --> B}",
		"unexpected-close":     "graph TD\nA --> B]",
		"unexpected-pipe":      "graph TD\nA --> B|",
		"unexpected-unicode":   "graph TD\nA --> B 日本語 junk",
		"unexpected-quote":     "graph TD\nA --> B \"junk\"",
		"unexpected-colon":     "graph TD\nA --> B: label",
		"unexpected-hash":      "graph TD\nA --> B #c",
		"unexpected-eq":        "graph TD\nA --> B = C",
		"unexpected-lt":        "graph TD\nA --> B < C",
		"edge-id-with-dash":    "graph TD\nnode-1 --> node-2",
		"edge-id-with-o":       "graph TD\nfoo --> bar\nboo --- zoo\noo --o ox",
		"edge-xo-tight":        "graph TD\nAo--oB\nCx--xD",
		"edge-xo-spaced":       "graph TD\nA o--o B\nC x--x D",
		"edge-xo-ids":          "graph TD\nxA --> oB\nox --> xo",
		"edge-tight-label":     "graph TD\nA--text-->B",
		"edge-tight-dash":      "graph TD\nA--->B",
		"edge-long-chain":      "graph TD\n" + strings.Repeat("N0 --> ", 1) + chain(200),
		"edge-many-lines":      "graph TD\n" + lines(400),
		"edge-wide-fan":        "graph TD\nA --> " + fan(150),
		"edge-wide-multi":      "graph TD\n" + fan2(30),
	})

	// Comments, quotes, statements.
	named("mm-text", "mermaid", map[string]string{
		"comment-line":         "graph TD\n%% a comment\nA --> B",
		"comment-trailing":     "graph TD\nA --> B %% trailing",
		"comment-no-space":     "graph TD\nA --> B%%trailing",
		"comment-in-quote":     "graph TD\nA[\"x %% y\"] --> B",
		"comment-in-single":    "graph TD\nA['x %% y'] --> B",
		"comment-after-quote":  "graph TD\nA[\"x\"] %% c\nB",
		"comment-escaped":      "graph TD\nA[\"x\\\" %% still quoted\"] --> B",
		"comment-percent":      "graph TD\nA[100%] --> B",
		"comment-percent-2":    "graph TD\nA[a%b%%c]",
		"comment-triple":       "graph TD\nA --> B %%% x",
		"comment-brace":        "graph TD\nA --> B %%{ notinit }",
		"comment-brace-init":   "graph TD\nA --> B %%{init: x}",
		"comment-only-header":  "graph TD %% header comment\nA",
		"comment-unclosed-q":   "graph TD\nA --> B 'c %% d",
		"comment-unicode":      "graph TD\nA --> B %% Größe 日本",
		"comment-crlf":         "graph TD\r\nA --> B %% c\r\nB --> C\r\n",
		"comment-at-end":       "graph TD\nA --> B\n%% end comment",
		"comment-everywhere":   "%% 1\ngraph TD %% 2\n%% 3\nA --> B %% 4\n%% 5",
		"quote-unclosed":       "graph TD\nA[\"unclosed] --> B",
		"quote-unclosed-sq":    "graph TD\nA['unclosed",
		"quote-unclosed-bare":  "graph TD\n\"abc",
		"quote-escape-eol":     "graph TD\nA[\"x\\",
		"quote-mismatch":       "graph TD\nA[\"x'] --> B",
		"quote-empty":          "graph TD\nA[\"\"]",
		"quote-empty-single":   "graph TD\nA['']",
		"quote-in-label-text":  "graph TD\nA[say \"hi\" now]",
		"quote-in-label-sq":    "graph TD\nA[say 'hi' now]",
		"quote-quote-edge":     "graph TD\nA -->|\"q\"| B[\"r\"]",
		"semicolon-quote":      "graph TD\nA[\"a;b\"]; B",
		"semicolon-after":      "graph TD\nA; B; C",
		"semicolon-only-line":  "graph TD\n  ;  ",
		"semicolon-unclosed-q": "graph TD\nA; B[\"x; C",
		"whitespace-lines":     "graph TD\n   \n\t\n  A --> B  \n   \n",
		"indented":             "graph TD\n    A --> B\n        B --> C\n\t\tC --> D",
		"no-trailing-nl":       "graph TD\nA --> B",
		"trailing-nls":         "graph TD\nA --> B\n\n\n\n",
		"windows-eols":         "graph TD\r\n\r\nA --> B\r\n\r\n",
		"mixed-eols":           "graph TD\nA --> B\r\nB --> C\rC --> D\n",
		"only-header-spaces":   "   graph   TD   ",
		"very-long-line":       "graph TD\nA --> B" + strings.Repeat(" ", 20000) + "--> C",
		"very-long-comment":    "graph TD\nA --> B %% " + strings.Repeat("c", 20000),
		"very-long-quote":      "graph TD\nA[\"" + strings.Repeat("q", 20000) + "\"]",
		"many-semicolons":      "graph TD\n" + strings.Repeat("A;", 2000),
		"many-comments":        "graph TD\n" + strings.Repeat("%% c\n", 2000) + "A",
		"deep-subgraphs":       "graph TD\n" + strings.Repeat("subgraph s\n", 50) + "A\n" + strings.Repeat("end\n", 50),
		"unicode-everywhere":   "graph LR\nÄ[Größe] -->|日本| Ö{😀}\nÖ -.-> Ü((é\u0301))",
		"surrogate-range":      "graph TD\nA[\U0001F600\U0010FFFF]",
		"noncharacter":         "graph TD\nA[\uFFFE\uFFFF\uFDD0]",
		"private-use":          "graph TD\nA[\uE000\U000F0000]",
		"zero-width":           "graph TD\nA[a\u200bb\u200dc\u2060d]",
		"format-chars":         "graph TD\nA[x\u00ady\u202ez\u061c]",
		"rtl-override-id":      "graph TD\n\u202eA --> B",
		"nel-in-line":          "graph TD\nA --> B\u0085C",
		"bom-inside":           "graph TD\nA\ufeff --> B",
		"unassigned":           "graph TD\nA[\u0378\U000E0080]",
	})

	// Subgraphs.
	named("mm-sub", "mermaid", map[string]string{
		"basic":                    "graph TD\nsubgraph one\nA --> B\nend\nC",
		"titled":                   "graph TD\nsubgraph My Group Title\nA\nend",
		"id-label-bracket":         "graph TD\nsubgraph g1 [Group One]\nA\nend",
		"id-label-paren":           "graph TD\nsubgraph g2 (Group Two)\nA\nend",
		"label-only":               "graph TD\nsubgraph [Just Label]\nA\nend",
		"label-only-paren":         "graph TD\nsubgraph (Just Paren)\nA\nend",
		"quoted":                   "graph TD\nsubgraph \"Quoted Title\"\nA\nend",
		"quoted-single":            "graph TD\nsubgraph 'Single Q'\nA\nend",
		"quoted-bracket":           "graph TD\nsubgraph g [\"Quoted Label\"]\nA\nend",
		"slug-empty":               "graph TD\nsubgraph \"!!!\"\nA\nend",
		"slug-empty-2":             "graph TD\nsubgraph [???]\nA\nend",
		"slug-unicode":             "graph TD\nsubgraph Größe 日本\nA\nend",
		"slug-tab":                 "graph TD\nsubgraph a\tb\nA\nend",
		"slug-dash":                "graph TD\nsubgraph my-group_1\nA\nend",
		"bare-subgraph-sp":         "graph TD\nsubgraph \nA\nend",
		"bare-subgraph":            "graph TD\nsubgraph\nA\nend",
		"bare-bracket":             "graph TD\nsubgraph[Label]\nA\nend",
		"bare-paren":               "graph TD\nsubgraph(Label)\nA\nend",
		"bare-quote":               "graph TD\nsubgraph\"Quoted\"\nA\nend",
		"upper":                    "graph TD\nSUBGRAPH Up\nA\nEND",
		"mixed-case-end":           "graph TD\nsubgraph s\nA\nEnD",
		"nested":                   "graph TD\nsubgraph outer\nsubgraph inner\nA\nend\nB\nend",
		"nested-3":                 "graph TD\nsubgraph a\nsubgraph b\nsubgraph c\nX\nend\nend\nend",
		"empty":                    "graph TD\nsubgraph e\nend\nA",
		"empty-nested":             "graph TD\nsubgraph o\nsubgraph i\nend\nend",
		"empty-with-nodes-outside": "graph TD\nsubgraph e\nend\nA --> B",
		"unclosed":                 "graph TD\nsubgraph s\nA",
		"unclosed-nested":          "graph TD\nsubgraph a\nsubgraph b\nA\nend",
		"unclosed-title":           "graph TD\nsubgraph My Unclosed\nA",
		"unclosed-autoid":          "graph TD\nsubgraph\nA",
		"extra-end":                "graph TD\nA\nend",
		"extra-end-after":          "graph TD\nsubgraph s\nA\nend\nend",
		"end-upper-first":          "graph TD\nEND",
		"end-semi":                 "graph TD\nsubgraph s; A; end",
		"end-trailing":             "graph TD\nsubgraph s\nA\nend extra",
		"end-comment":              "graph TD\nsubgraph s\nA\nend %% done",
		"members-edges":            "graph TD\nsubgraph s\nA --> B\nend\nB --> C",
		"members-redefine":         "graph TD\nA\nsubgraph s\nA[Again]\nend",
		"members-dup":              "graph TD\nsubgraph s\nA\nA\nA --> A\nend",
		"members-shared":           "graph TD\nsubgraph a\nX\nend\nsubgraph b\nX\nend",
		"members-nested-up":        "graph TD\nsubgraph o\nsubgraph i\nA\nend\nB\nend",
		"members-multi":            "graph TD\nsubgraph s\nA & B --> C\nend",
		"dup-names":                "graph TD\nsubgraph same\nA\nend\nsubgraph same\nB\nend",
		"autoidx":                  "graph TD\nsubgraph\nA\nend\nsubgraph\nB\nend\nsubgraph\nC\nend",
		"autoidx-mixed":            "graph TD\nsubgraph named\nA\nend\nsubgraph\nB\nend",
		"label-quotes-trim":        "graph TD\nsubgraph x [ \" padded \" ]\nA\nend",
		"label-both":               "graph TD\nsubgraph x [one] (two)\nA\nend",
		"label-unbalanced":         "graph TD\nsubgraph x [one\nA\nend",
		"label-bracket-mid":        "graph TD\nsubgraph a[b]c\nA\nend",
		"label-paren-after":        "graph TD\nsubgraph t (x) y\nA\nend",
		"direction-in":             "graph TD\nsubgraph s\ndirection LR\nA\nend",
		"title-dotted":             "graph TD\nsubgraph t\u0130tle\nA\nend",
		"subgraph-edge":            "graph TD\nsubgraph s\nA\nend\ns --> B",
		"subgraph-node-id":         "graph TD\nsubgraph s [S]\nsubgraph --> B\nend",
		"subgraphx":                "graph TD\nsubgraphx --> B",
		"sub-then-text":            "graph TD\nsubgraph s A --> B\nend",
		"title-first-ws":           "graph TD\n   subgraph   spaced   title   \n  A\n  end  ",
		"crlf":                     "graph TD\r\nsubgraph s\r\nA --> B\r\nend\r\n",
		"in-fm":                    "---\ntitle: t\n---\ngraph TD\nsubgraph s\nA\nend",
	})

	// --- Sequence diagrams ---
	named("seq", "mermaid", map[string]string{
		"basic":                      "sequenceDiagram\n    participant Alice\n    participant Bob\n    Alice->>Bob: Hello Bob\n    Bob-->>Alice: Hi Alice\n",
		"header-sequence":            "sequence\nAlice->>Bob: x",
		"header-upper":               "SEQUENCEDIAGRAM\nA->>B: x",
		"header-mixed":               "SequenceDiagram\nA->>B: x",
		"header-args":                "sequenceDiagram ignored words\nA->>B: x",
		"implicit":                   "sequenceDiagram\nAlice->>Bob: ping",
		"no-participants":            "sequenceDiagram\n",
		"no-participants-2":          "sequenceDiagram",
		"only-comments":              "sequenceDiagram\n%% c\n\n",
		"bad-line":                   "sequenceDiagram\nAlice Bob: nope\n",
		"bad-line-2":                 "sequenceDiagram\nparticipant A\nA => B: x",
		"bad-line-after-ok":          "sequenceDiagram\nA->>B: x\nthis is bad\nC->>D: y",
		"bad-line-unicode":           "sequenceDiagram\nÄ ö ü",
		"bad-line-crlf":              "sequenceDiagram\r\nbad\r\n",
		"bad-line-comment":           "sequenceDiagram\nA B %% comment",
		"bad-line-after-hdr-comment": "%% c\nsequenceDiagram\nbad line",
		"participant-alias":          "sequenceDiagram\nparticipant A as Alice\nA->>B: hi",
		"participant-quoted":         "sequenceDiagram\nparticipant A \"Quoted Name\"\nA->>B: hi",
		"participant-single":         "sequenceDiagram\nparticipant A 'Single'\nA->>B: hi",
		"participant-many":           "sequenceDiagram\nparticipant A one two three\nA->>B: hi",
		"participant-upper":          "sequenceDiagram\nPARTICIPANT A\nA->>B: hi",
		"participant-mixed":          "sequenceDiagram\nParticipant A\nA->>B: hi",
		"participant-dup":            "sequenceDiagram\nparticipant A\nparticipant A as Again\nA->>B: hi",
		"participant-bare":           "sequenceDiagram\nparticipant\nA->>B: x",
		"participant-spaces":         "sequenceDiagram\nparticipant    Spaced   \nA->>B: x",
		"participant-tab":            "sequenceDiagram\nparticipant\tTabbed\nA->>B: x",
		"participant-uni":            "sequenceDiagram\nparticipant Ärger as Öl\nÄrger->>Öl: Grüße 日本",
		"participant-dotted":         "sequenceDiagram\nparticip\u0130nt A\nA->>B: x",
		"participant-dot2":           "sequenceDiagram\nparticipant\u0130 A\nA->>B: x",
		"participant-dot3":           "sequenceDiagram\npart\u0130c\u0130pant A\nA->>B: x",
		"participant-quote-mid":      "sequenceDiagram\nparticipant A\"s",
		"participant-comment":        "sequenceDiagram\nparticipant A %% c\nA->>B: x",
		"actor":                      "sequenceDiagram\nactor User\nUser->>System: login",
		"actor-alias":                "sequenceDiagram\nactor U as User\nU->>S: x",
		"actor-upper":                "sequenceDiagram\nACTOR U\nU->>S: x",
		"actor-bare":                 "sequenceDiagram\nactor\nA->>B: x",
		"actor-then-part":            "sequenceDiagram\nactor A\nparticipant A as P\nA->>B: x",
		"dashed":                     "sequenceDiagram\nA-->>B: dashed",
		"dashed-and-solid":           "sequenceDiagram\nA->>B: solid\nB-->>A: dashed",
		"arrow-in-text":              "sequenceDiagram\nA->>B: text with -->> inside",
		"arrow-in-text-2":            "sequenceDiagram\nA->>B: text ->> twice",
		"arrow-dashed-first":         "sequenceDiagram\nA-->>B->>C: x",
		"arrow-in-from":              "sequenceDiagram\nA->>B-->>C: x",
		"arrow-weird":                "sequenceDiagram\nA->>>B: x",
		"arrow-triple":               "sequenceDiagram\nA--->>B: x",
		"arrow-spaces":               "sequenceDiagram\n  A  ->>  B  :  spaced text  ",
		"no-text":                    "sequenceDiagram\nA->>B",
		"no-text-colon":              "sequenceDiagram\nA->>B:",
		"no-text-colon-sp":           "sequenceDiagram\nA->>B:   ",
		"colon-in-text":              "sequenceDiagram\nA->>B: one: two: three",
		"colon-first":                "sequenceDiagram\nA->>:text",
		"empty-from":                 "sequenceDiagram\n->>B: x",
		"empty-to":                   "sequenceDiagram\nA->>: x",
		"empty-to-nocolon":           "sequenceDiagram\nA->>",
		"empty-both":                 "sequenceDiagram\n->>",
		"from-spaces":                "sequenceDiagram\nAlice Smith->>Bob Jones: x",
		"unicode-text":               "sequenceDiagram\nA->>B: Größe — 日本語 😀 <&> \"q\" 'a'",
		"html-text":                  "sequenceDiagram\nA->>B: <script>alert(1)</script> & more",
		"text-controls":              "sequenceDiagram\nA->>B: tab\there",
		"text-lineseps":              "sequenceDiagram\nA->>B: a\u2028b\u2029c",
		"comment-in-line":            "sequenceDiagram\nA->>B: x %% comment",
		"comment-quote":              "sequenceDiagram\nA->>B: \"x %% y\"",
		"unclosed-quote":             "sequenceDiagram\nA->>B: it's",
		"unclosed-quote-2":           "sequenceDiagram\nA->>B: \"abc",
		"semicolon":                  "sequenceDiagram\nA->>B: x; C->>D: y",
		"note-line":                  "sequenceDiagram\nNote right of A: hi",
		"loop-line":                  "sequenceDiagram\nloop Every minute\nA->>B: x\nend",
		"alt-line":                   "sequenceDiagram\nalt ok\nA->>B: x\nelse bad\nB->>A: y\nend",
		"activate-line":              "sequenceDiagram\nactivate A",
		"autonumber":                 "sequenceDiagram\nautonumber\nA->>B: x",
		"title-line":                 "sequenceDiagram\ntitle My Seq\nA->>B: x",
		"crlf":                       "sequenceDiagram\r\nparticipant A\r\nparticipant B\r\nA->>B: hi\r\nB-->>A: yo\r\n",
		"crlf-text":                  "sequenceDiagram\r\nA->>B: crlf text\r\n",
		"indent-tabs":                "sequenceDiagram\n\tparticipant A\n\tA->>B: x",
		"order-many":                 "sequenceDiagram\n" + seqMany(120),
		"actors-many":                "sequenceDiagram\n" + seqActors(80),
		"long-text":                  "sequenceDiagram\nA->>B: " + strings.Repeat("long ", 4000),
		"long-id":                    "sequenceDiagram\n" + strings.Repeat("A", 5000) + "->>B: x",
		"fm-before":                  "---\ntitle: seq\n---\nsequenceDiagram\nA->>B: x",
		"after-comment-hdr":          "%% c\n\nsequenceDiagram\nA->>B: x",
		"graph-in-seq":               "sequenceDiagram\ngraph TD",
		"subgraph-in-seq":            "sequenceDiagram\nsubgraph s\nend",
		"nodes-like":                 "sequenceDiagram\nA[Label] --> B",
		"to-with-colon-lead":         "sequenceDiagram\nA->>B :text",
		"tight":                      "sequenceDiagram\nA->>B:x",
		"id-emoji":                   "sequenceDiagram\n😀->>😁: 👋",
		"id-nbsp":                    "sequenceDiagram\nA\u00a0B->>C: x",
		"text-nbsp":                  "sequenceDiagram\nA->>B:\u00a0text\u00a0",
	})

	return out
}

// generatedCorpus adds deterministic pseudo-random token soups and mutations
// of the accepted inputs. The PRNG is PCG with fixed seeds, so the corpus is
// stable across Go versions; outputs are still recorded from the real parser.
func generatedCorpus(base []testCase) []corpusCase {
	rng := rand.New(rand.NewPCG(0x5eed, 0xd5a))
	pick := func(items []string) string { return items[rng.IntN(len(items))] }
	var out []corpusCase

	flow := []string{"A", "B", "C", "node1", "x", "o", "Ä", "日", "-->", "---", "-.->", "==>", "--o", "--x", "<-->", "o--o", "..", "-- txt -->", "-.", "==", "[", "]", "(", ")", "{", "}", "((", "))", "[[", "]]", "([", "])", "[(", ")]", "{{", "}}", ">", "\"", "'", "&", ";", "|", "|lab|", " ", " ", " ", "\t", "\n", "\n", "\r\n", "subgraph", "end", "%%", "%%{", "title", "title:", "click", "style", "classDef", "direction", "~~~", "/", "\\", ":", "label text", "\u00a0", "😀", "\u0130"}
	for i := 0; i < 900; i++ {
		var b strings.Builder
		b.WriteString(pick([]string{"graph TD\n", "flowchart LR\n", "graph\n", "---\ntitle: t\n---\ngraph BT\n", "graph TD "}))
		for n := 2 + rng.IntN(14); n > 0; n-- {
			b.WriteString(pick(flow))
		}
		out = append(out, corpusCase{fmt.Sprintf("gen-flow-%03d", i), "mermaid", b.String()})
	}

	seq := []string{"participant", "actor", "A", "B", "Alice", "as", "->>", "-->>", ":", " ", " ", "\n", "\n", "text", "\"", "'", "%%", "Note", "x", "😀", "\u0130", "\t", ";"}
	for i := 0; i < 300; i++ {
		var b strings.Builder
		b.WriteString(pick([]string{"sequenceDiagram\n", "sequence\n", "SequenceDiagram "}))
		for n := 2 + rng.IntN(12); n > 0; n-- {
			b.WriteString(pick(seq))
		}
		out = append(out, corpusCase{fmt.Sprintf("gen-seq-%03d", i), "mermaid", b.String()})
	}

	alphabet := []rune("{}[]\",:\\ 0123456789-.eE+tfnulsaxy\n\u00e9\u0130\u212a")
	var seeds []string
	for _, c := range base {
		if c.Accepted && c.Entry != "mermaid" && len(c.Input) < 600 && strings.HasPrefix(strings.TrimSpace(c.Input), "{") {
			seeds = append(seeds, c.Input)
		}
	}
	for i := 0; i < 700 && len(seeds) > 0; i++ {
		runes := []rune(seeds[rng.IntN(len(seeds))])
		for m := 1 + rng.IntN(3); m > 0 && len(runes) > 0; m-- {
			at := rng.IntN(len(runes))
			switch rng.IntN(3) {
			case 0:
				runes = append(runes[:at], runes[at+1:]...)
			case 1:
				runes[at] = alphabet[rng.IntN(len(alphabet))]
			default:
				runes = append(runes[:at], append([]rune{alphabet[rng.IntN(len(alphabet))]}, runes[at:]...)...)
			}
		}
		entry := "json"
		if i%2 == 1 {
			entry = "source"
		}
		out = append(out, corpusCase{fmt.Sprintf("gen-json-%03d", i), entry, string(runes)})
	}
	return out
}

func seqMany(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			fmt.Fprintf(&b, "A->>B: message %d\n", i)
		} else {
			fmt.Fprintf(&b, "B-->>A: reply %d\n", i)
		}
	}
	return b.String()
}

func seqActors(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "participant P%d as Actor %d\n", i, i)
	}
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "P%d->>P%d: hop\n", i-1, i)
	}
	return b.String()
}

func chain(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "N%d", i)
		if i < n {
			b.WriteString(" --> ")
		}
	}
	return b.String()
}

func lines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "N%d[Node %d] -->|e%d| N%d(Next)\n", i, i, i, i+1)
	}
	return b.String()
}

func fan(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("T%d", i)
	}
	return strings.Join(parts, " & ")
}

func fan2(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "S%d & S%d --> T%d & T%d\n", i, i+1, i, i+1)
	}
	return b.String()
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "_%x", r)
		}
	}
	return b.String()
}

// renderTables emits the Go Unicode facts the Rust port needs: IsSpace,
// IsPrint (strconv.Quote's %q printable test) and ToLower (strings.ToLower).
func renderTables() string {
	var b strings.Builder
	b.WriteString("// Code generated by scripts/rust-port/cmd/renderdslgen; DO NOT EDIT.\n")
	b.WriteString("// Source: Go unicode package of the pinned fixture toolchain (go1.26.10).\n")
	b.WriteString("// Regenerate with `make render-ir-fixtures-generate`.\n\n")
	ranges := func(name string, doc string, pred func(rune) bool) {
		fmt.Fprintf(&b, "/// %s\npub(crate) const %s: &[(u32, u32)] = &[\n", doc, name)
		start := rune(-1)
		flush := func(end rune) {
			if start >= 0 {
				fmt.Fprintf(&b, "    (0x%X, 0x%X),\n", start, end)
				start = -1
			}
		}
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if pred(r) {
				if start < 0 {
					start = r
				}
			} else {
				flush(r - 1)
			}
		}
		flush(unicode.MaxRune)
		b.WriteString("];\n\n")
	}
	ranges("GO_SPACE", "Inclusive rune ranges where Go's unicode.IsSpace is true.", unicode.IsSpace)
	ranges("GO_PRINT", "Inclusive rune ranges where Go's strconv.IsPrint is true.", strconv.IsPrint)
	b.WriteString("/// (rune, lower) pairs where Go's unicode.ToLower changes the rune.\npub(crate) const GO_TO_LOWER: &[(u32, u32)] = &[\n")
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if l := unicode.ToLower(r); l != r {
			fmt.Fprintf(&b, "    (0x%X, 0x%X),\n", r, l)
		}
	}
	b.WriteString("];\n")
	return b.String()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
