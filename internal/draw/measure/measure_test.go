package measure

import (
	"math"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

func TestMeasurerMetrics(t *testing.T) {
	m := Default()

	// Test font metrics matching SYMDRAW.md §3 spike
	text := "Datenbank-Migration"
	metrics, err := m.MeasureTextDetailed(text, 14.0, WeightRegular)
	if err != nil {
		t.Fatalf("measure text: %v", err)
	}

	if math.Abs(metrics.Width-140.17) > 0.1 {
		t.Errorf("expected width ~140.17 for %q, got %.2f", text, metrics.Width)
	}
	if math.Abs(metrics.Ascent-13.56) > 0.1 {
		t.Errorf("expected ascent ~13.56, got %.2f", metrics.Ascent)
	}
	if math.Abs(metrics.Descent-3.38) > 0.1 {
		t.Errorf("expected descent ~3.38, got %.2f", metrics.Descent)
	}
	if math.Abs(metrics.LineHeight-16.94) > 0.1 {
		t.Errorf("expected line height ~16.94, got %.2f", metrics.LineHeight)
	}

	text2 := "Kundenprojekt Alpha"
	metrics2, err := m.MeasureTextDetailed(text2, 14.0, WeightRegular)
	if err != nil {
		t.Fatalf("measure text2: %v", err)
	}
	if math.Abs(metrics2.Width-138.70) > 0.1 {
		t.Errorf("expected width ~138.70 for %q, got %.2f", text2, metrics2.Width)
	}
}

func TestLineBreaking(t *testing.T) {
	m := Default()

	text := "Short line\nAnother explicit line"
	res := m.BreakLines(text, 0, 14.0, WeightRegular)
	if len(res.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(res.Lines), res.Lines)
	}

	longText := "The quick brown fox jumps over the lazy dog"
	wrapped := m.BreakLines(longText, 100.0, 14.0, WeightRegular)
	if len(wrapped.Lines) < 3 {
		t.Errorf("expected wrapped text into at least 3 lines, got %d: %v", len(wrapped.Lines), wrapped.Lines)
	}
	for _, l := range wrapped.Lines {
		w, _ := m.MeasureText(l, 14.0, WeightRegular)
		if w > 105.0 {
			t.Errorf("line width %.2f exceeds max width 100: %q", w, l)
		}
	}
}

func TestMeasureNode(t *testing.T) {
	m := Default()
	opts := DefaultNodeMeasureOptions()

	w, h, lines := m.MeasureNode("Ingest Service", ir.ShapeRound, opts)
	if w <= 0 || h <= 0 {
		t.Fatalf("invalid node dimensions: %.2f x %.2f", w, h)
	}
	if len(lines) != 1 || lines[0] != "Ingest Service" {
		t.Errorf("expected 1 line, got %v", lines)
	}

	// Test circle shape expands
	wCirc, hCirc, _ := m.MeasureNode("Ingest Service", ir.ShapeCircle, opts)
	if wCirc != hCirc {
		t.Errorf("circle node width (%.2f) must equal height (%.2f)", wCirc, hCirc)
	}
	if wCirc <= w {
		t.Errorf("circle diameter %.2f should be larger than rect width %.2f", wCirc, w)
	}
}

func TestOutlines(t *testing.T) {
	m := Default()

	segs, err := m.Outlines("g", 14.0, WeightRegular, 0, 0)
	if err != nil {
		t.Fatalf("outlines error: %v", err)
	}
	if len(segs) < 10 {
		t.Errorf("expected at least 10 outline segments for 'g', got %d", len(segs))
	}

	svgPath := SegmentsToSVGPath(segs)
	if svgPath == "" || svgPath[0] != 'M' {
		t.Errorf("expected valid SVG path string starting with 'M', got %q", svgPath)
	}
}
