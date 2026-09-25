// Command renderirgen freezes the Go SymDraw JSON parser and IR validation contract.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/parse"
)

type fixture struct {
	SchemaVersion int        `json:"schema_version"`
	Oracle        oracle     `json:"oracle"`
	Cases         []testCase `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type testCase struct {
	ID       string          `json:"id"`
	Input    string          `json:"input"`
	Accepted bool            `json:"accepted"`
	Stage    string          `json:"stage,omitempty"`
	Field    string          `json:"field"`
	Diagram  json.RawMessage `json:"diagram,omitempty"`
}

func main() {
	output := flag.String("output", "testdata/port/render/json-ir.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", "91f09a2515fcbbeac9a059375668a7545d77e4e7", "Go oracle commit")
	release := flag.String("oracle-release", "render-ir-oracle", "Go oracle release")
	flag.Parse()

	result := fixture{SchemaVersion: 1, Oracle: oracle{Commit: *commit, Release: *release}}
	for _, item := range corpus() {
		got, err := parse.ParseJSON([]byte(item.input))
		entry := testCase{ID: item.id, Input: item.input, Accepted: err == nil}
		if err != nil {
			var parseErr *parse.ParseError
			if !errors.As(err, &parseErr) {
				fatal("%s: expected typed ParseError, got %T: %v", item.id, err, err)
			}
			entry.Stage = parseErr.Stage
			entry.Field = field(parseErr)
		} else {
			entry.Diagram, err = json.Marshal(got)
			if err != nil {
				fatal("%s: marshal Go oracle result: %v", item.id, err)
			}
		}
		result.Cases = append(result.Cases, entry)
	}
	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal("marshal fixture: %v", err)
	}
	content = append(content, '\n')
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil || !bytes.Equal(current, content) {
			fatal("render JSON IR fixture is stale; regenerate deliberately")
		}
		fmt.Println("PASS render JSON IR fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Println("PASS render JSON IR fixture generated")
}

type corpusCase struct{ id, input string }

func corpus() []corpusCase {
	return []corpusCase{
		{"valid-graph", `{"kind":"graph","direction":"LR","title":"Services","width":800,"height":400,"nodes":[{"id":"api","label":"API","shape":"round","width":120,"height":48,"style":{"fill":"#fff","stroke":"#333","stroke_width":2,"text_color":"#111","opacity":0.9,"dash_array":"2 2"}},{"id":"db","label":"Store","shape":"cylinder"}],"edges":[{"from":"api","to":"db","label":"writes","style":"dashed","arrow":"single","color":"#666"}],"groups":[{"id":"backend","label":"Backend","members":["api","db"],"style":{"fill":"#eee","stroke":"#ccc","stroke_width":1}}]}`},
		{"valid-chart", `{"kind":"chart","chart":{"type":"bar","title":"Usage","legend":true,"series":[{"name":"Requests","color":"blue","data":[{"label":"Jan","y":12},{"x":2,"y":18,"label":"Feb","color":"green"}]}],"x_axis":{"title":"Month","labels":["Jan","Feb"],"min":0,"max":2},"y_axis":{"title":"Count","min":0}}}`},
		{"empty", ``},
		{"malformed", `{"kind":"graph",}`},
		{"unknown-top-level", `{"kind":"graph","unsupported":true}`},
		{"unknown-nested-style", `{"kind":"graph","nodes":[{"id":"a","style":{"glow":true}}]}`},
		{"invalid-kind", `{"kind":"unknown"}`},
		{"duplicate-id", `{"kind":"graph","nodes":[{"id":"a"},{"id":"a"}]}`},
		{"dangling-edge", `{"kind":"graph","nodes":[{"id":"a"}],"edges":[{"from":"a","to":"missing"}]}`},
		{"negative-dimension", `{"kind":"graph","nodes":[{"id":"a","width":-1}]}`},
	}
}

func field(err *parse.ParseError) string {
	if err.Stage != "schema" {
		return err.Detail
	}
	const marker = `unknown field "`
	if i := strings.Index(err.Detail, marker); i >= 0 {
		value := err.Detail[i+len(marker):]
		if end := strings.IndexByte(value, '"'); end >= 0 {
			return value[:end]
		}
	}
	return ""
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
