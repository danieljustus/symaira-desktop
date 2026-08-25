package parse

import (
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

func TestParseBasicFlowcharts(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantDir   ir.Direction
		wantNodes int
		wantEdges int
	}{
		{
			name:      "graph TD default",
			src:       "graph TD\nA --> B",
			wantDir:   ir.DirTD,
			wantNodes: 2,
			wantEdges: 1,
		},
		{
			name:      "flowchart LR",
			src:       "flowchart LR\nA --> B\nB --> C",
			wantDir:   ir.DirLR,
			wantNodes: 3,
			wantEdges: 2,
		},
		{
			name:      "graph BT lowercase",
			src:       "graph bt\nnode1 --> node2",
			wantDir:   ir.DirBT,
			wantNodes: 2,
			wantEdges: 1,
		},
		{
			name:      "flowchart RL uppercase",
			src:       "FLOWCHART RL\nStart --> End",
			wantDir:   ir.DirRL,
			wantNodes: 2,
			wantEdges: 1,
		},
		{
			name:      "graph TB synonym for TD",
			src:       "graph TB\nTop --> Bottom",
			wantDir:   ir.DirTD,
			wantNodes: 2,
			wantEdges: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := ParseMermaid(tt.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Kind != ir.KindGraph {
				t.Errorf("expected KindGraph, got %q", d.Kind)
			}
			if d.Direction != tt.wantDir {
				t.Errorf("expected direction %s, got %s", tt.wantDir, d.Direction)
			}
			if len(d.Nodes) != tt.wantNodes {
				t.Errorf("expected %d nodes, got %d", tt.wantNodes, len(d.Nodes))
			}
			if len(d.Edges) != tt.wantEdges {
				t.Errorf("expected %d edges, got %d", tt.wantEdges, len(d.Edges))
			}
		})
	}
}

func TestParseAllSupportedShapes(t *testing.T) {
	src := `graph TD
	a[Rectangle]
	b(Round)
	c([Stadium])
	d[[Subroutine]]
	e[(Cylinder)]
	f((Circle))
	g>Asymmetric]
	h{Diamond}
	i{{Hexagon}}
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("failed to parse shapes: %v", err)
	}

	expected := map[string]struct {
		label string
		shape ir.NodeShape
	}{
		"a": {"Rectangle", ir.ShapeRect},
		"b": {"Round", ir.ShapeRound},
		"c": {"Stadium", ir.ShapeStadium},
		"d": {"Subroutine", ir.ShapeSubroutine},
		"e": {"Cylinder", ir.ShapeCylinder},
		"f": {"Circle", ir.ShapeCircle},
		"g": {"Asymmetric", ir.ShapeAsymmetric},
		"h": {"Diamond", ir.ShapeDiamond},
		"i": {"Hexagon", ir.ShapeHexagon},
	}

	if len(d.Nodes) != len(expected) {
		t.Fatalf("expected %d nodes, got %d", len(expected), len(d.Nodes))
	}

	for _, n := range d.Nodes {
		exp, ok := expected[n.ID]
		if !ok {
			t.Errorf("unexpected node ID %q", n.ID)
			continue
		}
		if n.Label != exp.label {
			t.Errorf("node %s: expected label %q, got %q", n.ID, exp.label, n.Label)
		}
		if n.Shape != exp.shape {
			t.Errorf("node %s: expected shape %q, got %q", n.ID, exp.shape, n.Shape)
		}
	}
}

func TestParseQuotedLabelsAndEscapes(t *testing.T) {
	src := `graph TD
	A["Label with [brackets] and (parentheses) and {braces}"]
	B['Single quoted label']
	C["Label with \"escaped\" quotes"]
	A --> B
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(d.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(d.Nodes))
	}

	if d.Nodes[0].Label != "Label with [brackets] and (parentheses) and {braces}" {
		t.Errorf("unexpected label for A: %q", d.Nodes[0].Label)
	}
	if d.Nodes[1].Label != "Single quoted label" {
		t.Errorf("unexpected label for B: %q", d.Nodes[1].Label)
	}
	if d.Nodes[2].Label != "Label with \"escaped\" quotes" {
		t.Errorf("unexpected label for C: %q", d.Nodes[2].Label)
	}
}

