package emit

import (
	"bytes"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

func createSampleScene() *scene.Scene {
	th := theme.Resolve("symaira-dark")
	sc := scene.NewScene(400, 200, th)

	rect := &scene.RectElement{
		ID:          "node-a",
		X:           50,
		Y:           50,
		Width:       120,
		Height:      60,
		Rx:          8,
		Ry:          8,
		Fill:        th.Surface,
		Stroke:      th.Border,
		StrokeWidth: 2,
		Link:        "Projekte/Ingest.md",
	}

	txt := &scene.TextElement{
		X:          110,
		Y:          85,
		Text:       "Ingest",
		FontSize:   14,
		FontWeight: measure.WeightRegular,
		Fill:       th.Text,
		Anchor:     scene.AnchorMiddle,
		Baseline:   scene.BaselineMiddle,
	}

	circ := &scene.CircleElement{
		ID:          "node-b",
		CX:          300,
		CY:          80,
		R:           30,
		Fill:        th.SurfaceSubtle,
		Stroke:      th.Primary,
		StrokeWidth: 2,
	}

	txt2 := &scene.TextElement{
		X:          300,
		Y:          80,
		Text:       "Sidecar",
		FontSize:   12,
		FontWeight: measure.WeightBold,
		Fill:       th.Primary,
		Anchor:     scene.AnchorMiddle,
		Baseline:   scene.BaselineMiddle,
	}

	line := &scene.LineElement{
		ID:          "edge-a-b",
		X1:          170,
		Y1:          80,
		X2:          270,
		Y2:          80,
		Stroke:      th.Edge,
		StrokeWidth: 2,
		MarkerEnd:   "arrow-end",
	}

	sc.Add(rect, txt, circ, txt2, line)
	return sc
}

func TestEmitSVG(t *testing.T) {
	sc := createSampleScene()
	svgBytes, err := EmitSVG(sc, SVGEmitOptions{
		FontHash:  "test-font-hash",
		Generator: "symdraw",
	})
	if err != nil {
		t.Fatalf("EmitSVG failed: %v", err)
	}

	svgStr := string(svgBytes)
	if !bytes.Contains(svgBytes, []byte("<svg")) || !bytes.Contains(svgBytes, []byte("</svg>")) {
		t.Fatal("invalid SVG output: missing <svg> root tags")
	}
	if !bytes.Contains(svgBytes, []byte("test-font-hash")) {
		t.Error("missing font-hash metadata in SVG")
	}
	if !bytes.Contains(svgBytes, []byte("Projekte/Ingest.md")) {
		t.Error("missing node link in SVG")
	}
	if !bytes.Contains(svgBytes, []byte("textLength=")) {
		t.Error("missing textLength attribute in SVG text element")
	}

	// Test SVG Determinism: 50 successive runs must produce identical byte slice
	for i := 0; i < 50; i++ {
		repeatBytes, err := EmitSVG(sc, SVGEmitOptions{
			FontHash:  "test-font-hash",
			Generator: "symdraw",
		})
		if err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
		if !bytes.Equal(svgBytes, repeatBytes) {
			t.Fatalf("SVG determinism violation at run %d", i)
		}
	}
	_ = svgStr
}

func TestEmitSVGTextAsPaths(t *testing.T) {
	sc := createSampleScene()
	svgBytes, err := EmitSVG(sc, SVGEmitOptions{
		TextAsPaths: true,
		FontHash:    "test-font-hash",
	})
	if err != nil {
		t.Fatalf("EmitSVG with TextAsPaths failed: %v", err)
	}

	if bytes.Contains(svgBytes, []byte("<text")) {
		t.Error("TextAsPaths mode should not emit <text> elements")
	}
	if !bytes.Contains(svgBytes, []byte("<path")) {
		t.Error("TextAsPaths mode should emit vector <path> elements")
	}
}

func TestEmitPNG(t *testing.T) {
	sc := createSampleScene()
	pngBytes, err := EmitPNG(sc, RasterOptions{Scale: 1.0})
	if err != nil {
		t.Fatalf("EmitPNG failed: %v", err)
	}

	if len(pngBytes) < 100 {
		t.Fatalf("PNG output too small: %d bytes", len(pngBytes))
	}
	// Verify PNG magic header: \x89PNG\r\n\x1a\n
	pngHeader := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	if !bytes.HasPrefix(pngBytes, pngHeader) {
		t.Fatal("invalid PNG header")
	}

	// Test PNG Determinism
	for i := 0; i < 20; i++ {
		repeatPNG, err := EmitPNG(sc, RasterOptions{Scale: 1.0})
		if err != nil {
			t.Fatalf("PNG run %d failed: %v", i, err)
		}
		if !bytes.Equal(pngBytes, repeatPNG) {
			t.Fatalf("PNG determinism violation at run %d", i)
		}
	}
}

func TestEmitGIF(t *testing.T) {
	sc1 := createSampleScene()
	sc2 := createSampleScene()

	gifBytes, err := EmitGIF([]*scene.Scene{sc1, sc2}, 20, RasterOptions{Scale: 1.0})
	if err != nil {
		t.Fatalf("EmitGIF failed: %v", err)
	}
	if !bytes.HasPrefix(gifBytes, []byte("GIF89a")) && !bytes.HasPrefix(gifBytes, []byte("GIF87a")) {
		t.Fatal("invalid GIF magic header")
	}
}

func TestEmitTypst(t *testing.T) {
	sc := createSampleScene()
	typstCode, err := EmitTypst(sc)
	if err != nil {
		t.Fatalf("EmitTypst failed: %v", err)
	}
	if !bytes.Contains([]byte(typstCode), []byte("#box(")) {
		t.Fatal("expected #box in Typst output")
	}
}
