// Command rendergifgen records animated GIF output from the production Go
// draw engine: draw.AnimateFrames + emit.EmitGIF (Floyd-Steinberg onto the
// Plan 9 palette, image/gif EncodeAll).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"image/gif"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/danieljustus/symaira-desktop/internal/draw"
	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	parsepkg "github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

// rasterBudget bounds the rasterized pixel count of a recorded frame: the
// dither visits every pixel against 256 palette entries.
const rasterBudget = 30000

type sourceCase struct{ ID, Mode, Input string }

type gifCase struct {
	ID              string  `json:"id"`
	Ref             string  `json:"ref"`
	Variant         string  `json:"variant"`
	Mode            string  `json:"mode"`
	InputSHA256     string  `json:"input_sha256"`
	DelayCS         int     `json:"delay_cs"`
	Scale           float64 `json:"scale"`
	AnimFrames      int     `json:"anim_frames"`
	AnimSteps       [][]int `json:"anim_steps"`
	FramePrimitives []int   `json:"frame_primitives"`
	Frames          int     `json:"frames"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	SHA256          string  `json:"sha256"`
	Error           string  `json:"error"`
	Ref2            string  `json:"ref2,omitempty"`
}

type corpus struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         []gifCase        `json:"cases"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rasterSize(sc *scene.Scene, scale float64) (int, int) {
	if scale <= 0 {
		scale = 1
	}
	w, h := int(math.Ceil(sc.Width*scale)), int(math.Ceil(sc.Height*scale))
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 100
	}
	return w, h
}

func inBudget(sc *scene.Scene, scale float64) bool {
	if math.IsNaN(sc.Width) || math.IsNaN(sc.Height) || sc.Width > 4096 || sc.Height > 4096 {
		return false
	}
	w, h := rasterSize(sc, scale)
	return w*h <= rasterBudget
}

func record(item *gifCase, scenes []*scene.Scene) error {
	opts := emit.RasterOptions{Scale: item.Scale}
	b, err := emit.EmitGIF(scenes, item.DelayCS, opts)
	if err != nil {
		item.Error = err.Error()
		return nil
	}
	all, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%s: recorded GIF does not decode: %w", item.ID, err)
	}
	if len(all.Image) != len(scenes) {
		return fmt.Errorf("%s: decoded %d frames, want %d", item.ID, len(all.Image), len(scenes))
	}
	item.Frames = len(all.Image)
	item.Width, item.Height = all.Config.Width, all.Config.Height
	item.SHA256 = fmt.Sprintf("%x", sha256.Sum256(b))
	return nil
}

