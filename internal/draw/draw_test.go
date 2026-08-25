package draw

import (
	"bytes"
	"context"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

func sampleArchitectureIR() *ir.Diagram {
	return &ir.Diagram{
		Kind:      ir.KindGraph,
		Direction: ir.DirTD,
		Theme:     "symaira-dark",
		Nodes: []ir.Node{
			{ID: "ingest", Label: "Ingest Service", Shape: ir.ShapeRound, Note: "Projekte/Ingest.md"},
			{ID: "sidecar", Label: "Sidecar Database", Shape: ir.ShapeCylinder},
			{ID: "search", Label: "Search Index", Shape: ir.ShapeStadium},
		},
		Edges: []ir.Edge{
			{From: "ingest", To: "sidecar", Label: "derives", Style: ir.EdgeSolid, Arrow: ir.ArrowSingle},
			{From: "sidecar", To: "search", Label: "indexes", Style: ir.EdgeDashed, Arrow: ir.ArrowSingle},
		},
		Groups: []ir.Group{
			{Label: "Storage Layer", Members: []string{"sidecar", "search"}},
		},
	}
}

func sampleChartIR() *ir.Diagram {
	return &ir.Diagram{
		Kind:  ir.KindChart,
		Theme: "symaira-light",
		Chart: &ir.ChartSpec{
			Type:   ir.ChartBar,
			Title:  "Monthly Vault Growth",
			Legend: true,
			Series: []ir.Series{
				{
					Name: "Documents",
					Data: []ir.DataPoint{
						{Label: "Jan", Y: 120},
						{Label: "Feb", Y: 180},
						{Label: "Mar", Y: 240},
					},
				},
				{
					Name: "Assets",
					Data: []ir.DataPoint{
						{Label: "Jan", Y: 45},
						{Label: "Feb", Y: 90},
						{Label: "Mar", Y: 135},
					},
				},
			},
		},
	}
}

func sampleChartIRForType(chartType ir.ChartType) *ir.Diagram {
	diag := sampleChartIR()
	diag.Chart.Type = chartType
	if chartType == ir.ChartScatter {
		for seriesIndex := range diag.Chart.Series {
			for pointIndex := range diag.Chart.Series[seriesIndex].Data {
				diag.Chart.Series[seriesIndex].Data[pointIndex].X = float64(pointIndex + seriesIndex + 1)
			}
		}
	}
	return diag
}

func TestRenderPipelineSVGAndPNG(t *testing.T) {
	ctx := context.Background()
	diag := sampleArchitectureIR()

	// 1. Render SVG
	svgRes, err := Render(ctx, RenderRequest{
		IR:     diag,
		Format: FormatSVG,
	})
	if err != nil {
		t.Fatalf("Render SVG failed: %v", err)
	}
	if len(svgRes.Data) == 0 {
		t.Fatal("empty SVG data")
	}
	if svgRes.FontHash == "" {
		t.Fatal("missing FontHash in SVG result")
	}
	if !bytes.Contains(svgRes.Data, []byte("<svg")) || !bytes.Contains(svgRes.Data, []byte("</svg>")) {
		t.Fatal("malformed SVG output")
	}

	// 2. Render PNG
	pngRes, err := Render(ctx, RenderRequest{
		IR:     diag,
		Format: FormatPNG,
		Scale:  1.0,
	})
	if err != nil {
		t.Fatalf("Render PNG failed: %v", err)
	}
	if len(pngRes.Data) < 100 {
		t.Fatalf("PNG output unexpectedly small: %d bytes", len(pngRes.Data))
	}
	pngHeader := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	if !bytes.HasPrefix(pngRes.Data, pngHeader) {
		t.Fatal("malformed PNG output: missing magic bytes")
	}

	// 3. Render Chart SVG and PNG
	chartDiag := sampleChartIR()
	chartSVG, err := Render(ctx, RenderRequest{
		IR:     chartDiag,
		Format: FormatSVG,
	})
	if err != nil {
		t.Fatalf("Render Chart SVG failed: %v", err)
	}
	if !bytes.Contains(chartSVG.Data, []byte("Monthly Vault Growth")) {
		t.Fatal("chart title missing in SVG output")
	}

	chartPNG, err := Render(ctx, RenderRequest{
		IR:     chartDiag,
		Format: FormatPNG,
		Scale:  1.0,
	})
	if err != nil {
		t.Fatalf("Render Chart PNG failed: %v", err)
	}
	if len(chartPNG.Data) < 100 {
		t.Fatalf("Chart PNG too small: %d bytes", len(chartPNG.Data))
	}
}

func TestRenderDeterminism(t *testing.T) {
	ctx := context.Background()
	diag := sampleArchitectureIR()

	// Initial render
	baseSVG, err := Render(ctx, RenderRequest{IR: diag, Format: FormatSVG})
	if err != nil {
		t.Fatalf("initial SVG render failed: %v", err)
	}
	basePNG, err := Render(ctx, RenderRequest{IR: diag, Format: FormatPNG, Scale: 1.0})
	if err != nil {
		t.Fatalf("initial PNG render failed: %v", err)
	}

	// Repeat 50 times to guarantee strict byte-for-byte identical output
	for i := 0; i < 50; i++ {
		iterSVG, err := Render(ctx, RenderRequest{IR: diag, Format: FormatSVG})
		if err != nil {
			t.Fatalf("iteration %d SVG render failed: %v", i, err)
		}
		if !bytes.Equal(baseSVG.Data, iterSVG.Data) {
			t.Fatalf("SVG determinism violated at iteration %d", i)
		}

		iterPNG, err := Render(ctx, RenderRequest{IR: diag, Format: FormatPNG, Scale: 1.0})
		if err != nil {
			t.Fatalf("iteration %d PNG render failed: %v", i, err)
		}
		if !bytes.Equal(basePNG.Data, iterPNG.Data) {
			t.Fatalf("PNG determinism violated at iteration %d", i)
		}
	}
}

func TestPublicFacadeMethods(t *testing.T) {
	kinds := Kinds()
	if len(kinds) < 5 {
		t.Errorf("expected at least 5 diagram kinds, got %d", len(kinds))
	}

	themes := Themes()
	if len(themes) < 4 {
		t.Errorf("expected at least 4 themes, got %d", len(themes))
	}

	hash := FontHash()
	if len(hash) != 64 {
		t.Errorf("expected 64-character SHA-256 font hash, got %d: %q", len(hash), hash)
	}

	validJSON := `{"kind": "graph", "nodes": [{"id": "n1", "label": "Hello"}]}`
	if err := ValidateJSON([]byte(validJSON)); err != nil {
		t.Errorf("valid JSON rejected: %v", err)
	}

	invalidJSON := `{"kind": "unknown_kind"}`
	if err := ValidateJSON([]byte(invalidJSON)); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
