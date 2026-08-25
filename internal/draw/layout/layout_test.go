package layout

import (
	"reflect"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

func layeredFixture() *ir.Diagram {
	return &ir.Diagram{
		Kind:      ir.KindGraph,
		Direction: ir.DirTD,
		Nodes: []ir.Node{
			{ID: "a", Label: "Start"},
			{ID: "b", Label: "Middle"},
			{ID: "c", Label: "Branch"},
			{ID: "d", Label: "Finish"},
		},
		Edges: []ir.Edge{
			{From: "a", To: "d"},
			{From: "a", To: "b"},
			{From: "b", To: "c"},
			{From: "c", To: "a"}, // cycle, must be reversed deterministically
			{From: "c", To: "d"},
		},
	}
}

func TestLayoutDeterministicAndBounded(t *testing.T) {
	first, err := Layout(layeredFixture())
	if err != nil {
		t.Fatalf("layout failed: %v", err)
	}
	second, err := Layout(layeredFixture())
	if err != nil {
		t.Fatalf("second layout failed: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("layout is not deterministic")
	}
	if first.Crossings < 0 {
		t.Fatalf("invalid crossing count: %d", first.Crossings)
	}

	for id, box := range first.Nodes {
		if box.X < 0 || box.Y < 0 || box.X+box.Width > first.Width || box.Y+box.Height > first.Height {
			t.Fatalf("node %q is outside bounds: box=%+v result=%gx%g", id, box, first.Width, first.Height)
		}
	}
	for layerIndex, ids := range first.Layers {
		for i := range ids {
			left := first.Nodes[ids[i]]
			for _, rightID := range ids[i+1:] {
				right := first.Nodes[rightID]
				if overlaps(left, right) {
					t.Fatalf("nodes overlap in layer %d: %q and %q", layerIndex, ids[i], rightID)
				}
			}
		}
	}

	if len(first.Dummies) == 0 {
		t.Fatal("long edge did not produce a dummy node")
	}
	for index, route := range first.Routes {
		if len(route.Points) < 2 {
			t.Fatalf("edge %d has no usable route: %+v", index, route)
		}
	}
	t.Logf("layout baseline: nodes=%d dummies=%d layers=%d crossings=%d size=%.0fx%.0f", len(first.Nodes), len(first.Dummies), len(first.Layers), first.Crossings, first.Width, first.Height)
}

func TestLayoutRejectsInvalidGraph(t *testing.T) {
	if _, err := Layout(&ir.Diagram{Kind: ir.KindChart}); err == nil {
		t.Fatal("expected non-graph input to be rejected")
	}
	if _, err := Layout(&ir.Diagram{Kind: ir.KindGraph, Nodes: []ir.Node{{ID: "a"}, {ID: "a"}}}); err == nil {
		t.Fatal("expected duplicate node IDs to be rejected")
	}
}

func BenchmarkLayout(b *testing.B) {
	fixture := layeredFixture()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Layout(fixture); err != nil {
			b.Fatal(err)
		}
	}
}

func overlaps(left, right Box) bool {
	return left.X < right.X+right.Width && right.X < left.X+left.Width &&
		left.Y < right.Y+right.Height && right.Y < left.Y+left.Height
}