func run() error {
	output := flag.String("output", "testdata/port/render/gif.json", "fixture path")
	check := flag.Bool("check", false, "compare without writing")
	flag.Parse()
	// The frames come from the same float32 rasterizer as the PNG corpus,
	// whose arithmetic depends on arm64 FMA fusion (see renderpnggen).
	if runtime.GOARCH != "arm64" {
		if *check {
			fmt.Printf("SKIP render GIF oracle check: fixture is recorded on arm64, host is %s\n", runtime.GOARCH)
			return nil
		}
		return fmt.Errorf("render GIF fixture must be generated on arm64, host is %s", runtime.GOARCH)
	}
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		return err
	}
	oracle := fixtureoracle.Defaults()
	pin := inventory.Oracle{Commit: oracle.Commit, Release: oracle.Release}
	if err := fixtureoracle.ValidateSource(root, pin); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, "testdata/port/render/emit.json"))
	if err != nil {
		return err
	}
	var src struct {
		Cases []sourceCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &src); err != nil {
		return err
	}
	result := corpus{SchemaVersion: 1, Oracle: pin}
	var smallRefs []string
	n := 0
	for _, c := range src.Cases {
		if c.Mode != "source" && c.Mode != "ir" {
			continue
		}
		var d *ir.Diagram
		if c.Mode == "source" {
			d, err = parsepkg.Source(c.Input)
		} else {
			d = &ir.Diagram{}
			err = json.Unmarshal([]byte(c.Input), d)
		}
		if err != nil {
			continue
		}
		sc, e := draw.BuildSceneFromIR(d)
		if e != nil || !inBudget(sc, 1) {
			continue
		}
		base := gifCase{
			Ref:         c.ID,
			Mode:        c.Mode,
			InputSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(c.Input))),
		}
		add := func(variant string, mutate func(*gifCase), scenes func() ([]*scene.Scene, error)) error {
			item := base
			item.ID = c.ID + "#" + variant
			item.Variant = variant
			mutate(&item)
			frames, err := scenes()
			if err != nil {
				item.Error = err.Error()
				result.Cases = append(result.Cases, item)
				return nil
			}
			for _, f := range frames {
				item.FramePrimitives = append(item.FramePrimitives, len(f.Primitives))
			}
			if err := record(&item, frames); err != nil {
				return err
			}
			result.Cases = append(result.Cases, item)
			return nil
		}
		// Every case: one frame, default delay.
		if err := add("single", func(i *gifCase) {}, func() ([]*scene.Scene, error) {
			return []*scene.Scene{sc}, nil
		}); err != nil {
			return err
		}
		n++
		if len(sc.Primitives) == 0 {
			// AnimateFrames rejects empty scenes; keep that error recorded.
			if err := add("anim3", func(i *gifCase) { i.AnimFrames, i.DelayCS = 3, 7 }, func() ([]*scene.Scene, error) {
				return draw.AnimateFrames(sc, draw.AnimateOptions{Frames: 3})
			}); err != nil {
				return err
			}
			continue
		}
		if n%3 == 0 {
			if err := add("anim3", func(i *gifCase) { i.AnimFrames, i.DelayCS = 3, 7 }, func() ([]*scene.Scene, error) {
				return draw.AnimateFrames(sc, draw.AnimateOptions{Frames: 3})
			}); err != nil {
				return err
			}
		}
		if n%5 == 1 && len(sc.Primitives) <= 5 {
			scale := 0.0
			if inBudget(sc, 1.5) {
				scale = 1.5
			}
			if err := add("anim-default", func(i *gifCase) { i.DelayCS, i.Scale = 12, scale }, func() ([]*scene.Scene, error) {
				return draw.AnimateFrames(sc, draw.AnimateOptions{})
			}); err != nil {
				return err
			}
		}
		if n%7 == 2 {
			steps := [][]int{{0}, {1, 2}, {7, -1, 3}}
			if err := add("anim-steps", func(i *gifCase) { i.AnimFrames, i.AnimSteps, i.DelayCS = 4, steps, 300 }, func() ([]*scene.Scene, error) {
				return draw.AnimateFrames(sc, draw.AnimateOptions{Frames: 4, Steps: steps})
			}); err != nil {
				return err
			}
		}
		if len(smallRefs) < 2 && len(sc.Primitives) > 0 {
			smallRefs = append(smallRefs, c.ID)
		}
	}
	if len(smallRefs) < 2 {
		return fmt.Errorf("need two source cases for the frame-size controls")
	}
	// Frame-size controls over two rendered scenes a (first) and b.
	scenes := map[string]*scene.Scene{}
	for _, ref := range smallRefs {
		for _, c := range src.Cases {
			if c.ID != ref {
				continue
			}
			var d *ir.Diagram
			if c.Mode == "source" {
				d, err = parsepkg.Source(c.Input)
			} else {
				d = &ir.Diagram{}
				err = json.Unmarshal([]byte(c.Input), d)
			}
			if err != nil {
				return err
			}
			sc, e := draw.BuildSceneFromIR(d)
			if e != nil {
				return e
			}
			scenes[ref] = sc
		}
	}
	a, b := scenes[smallRefs[0]], scenes[smallRefs[1]]
	synthetic := func(id, variant string, frames []*scene.Scene, delay int) error {
		item := gifCase{ID: id, Ref: smallRefs[0], Ref2: smallRefs[1], Variant: variant, Mode: "synthetic", DelayCS: delay}
		for _, f := range frames {
			if f == nil {
				item.FramePrimitives = append(item.FramePrimitives, -1)
			} else {
				item.FramePrimitives = append(item.FramePrimitives, len(f.Primitives))
			}
		}
		if err := record(&item, frames); err != nil {
			return err
		}
		result.Cases = append(result.Cases, item)
		return nil
	}
	if err := synthetic("synthetic#no-scenes", "no-scenes", nil, 0); err != nil {
		return err
	}
	if err := synthetic("synthetic#nil-frame", "nil-frame", []*scene.Scene{a, nil}, 5); err != nil {
		return err
	}
	// A frame larger than the first (a, then a stretched copy) overflows the
	// logical screen; a smaller one fits inside it.
	big := *a
	big.Width, big.Height = a.Width+50, a.Height+50
	if err := synthetic("synthetic#frame-grows", "frame-grows", []*scene.Scene{a, &big}, 5); err != nil {
		return err
	}
	if err := synthetic("synthetic#frame-shrinks", "frame-shrinks", []*scene.Scene{&big, a}, 5); err != nil {
		return err
	}
	if err := synthetic("synthetic#two-scenes", "two-scenes", []*scene.Scene{a, b}, -3); err != nil {
		return err
	}
	// AnimateFrames input rejections.
	for _, rej := range []struct {
		variant string
		sc      *scene.Scene
	}{{"animate-nil", nil}, {"animate-empty", &scene.Scene{Width: 10, Height: 10}}} {
		_, aerr := draw.AnimateFrames(rej.sc, draw.AnimateOptions{})
		if aerr == nil {
			return fmt.Errorf("%s: AnimateFrames accepted the input", rej.variant)
		}
		result.Cases = append(result.Cases, gifCase{ID: "synthetic#" + rej.variant, Variant: rej.variant, Mode: "synthetic", Error: aerr.Error()})
	}
	if len(result.Cases) == 0 {
		return fmt.Errorf("empty GIF corpus")
	}
	data, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *check {
		old, err := os.ReadFile(*output)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("render GIF fixture is stale")
		}
	} else if err := os.WriteFile(*output, data, 0600); err != nil {
		return err
	}
	fmt.Printf("PASS render GIF fixture (%d cases)\n", len(result.Cases))
	return nil
}
