package parse

import (
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

func TestParseValidJSON(t *testing.T) {
	validJSON := `{
		"kind": "graph",
		"direction": "TD",
		"title": "System Arch",
		"nodes": [
			{"id": "a", "label": "Ingest", "shape": "round"},
			{"id": "b", "label": "Database", "shape": "cylinder"}
		],
		"edges": [
			{"from": "a", "to": "b", "label": "writes", "style": "solid", "arrow": "single"}
		],
		"groups": [
			{"id": "grp1", "label": "Backend", "members": ["a", "b"]}
		]
	}`

	diag, err := ParseJSON([]byte(validJSON))
	if err != nil {
		t.Fatalf("unexpected error parsing valid JSON: %v", err)
	}

	if diag.Kind != ir.KindGraph {
		t.Errorf("expected KindGraph, got %q", diag.Kind)
	}
	if diag.Direction != ir.DirTD {
		t.Errorf("expected DirTD, got %q", diag.Direction)
	}
	if diag.Title != "System Arch" {
		t.Errorf("expected title 'System Arch', got %q", diag.Title)
	}
	if len(diag.Nodes) != 2 || len(diag.Edges) != 1 || len(diag.Groups) != 1 {
		t.Errorf("unexpected counts: nodes=%d, edges=%d, groups=%d",
			len(diag.Nodes), len(diag.Edges), len(diag.Groups))
	}
}

func TestParseValidChartJSON(t *testing.T) {
	chartJSON := `{
		"kind": "chart",
		"chart": {
			"type": "bar",
			"title": "Vault Metrics",
			"series": [
				{
					"name": "Docs",
					"data": [
						{"label": "Jan", "y": 100},
						{"label": "Feb", "y": 150}
					]
				}
			]
		}
	}`

	diag, err := ParseJSON([]byte(chartJSON))
	if err != nil {
		t.Fatalf("unexpected error parsing valid chart JSON: %v", err)
	}
	if diag.Kind != ir.KindChart {
		t.Errorf("expected KindChart, got %q", diag.Kind)
	}
	if diag.Chart == nil || diag.Chart.Type != ir.ChartBar {
		t.Errorf("unexpected chart spec: %+v", diag.Chart)
	}
}

func TestParseJSONMalformedSyntax(t *testing.T) {
	malformed := []struct {
		name string
		json string
	}{
		{"empty", ""},
		{"whitespace only", "   \n  "},
		{"unclosed brace", `{"kind": "graph", "nodes": [`},
		{"trailing comma", `{"kind": "graph",}`},
		{"unquoted key", `{kind: "graph"}`},
	}

	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseJSON([]byte(tt.json))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}
			if pe.Stage != "parse" {
				t.Errorf("expected stage 'parse', got %q", pe.Stage)
			}
			if pe.Hint == "" {
				t.Errorf("expected actionable Hint")
			}
		})
	}
}

func TestParseJSONUnknownFieldRejection(t *testing.T) {
	unknownFieldJSON := `{
		"kind": "graph",
		"unsupported_property": true,
		"nodes": [{"id": "a"}]
	}`

	_, err := ParseJSON([]byte(unknownFieldJSON))
	if err == nil {
		t.Fatal("expected error for unknown field in JSON, got nil")
	}

	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %T: %v", err, err)
	}

	if pe.Stage != "schema" {
		t.Errorf("expected stage 'schema', got %q", pe.Stage)
	}
	if !strings.Contains(pe.Detail, "unknown field") {
		t.Errorf("expected detail to mention unknown field, got %q", pe.Detail)
	}
}

func TestParseJSONContractValidations(t *testing.T) {
	contractTests := []struct {
		name        string
		json        string
		expectField string
	}{
		{
			name:        "missing kind",
			json:        `{"nodes": [{"id": "a"}]}`,
			expectField: "kind",
		},
		{
			name:        "invalid kind",
			json:        `{"kind": "invalid_kind"}`,
			expectField: "kind",
		},
		{
			name:        "invalid direction",
			json:        `{"kind": "graph", "direction": "XYZ"}`,
			expectField: "direction",
		},
		{
			name:        "duplicate node id",
			json:        `{"kind": "graph", "nodes": [{"id": "a"}, {"id": "a"}]}`,
			expectField: "nodes[1].id",
		},
		{
			name:        "invalid node shape",
			json:        `{"kind": "graph", "nodes": [{"id": "a", "shape": "invalid_shape"}]}`,
			expectField: "nodes[0].shape",
		},
		{
			name:        "edge references non-existent node",
			json:        `{"kind": "graph", "nodes": [{"id": "a"}], "edges": [{"from": "a", "to": "b"}]}`,
			expectField: "edges[0].to",
		},
		{
			name:        "group references non-existent node",
			json:        `{"kind": "graph", "nodes": [{"id": "a"}], "groups": [{"members": ["a", "missing"]}]}`,
			expectField: "groups[0].members",
		},
	}

	for _, tt := range contractTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseJSON([]byte(tt.json))
			if err == nil {
				t.Fatalf("expected contract validation error for %s, got nil", tt.name)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}
			if pe.Stage != "contract" {
				t.Errorf("expected stage 'contract', got %q", pe.Stage)
			}
			if pe.Detail != tt.expectField {
				t.Errorf("expected detail %q, got %q", tt.expectField, pe.Detail)
			}
			if pe.Hint == "" {
				t.Errorf("expected actionable Hint")
			}
		})
	}
}

func TestSourceAutoDetection(t *testing.T) {
	mermaidSrc := "graph TD\nA --> B"
	jsonSrc := `{"kind": "graph", "nodes": [{"id": "A"}, {"id": "B"}], "edges": [{"from": "A", "to": "B"}]}`

	diag1, err := Source(mermaidSrc)
	if err != nil {
		t.Fatalf("Source auto-detect Mermaid failed: %v", err)
	}
	if len(diag1.Nodes) != 2 || len(diag1.Edges) != 1 {
		t.Errorf("unexpected counts for Mermaid: %+v", diag1)
	}

	diag2, err := Source(jsonSrc)
	if err != nil {
		t.Fatalf("Source auto-detect JSON failed: %v", err)
	}
	if len(diag2.Nodes) != 2 || len(diag2.Edges) != 1 {
		t.Errorf("unexpected counts for JSON: %+v", diag2)
	}
}
