package draw

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

var updateGolden = flag.Bool("update-golden", false, "update golden test files")

func TestGoldenOutputs(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		req       RenderRequest
		goldenSVG string
		goldenPNG string
	}{
		{
			name: "architecture_dark",
			req: RenderRequest{
				IR:     sampleArchitectureIR(),
				Format: FormatSVG,
			},
			goldenSVG: "testdata/golden/architecture_dark.svg",
			goldenPNG: "testdata/golden/architecture_dark.png",
		},
		{
			name: "chart_light",
			req: RenderRequest{
				IR:     sampleChartIR(),
				Format: FormatSVG,
			},
			goldenSVG: "testdata/golden/chart_light.svg",
			goldenPNG: "testdata/golden/chart_light.png",
		},
		{
			name: "architecture_text_as_paths",
			req: RenderRequest{
				IR:          sampleArchitectureIR(),
				Format:      FormatSVG,
				TextAsPaths: true,
			},
			goldenSVG: "testdata/golden/architecture_text_as_paths.svg",
		},
		{
			name: "report_profile",
			req: RenderRequest{
				IR: &ir.Diagram{
					Kind:  ir.KindGraph,
					Theme: "report",
					Nodes: []ir.Node{
						{ID: "node1", Label: "Quartalsbericht", Shape: ir.ShapeRound},
						{ID: "node2", Label: "Genehmigung", Shape: ir.ShapeStadium},
					},
					Edges: []ir.Edge{
						{From: "node1", To: "node2", Label: "vorlegen"},
					},
				},
				Format: FormatSVG,
			},
			goldenSVG: "testdata/golden/report_profile.svg",
			goldenPNG: "testdata/golden/report_profile.png",
		},
		{
			name: "meeting_profile",
			req: RenderRequest{
				IR: &ir.Diagram{
					Kind:  ir.KindGraph,
					Theme: "meeting",
					Nodes: []ir.Node{
						{ID: "m1", Label: "Agenda", Shape: ir.ShapeRect},
						{ID: "m2", Label: "Beschluss", Shape: ir.ShapeDiamond},
					},
					Edges: []ir.Edge{
						{From: "m1", To: "m2", Label: "abstimmen"},
					},
				},
				Format: FormatSVG,
			},
			goldenSVG: "testdata/golden/meeting_profile.svg",
			goldenPNG: "testdata/golden/meeting_profile.png",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// SVG
			svgReq := tc.req
			svgReq.Format = FormatSVG
			resSVG, err := Render(ctx, svgReq)
			if err != nil {
				t.Fatalf("render SVG failed: %v", err)
			}

			if tc.goldenSVG != "" {
				verifyOrUpdateGolden(t, tc.goldenSVG, resSVG.Data)
			}

			// PNG
			if tc.goldenPNG != "" {
				pngReq := tc.req
				pngReq.Format = FormatPNG
				pngReq.Scale = 1.0
				resPNG, err := Render(ctx, pngReq)
				if err != nil {
					t.Fatalf("render PNG failed: %v", err)
				}
				verifyOrUpdateGolden(t, tc.goldenPNG, resPNG.Data)
			}
		})
	}
}

func verifyOrUpdateGolden(t *testing.T, goldenPath string, actual []byte) {
	t.Helper()

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o700); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, actual, 0o600); err != nil {
			t.Fatalf("write golden file %s: %v", goldenPath, err)
		}
		return
	}

	expected, err := os.ReadFile(goldenPath) // #nosec G304 -- paths come from the fixed golden test table.
	if err != nil {
		if os.IsNotExist(err) {
			// Auto-generate if missing
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o700); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, actual, 0o600); err != nil {
				t.Fatalf("write initial golden file %s: %v", goldenPath, err)
			}
			return
		}
		t.Fatalf("read golden file %s: %v", goldenPath, err)
	}

	if !bytes.Equal(expected, actual) {
		t.Fatalf("golden mismatch for %s: actual bytes differ from expected golden file", goldenPath)
	}
}
