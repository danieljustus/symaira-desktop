package draw

import (
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
)

// AnimateOptions configures step-sequence animation over one diagram scene.
// The cheap animation shape from docs/SYMDRAW.md §14: a sequence of steps
// over one scene, emitted as raster frames or an animated vector graphic —
// deliberately not a separate animation system and not an interactive canvas.
type AnimateOptions struct {
	// Frames is the number of frames in the sequence. Each frame reveals one
	// additional batch of primitives (appear animation). Defaults to the
	// number of primitives (one per frame) when <= 0.
	Frames int
	// Steps optionally groups primitives into named steps: each step index
	// maps to the primitives revealed by that step. When empty, primitives
	// are distributed evenly across Frames.
	Steps [][]int
}

// AnimateFrames derives a step sequence of scenes from one positioned
// diagram scene. Frame i contains the primitives revealed up to step i, so
// passing the result to emit.EmitGIF produces an animated diagram.
func AnimateFrames(sc *scene.Scene, opts AnimateOptions) ([]*scene.Scene, error) {
	if sc == nil {
		return nil, fmt.Errorf("scene is nil")
	}
	if len(sc.Primitives) == 0 {
		return nil, fmt.Errorf("scene has no primitives to animate")
	}

	frames := opts.Frames
	if frames <= 0 {
		frames = len(sc.Primitives)
	}

	// Build the reveal schedule: for each frame, the set of primitive indices
	// that are visible. With explicit steps, every frame up to the step shows
	// the step's primitives; otherwise primitives are distributed evenly.
	visible := make([][]bool, frames)
	for f := 0; f < frames; f++ {
		visible[f] = make([]bool, len(sc.Primitives))
	}

	if len(opts.Steps) > 0 {
		for stepIdx, step := range opts.Steps {
			frame := stepIdx
			if frame >= frames {
				break
			}
			for _, p := range step {
				if p >= 0 && p < len(sc.Primitives) {
					visible[frame][p] = true
				}
			}
		}
		// Carry forward: a primitive revealed in an earlier step stays
		// visible in all later frames.
		for f := 1; f < frames; f++ {
			for p := range sc.Primitives {
				if visible[f-1][p] {
					visible[f][p] = true
				}
			}
		}
	} else {
		for p := range sc.Primitives {
			frame := p * frames / len(sc.Primitives)
			if frame >= frames {
				frame = frames - 1
			}
			for f := frame; f < frames; f++ {
				visible[f][p] = true
			}
		}
	}

	out := make([]*scene.Scene, 0, frames)
	for f := 0; f < frames; f++ {
		frame := *sc
		frame.Primitives = make([]scene.Primitive, 0, len(sc.Primitives))
		for p := range sc.Primitives {
			if visible[f][p] {
				frame.Primitives = append(frame.Primitives, sc.Primitives[p])
			}
		}
		out = append(out, &frame)
	}
	return out, nil
}
