package ir

import (
	"testing"
)

func TestIRValidation(t *testing.T) {
	validJSON := `{
		"kind": "graph",
		"direction": "TD",
		"nodes": [
			{"id": "a", "label": "Ingest", "shape": "round", "note": "Projekte/Ingest.md"},
			{"id": "b", "label": "Sidecar", "shape": "cylinder"}
		],
		"edges": [
			{"from": "a", "to": "b", "label": "derives", "style": "solid", "arrow": "single"}
		],
		"groups": [
			{"label": "Core", "members": ["a", "b"]}
		]
	}`

	d, err := FromJSON([]byte(validJSON))
	if err != nil {
		t.Fatalf("expected valid diagram, got error: %v", err)
	}

	if d.Kind != KindGraph {
		t.Errorf("expected kind %q, got %q", KindGraph, d.Kind)
	}
	if len(d.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(d.Nodes))
	}
	if len(d.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(d.Edges))
	}
	if len(d.Groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(d.Groups))
	}

	out, err := ToJSON(d)
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty JSON output")
	}
}

func TestIRInvalidCases(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"missing kind", `{}`},
		{"invalid kind", `{"kind": "foobar"}`},
		{"empty node id", `{"kind": "graph", "nodes": [{"id": ""}]}`},
		{"duplicate node id", `{"kind": "graph", "nodes": [{"id": "a"}, {"id": "a"}]}`},
		{"unknown edge from", `{"kind": "graph", "nodes": [{"id": "a"}], "edges": [{"from": "b", "to": "a"}]}`},
		{"unknown edge to", `{"kind": "graph", "nodes": [{"id": "a"}], "edges": [{"from": "a", "to": "b"}]}`},
		{"unknown group member", `{"kind": "graph", "nodes": [{"id": "a"}], "groups": [{"members": ["missing"]}]}`},
		{"invalid shape", `{"kind": "graph", "nodes": [{"id": "a", "shape": "star"}]}`},
		{"invalid edge style", `{"kind": "graph", "nodes": [{"id": "a"}, {"id": "b"}], "edges": [{"from": "a", "to": "b", "style": "zigzag"}]}`},
		{"chart missing spec", `{"kind": "chart"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromJSON([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected error for case %q, got nil", tc.name)
			}
		})
	}
}