func TestParseWikilinks(t *testing.T) {
	src := `graph TD
	A[[Vault Note]] --> B["[[Projekte/Alpha|Project Alpha]]"]
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(d.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(d.Nodes))
	}

	if d.Nodes[0].Note != "Vault Note" || d.Nodes[0].Label != "Vault Note" {
		t.Errorf("node A: note=%q label=%q", d.Nodes[0].Note, d.Nodes[0].Label)
	}
	if d.Nodes[1].Note != "Projekte/Alpha" || d.Nodes[1].Label != "Project Alpha" {
		t.Errorf("node B: note=%q label=%q", d.Nodes[1].Note, d.Nodes[1].Label)
	}
}

func TestParseEdgeConnectorsAndLabels(t *testing.T) {
	src := `graph TD
	A --> B
	B --- C
	C <--> D
	D -.-> E
	E -.- F
	F ==> G
	G === H
	H ..-> I
	I ... J
	J --o K
	K --x L
	L o--o M
	M x--x N
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(d.Edges) != 13 {
		t.Fatalf("expected 13 edges, got %d", len(d.Edges))
	}

	type edgeCheck struct {
		from  string
		to    string
		style ir.EdgeStyle
		arrow ir.ArrowType
	}

	checks := []edgeCheck{
		{"A", "B", ir.EdgeSolid, ir.ArrowSingle},
		{"B", "C", ir.EdgeSolid, ir.ArrowNone},
		{"C", "D", ir.EdgeSolid, ir.ArrowDouble},
		{"D", "E", ir.EdgeDashed, ir.ArrowSingle},
		{"E", "F", ir.EdgeDashed, ir.ArrowNone},
		{"F", "G", ir.EdgeThick, ir.ArrowSingle},
		{"G", "H", ir.EdgeThick, ir.ArrowNone},
		{"H", "I", ir.EdgeDotted, ir.ArrowSingle},
		{"I", "J", ir.EdgeDotted, ir.ArrowNone},
		{"J", "K", ir.EdgeSolid, ir.ArrowCircle},
		{"K", "L", ir.EdgeSolid, ir.ArrowCross},
		{"L", "M", ir.EdgeSolid, ir.ArrowCircle},
		{"M", "N", ir.EdgeSolid, ir.ArrowCross},
	}

	for i, c := range checks {
		e := d.Edges[i]
		if e.From != c.from || e.To != c.to || e.Style != c.style || e.Arrow != c.arrow {
			t.Errorf("edge %d mismatch: got (%s->%s, %s, %s), want (%s->%s, %s, %s)",
				i, e.From, e.To, e.Style, e.Arrow, c.from, c.to, c.style, c.arrow)
		}
	}
}

func TestParseEdgeLabelSyntaxes(t *testing.T) {
	src := `graph TD
	A -->|pipe label| B
	B -- inline label --> C
	C -. dashed label .-> D
	D == thick label ==> E
	E ---|solid line label| F
	F -- solid line inline --- G
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	expectedLabels := []string{
		"pipe label",
		"inline label",
		"dashed label",
		"thick label",
		"solid line label",
		"solid line inline",
	}

	if len(d.Edges) != len(expectedLabels) {
		t.Fatalf("expected %d edges, got %d", len(expectedLabels), len(d.Edges))
	}

	for i, exp := range expectedLabels {
		if d.Edges[i].Label != exp {
			t.Errorf("edge %d: expected label %q, got %q", i, exp, d.Edges[i].Label)
		}
	}
}

func TestParseChainedEdgesAndMultiNodes(t *testing.T) {
	src := `graph TD
	A[Start] -->|s1| B(Process) -->|s2| C{Decision} -->|yes| D[End]
	X & Y --> Z & W
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	// Chained: 3 edges
	// Multi-node: 2x2 = 4 edges
	// Total: 7 edges
	if len(d.Edges) != 7 {
		t.Fatalf("expected 7 edges, got %d", len(d.Edges))
	}

	if d.Edges[0].From != "A" || d.Edges[0].To != "B" || d.Edges[0].Label != "s1" {
		t.Errorf("edge 0: %+v", d.Edges[0])
	}
	if d.Edges[1].From != "B" || d.Edges[1].To != "C" || d.Edges[1].Label != "s2" {
		t.Errorf("edge 1: %+v", d.Edges[1])
	}
	if d.Edges[2].From != "C" || d.Edges[2].To != "D" || d.Edges[2].Label != "yes" {
		t.Errorf("edge 2: %+v", d.Edges[2])
	}
}

