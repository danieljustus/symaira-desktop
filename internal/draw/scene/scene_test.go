package scene

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

func TestSceneBoundsAndFit(t *testing.T) {
	th := theme.Resolve("symaira-dark")
	sc := NewScene(400, 300, th)

	rect := &RectElement{
		X:           50,
		Y:           60,
		Width:       100,
		Height:      50,
		StrokeWidth: 2,
	}
	circ := &CircleElement{
		CX:          250,
		CY:          200,
		R:           30,
		StrokeWidth: 2,
	}
	line := &LineElement{
		X1:          100,
		Y1:          100,
		X2:          300,
		Y2:          250,
		StrokeWidth: 4,
	}

	sc.Add(rect, circ, line)

	b := sc.ComputeBounds()
	if b.X > 50 || b.Y > 60 {
		t.Errorf("bounds upper-left incorrect: %+v", b)
	}
	if b.MaxX() < 300 || b.MaxY() < 250 {
		t.Errorf("bounds lower-right incorrect: %+v", b)
	}

	for i, p := range sc.Primitives {
		pb := p.Bounds()
		if !b.Contains(pb) {
			t.Errorf("scene bounds %+v do not contain primitive [%d] bounds %+v", b, i, pb)
		}
	}

	sc.FitToBounds(20)
	if sc.Width <= b.Width || sc.Height <= b.Height {
		t.Errorf("fitted dimensions (%.2f, %.2f) must exceed raw bounds (%.2f, %.2f)", sc.Width, sc.Height, b.Width, b.Height)
	}

	if err := sc.Validate(); err != nil {
		t.Fatalf("scene validation failed: %v", err)
	}
}

func TestTextPrimitiveBounds(t *testing.T) {
	txt := &TextElement{
		X:          100,
		Y:          100,
		Text:       "Hello World",
		FontSize:   14,
		FontWeight: measure.WeightRegular,
		Anchor:     AnchorStart,
		Baseline:   BaselineAlphabetic,
	}

	b := txt.Bounds()
	if b.Width <= 0 || b.Height <= 0 {
		t.Fatalf("invalid text bounds: %+v", b)
	}
	if b.X != 100 {
		t.Errorf("expected text bounds X=100 for AnchorStart, got %.2f", b.X)
	}
}
