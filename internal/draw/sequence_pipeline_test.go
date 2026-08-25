package draw

import (
	"strings"
	"testing"
)

// TestSequencePipelineEndToEnd exercises the full sequence path: Mermaid
// source -> parse -> closed-form layout -> scene -> SVG emission.
func TestSequencePipelineEndToEnd(t *testing.T) {
	src := "sequenceDiagram\n    participant Alice\n    participant Bob\n    Alice->>Bob: Hello Bob\n    Bob-->>Alice: Hi Alice\n"
	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "sequence" {
		t.Fatalf("kind = %q, want sequence", d.Kind)
	}
	sc, err := BuildSceneFromIR(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Primitives) == 0 {
		t.Fatal("scene has no primitives")
	}
	out, err := RenderScene(sc, FormatSVG)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<svg") {
		t.Errorf("output is not SVG: %s", string(out)[:min(100, len(out))])
	}
	if !strings.Contains(string(out), "Hello Bob") {
		t.Errorf("SVG does not contain message text")
	}
	if !strings.Contains(string(out), "Alice") {
		t.Errorf("SVG does not contain actor label")
	}
}