func TestParseSubgraphs(t *testing.T) {
	src := `graph TD
	subgraph Core [Core Engine]
		A[Ingest] --> B[(Database)]
	end
	subgraph Storage [Storage Layer]
		B --> C[Index]
	end
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(d.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(d.Groups))
	}

	g1 := d.Groups[0]
	if g1.ID != "Core" || g1.Label != "Core Engine" {
		t.Errorf("group 1: ID=%q, Label=%q", g1.ID, g1.Label)
	}
	if len(g1.Members) != 2 || g1.Members[0] != "A" || g1.Members[1] != "B" {
		t.Errorf("group 1 members: %v", g1.Members)
	}

	g2 := d.Groups[1]
	if g2.ID != "Storage" || g2.Label != "Storage Layer" {
		t.Errorf("group 2: ID=%q, Label=%q", g2.ID, g2.Label)
	}
	if len(g2.Members) != 2 || g2.Members[0] != "B" || g2.Members[1] != "C" {
		t.Errorf("group 2 members: %v", g2.Members)
	}
}

func TestParseCommentsAndSemicolons(t *testing.T) {
	src := `%% Header comment
	graph TD
	%% Node declarations
	A[Start]; B(Process); C[End];
	A --> B; B --> C %% Inline comment
	`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(d.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(d.Nodes))
	}
	if len(d.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(d.Edges))
	}
}

func TestParseFrontmatter(t *testing.T) {
	src := `---
title: System Architecture
theme: symaira-dark
---
graph TD
A --> B
`

	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if d.Title != "System Architecture" {
		t.Errorf("expected title 'System Architecture', got %q", d.Title)
	}
	if d.Theme != "symaira-dark" {
		t.Errorf("expected theme 'symaira-dark', got %q", d.Theme)
	}
}

func TestUnsupportedDiagramTypesProduceTypedError(t *testing.T) {
	unsupportedSources := []struct {
		name string
		src  string
		kind string
	}{
		{"sequence", "sequenceDiagram\nAlice->>Bob: Hi", "sequenceDiagram"},
		{"pie", "pie\n\"A\": 10\n\"B\": 20", "pie"},
		{"class", "classDiagram\nclass Animal", "classDiagram"},
		{"state", "stateDiagram\n[*] --> State1", "stateDiagram"},
		{"er", "erDiagram\nCUSTOMER ||--o{ ORDER : places", "erDiagram"},
		{"gantt", "gantt\ntitle Roadmap\nsection Task", "gantt"},
		{"gitGraph", "gitGraph\ncommit", "gitGraph"},
		{"journey", "journey\ntitle My Journey", "journey"},
		{"mindmap", "mindmap\nroot((Root))", "mindmap"},
	}

	for _, tt := range unsupportedSources {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMermaid(tt.src)
			if err == nil {
				t.Fatalf("expected error for unsupported diagram %q, got nil", tt.kind)
			}

			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}

			if pe.Stage != "parse" {
				t.Errorf("expected stage 'parse', got %q", pe.Stage)
			}
			if !strings.Contains(strings.ToLower(pe.Message), strings.ToLower(tt.kind)) {
				t.Errorf("expected message to name %q, got %q", tt.kind, pe.Message)
			}
			if pe.Hint == "" {
				t.Errorf("expected non-empty Hint in ParseError")
			}
			if pe.Line != 1 {
				t.Errorf("expected error line 1, got %d", pe.Line)
			}
		})
	}
}

func TestUnsupportedDirectivesProduceTypedError(t *testing.T) {
	unsupportedDirectives := []struct {
		name      string
		src       string
		construct string
	}{
		{"click", "graph TD\nA --> B\nclick A \"https://example.com\"", "click"},
		{"style", "graph TD\nA --> B\nstyle A fill:#f9f,stroke:#333", "style"},
		{"classDef", "graph TD\nA --> B\nclassDef myClass fill:#f9f", "classDef"},
		{"class", "graph TD\nA --> B\nclass A myClass", "class"},
		{"linkStyle", "graph TD\nA --> B\nlinkStyle 0 stroke:#ff3", "linkStyle"},
		{"accTitle", "graph TD\naccTitle: Diagram Title\nA --> B", "accTitle"},
		{"accDescr", "graph TD\naccDescr: Accessible description\nA --> B", "accDescr"},
		{"direction inside subgraph", "graph TD\nsubgraph Sub\ndirection LR\nA --> B\nend", "direction"},
		{"invisible link", "graph TD\nA ~~~ B", "invisible link '~~~'"},
		{"init directive", "graph TD\n%%{init: {'theme':'dark'}}%%\nA --> B", "directive %%{init:...}%%"},
	}

	for _, tt := range unsupportedDirectives {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMermaid(tt.src)
			if err == nil {
				t.Fatalf("expected error for unsupported directive %q, got nil", tt.construct)
			}

			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}

			if pe.Stage != "parse" {
				t.Errorf("expected stage 'parse', got %q", pe.Stage)
			}
			if !strings.Contains(strings.ToLower(pe.Message), strings.ToLower(tt.construct)) {
				t.Errorf("expected message to name %q, got %q", tt.construct, pe.Message)
			}
			if pe.Hint == "" {
				t.Errorf("expected actionable Hint, got empty")
			}
		})
	}
}

func TestUnsupportedShapesProduceTypedError(t *testing.T) {
	unsupportedShapes := []struct {
		name string
		src  string
	}{
		{"parallelogram", "graph TD\nA[/Parallelogram/]\n"},
		{"parallelogram alt", "graph TD\nA[\\Parallelogram\\]\n"},
		{"trapezoid", "graph TD\nA[/Trapezoid\\]\n"},
		{"inverted trapezoid", "graph TD\nA[\\Inverted Trapezoid/]\n"},
		{"double circle", "graph TD\nA(((Double Circle)))\n"},
	}

	for _, tt := range unsupportedShapes {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMermaid(tt.src)
			if err == nil {
				t.Fatalf("expected error for unsupported shape, got nil")
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

func TestSyntaxErrorsProduceUsableErrors(t *testing.T) {
	syntaxErrors := []struct {
		name        string
		src         string
		errContains string
	}{
		{"empty input", "", "empty diagram source"},
		{"comment only", "%% just a comment\n", "empty diagram source"},
		{"invalid direction", "graph INVALID_DIR\nA --> B", "direction"},
		{"missing target", "graph TD\nA -->", "missing target node"},
		{"unclosed delimiter", "graph TD\nA[Unclosed label", "unclosed"},
		{"unclosed subgraph", "graph TD\nsubgraph Sub\nA --> B\n", "unclosed subgraph"},
		{"unexpected end", "graph TD\nA --> B\nend", "unexpected 'end'"},
	}

	for _, tt := range syntaxErrors {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMermaid(tt.src)
			if err == nil {
				t.Fatalf("expected syntax error for %q, got nil", tt.name)
			}

			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}

			if !strings.Contains(strings.ToLower(pe.Error()), strings.ToLower(tt.errContains)) {
				t.Errorf("expected error to contain %q, got %q", tt.errContains, pe.Error())
			}
		})
	}
}

func TestSwiftTestParityCases(t *testing.T) {
	// Pinned cases from MarkdownPreviewTests.swift in Swift:

	// 1. testParsesSimpleFlowchart: "graph TD\nA[Start] --> B{Decision}\nB --> C[End]"
	t.Run("Swift parity simple flowchart", func(t *testing.T) {
		d, err := ParseMermaid("graph TD\nA[Start] --> B{Decision}\nB --> C[End]")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Direction != ir.DirTD {
			t.Errorf("direction: got %s, want TD", d.Direction)
		}
		if len(d.Nodes) != 3 {
			t.Fatalf("nodes: got %d, want 3", len(d.Nodes))
		}
		if d.Nodes[0].Label != "Start" {
			t.Errorf("node 0 label: got %q, want 'Start'", d.Nodes[0].Label)
		}
		if len(d.Edges) != 2 {
			t.Fatalf("edges: got %d, want 2", len(d.Edges))
		}
		if d.Edges[0].From != "A" || d.Edges[0].To != "B" {
			t.Errorf("edge 0: %+v", d.Edges[0])
		}
	})

	// 2. testParsesEdgeLabels: "flowchart LR\nA -->|yes| B"
	t.Run("Swift parity edge labels", func(t *testing.T) {
		d, err := ParseMermaid("flowchart LR\nA -->|yes| B")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Direction != ir.DirLR {
			t.Errorf("direction: got %s, want LR", d.Direction)
		}
		if len(d.Edges) != 1 || d.Edges[0].Label != "yes" {
			t.Errorf("edge label: got %+v, want label 'yes'", d.Edges)
		}
	})

	// 3. testUnsupportedDiagramReturnsNil (in Go: returns typed ParseError)
	t.Run("Swift parity sequence diagram rejection", func(t *testing.T) {
		_, err := ParseMermaid("sequenceDiagram\nAlice->>Bob: Hi")
		if err == nil {
			t.Fatal("expected rejection of sequenceDiagram")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected *ParseError, got %T", err)
		}
		if !strings.Contains(pe.Message, "sequenceDiagram") {
			t.Errorf("expected message to contain sequenceDiagram, got %q", pe.Message)
		}
	})

	t.Run("Swift parity pie diagram rejection", func(t *testing.T) {
		_, err := ParseMermaid("pie\n\"a\": 1")
		if err == nil {
			t.Fatal("expected rejection of pie")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("expected *ParseError, got %T", err)
		}
		if !strings.Contains(pe.Message, "pie") {
			t.Errorf("expected message to contain pie, got %q", pe.Message)
		}
	})

	// 4. Chain and cycle
	t.Run("Swift parity chain", func(t *testing.T) {
		d, err := ParseMermaid("graph TD\nA --> B\nB --> C")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(d.Nodes) != 3 || len(d.Edges) != 2 {
			t.Errorf("got %d nodes, %d edges", len(d.Nodes), len(d.Edges))
		}
	})

	t.Run("Swift parity cycle", func(t *testing.T) {
		d, err := ParseMermaid("graph TD\nA --> B\nB --> A")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(d.Nodes) != 2 || len(d.Edges) != 2 {
			t.Errorf("got %d nodes, %d edges", len(d.Nodes), len(d.Edges))
		}
	})
}
