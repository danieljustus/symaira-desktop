package draw

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// testScene builds a tiny scene with three primitives for animation tests.
func testScene(t *testing.T) *scene.Scene {
	t.Helper()
	th := theme.Resolve("symaira-dark")
	sc := scene.NewScene(100, 100, th)
	for i := 0; i < 3; i++ {
		sc.Add(&scene.RectElement{
			X: 10, Y: 10 + float64(i)*20, Width: 20, Height: 15,
			Fill: th.Surface, Stroke: th.Border,
		})
	}
	return sc
}

// TestAnimateFramesEvenDistribution verifies the default path reveals one
// primitive per frame and the last frame contains everything.
func TestAnimateFramesEvenDistribution(t *testing.T) {
	sc := testScene(t)
	frames, err := AnimateFrames(sc, AnimateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	if len(frames[0].Primitives) != 1 {
		t.Errorf("frame 0 has %d primitives, want 1", len(frames[0].Primitives))
	}
	if len(frames[2].Primitives) != 3 {
		t.Errorf("last frame has %d primitives, want 3", len(frames[2].Primitives))
	}
}

// TestAnimateFramesExplicitSteps verifies named steps reveal batches and
// carry forward into later frames.
func TestAnimateFramesExplicitSteps(t *testing.T) {
	sc := testScene(t)
	frames, err := AnimateFrames(sc, AnimateOptions{
		Frames: 2,
		Steps:  [][]int{{0}, {1, 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if len(frames[0].Primitives) != 1 {
		t.Errorf("step 0 has %d primitives, want 1", len(frames[0].Primitives))
	}
	if len(frames[1].Primitives) != 3 {
		t.Errorf("step 1 has %d primitives, want 3 (carry-forward)", len(frames[1].Primitives))
	}
}

// TestAnimateFramesNilScene guards the API against nil input.
func TestAnimateFramesNilScene(t *testing.T) {
	if _, err := AnimateFrames(nil, AnimateOptions{}); err == nil {
		t.Error("expected error for nil scene")
	}
}

// TestAnimateFramesEmptyScene guards against scenes without primitives.
func TestAnimateFramesEmptyScene(t *testing.T) {
	th := theme.Resolve("symaira-dark")
	sc := scene.NewScene(100, 100, th)
	if _, err := AnimateFrames(sc, AnimateOptions{}); err == nil {
		t.Error("expected error for empty scene")
	}
}
