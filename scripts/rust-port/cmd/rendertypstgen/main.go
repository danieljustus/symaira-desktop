// Command rendertypstgen records Typst output from the production Go draw
// engine: emit.EmitTypst over the SymDraw emit corpus scenes plus hand-built
// scenes that reach every branch of the emitter.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/draw"
	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	parsepkg "github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type sourceCase struct{ ID, Mode, Input string }

// num is a JSON number, or the strings "NaN", "+Inf" and "-Inf".
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		switch s {
		case "NaN":
			*n = num(math.NaN())
		case "+Inf":
			*n = num(math.Inf(1))
		case "-Inf":
			*n = num(math.Inf(-1))
		default:
			return fmt.Errorf("bad number %q", s)
		}
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = num(f)
	return nil
}

// spec primitives (kept in sync with tests/draw_typst_contract.rs).
type primSpec struct {
	Type   string `json:"t"`
	X      num    `json:"x"`
	Y      num    `json:"y"`
	W      num    `json:"w"`
	H      num    `json:"h"`
	Rx     num    `json:"rx"`
	CX     num    `json:"cx"`
	CY     num    `json:"cy"`
	R      num    `json:"r"`
	Fill   string `json:"fill"`
	Stroke string `json:"stroke"`
	SW     num    `json:"sw"`
	Text   string `json:"text"`
	FS     num    `json:"fs"`
}

type sceneSpec struct {
	Width      num        `json:"width"`
	Height     num        `json:"height"`
	Background string     `json:"background"`
	Prims      []primSpec `json:"prims"`
}

type typstCase struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Input  string `json:"input"`
	Error  string `json:"error"`
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
	// Output is kept verbatim for short results so the fixture is readable.
	Output string `json:"output"`
}

type corpus struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         []typstCase      `json:"cases"`
}

func buildSpec(s sceneSpec) *scene.Scene {
	sc := scene.NewScene(float64(s.Width), float64(s.Height), theme.Resolve("symaira-dark"))
	sc.Background = s.Background
	for _, p := range s.Prims {
		switch p.Type {
		case "rect":
			sc.Add(&scene.RectElement{X: float64(p.X), Y: float64(p.Y), Width: float64(p.W), Height: float64(p.H),
				Rx: float64(p.Rx), Fill: p.Fill, Stroke: p.Stroke, StrokeWidth: float64(p.SW)})
		case "circle":
			sc.Add(&scene.CircleElement{CX: float64(p.CX), CY: float64(p.CY), R: float64(p.R),
				Fill: p.Fill, Stroke: p.Stroke, StrokeWidth: float64(p.SW)})
		case "text":
			sc.Add(&scene.TextElement{X: float64(p.X), Y: float64(p.Y), Text: p.Text, FontSize: float64(p.FS), Fill: p.Fill})
		case "line":
			sc.Add(&scene.LineElement{X1: float64(p.X), Y1: float64(p.Y), X2: float64(p.W), Y2: float64(p.H), Stroke: p.Stroke, StrokeWidth: float64(p.SW)})
		case "ellipse":
			sc.Add(&scene.EllipseElement{CX: float64(p.CX), CY: float64(p.CY), RX: float64(p.Rx), RY: float64(p.R), Fill: p.Fill})
		case "group":
			sc.Add(&scene.GroupElement{ID: "g", Children: []scene.Primitive{
				&scene.RectElement{X: 1, Y: 2, Width: 3, Height: 4, Fill: "#ffffff"}}})
		default:
			panic("unknown primitive " + p.Type)
		}
	}
	return sc
}

func rec(item *typstCase, sc *scene.Scene) {
	out, err := emit.EmitTypst(sc)
	if err != nil {
		item.Error = err.Error()
		return
	}
	item.Length = len(out)
	item.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(out)))
	if len(out) <= 1500 {
		item.Output = out
	}
}

var synthetic = []struct {
	id   string
	spec string
}{
	{"rect-fill-stroke", `{"width":120,"height":80,"background":"#101010","prims":[{"t":"rect","x":10,"y":5.5,"w":100.125,"h":60,"rx":4,"fill":"#ff0000","stroke":"#00ff00","sw":2}]}`},
	{"rect-none-fill-stroke", `{"width":50,"height":50,"background":"none","prims":[{"t":"rect","x":1,"y":2,"w":3,"h":4,"fill":"none","stroke":"none","sw":3}]}`},
	{"rect-empty-fill-zero-stroke", `{"width":50,"height":50,"background":"transparent","prims":[{"t":"rect","x":1,"y":2,"w":3,"h":4,"fill":"","stroke":"#123456","sw":0}]}`},
	{"rect-negative-stroke", `{"width":50,"height":50,"background":"","prims":[{"t":"rect","x":1,"y":2,"w":3,"h":4,"fill":"#abcdef","stroke":"#123456","sw":-1}]}`},
	{"circle-fill-stroke", `{"width":90,"height":90,"background":"#ffffff","prims":[{"t":"circle","cx":45,"cy":45.005,"r":30,"fill":"rgb(1,2,3)","stroke":"blue","sw":1.5}]}`},
	{"circle-none", `{"width":90,"height":90,"background":"x","prims":[{"t":"circle","cx":10,"cy":10,"r":5,"fill":"none","stroke":"","sw":1}]}`},
	{"text-defaults", `{"width":200,"height":40,"background":"","prims":[{"t":"text","x":10,"y":30,"text":"Hello","fs":0,"fill":""}]}`},
	{"text-negative-size", `{"width":200,"height":40,"background":"","prims":[{"t":"text","x":10,"y":30,"text":"neg","fs":-3,"fill":"#000000"}]}`},
	{"text-escapes", `{"width":200,"height":40,"background":"","prims":[{"t":"text","x":10,"y":30,"text":"a\\b [c] $d #e *_ \"q\" ä€ \n","fs":12,"fill":"#222222"}]}`},
	{"text-rounding", `{"width":200.005,"height":40.995,"background":"","prims":[{"t":"text","x":0.125,"y":0.375,"text":"r","fs":0.0049,"fill":"red"}]}`},
	{"ignored-primitives", `{"width":10,"height":10,"background":"","prims":[{"t":"line","x":0,"y":0,"w":5,"h":5,"stroke":"#fff","sw":1},{"t":"ellipse","cx":1,"cy":1,"rx":2,"r":3,"fill":"#fff"},{"t":"group"},{"t":"rect","x":0,"y":0,"w":1,"h":1,"fill":"#000"}]}`},
	{"empty", `{"width":0,"height":0,"background":"","prims":[]}`},
	{"non-finite", `{"width":"+Inf","height":"NaN","background":"#000","prims":[{"t":"rect","x":"-Inf","y":"NaN","w":"+Inf","h":1,"rx":0,"fill":"#111","stroke":"#222","sw":"+Inf"},{"t":"circle","cx":"NaN","cy":1,"r":"+Inf","fill":"#333","stroke":"#444","sw":"NaN"},{"t":"text","x":"NaN","y":"+Inf","text":"n","fs":"NaN","fill":""}]}`},
	{"large-and-negative-zero", `{"width":1e21,"height":-0.0,"background":"","prims":[{"t":"rect","x":-0.004,"y":123456789.987654321,"w":1e-7,"h":2.675,"rx":1.005,"fill":"#111","stroke":"#222","sw":0.001}]}`},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	output := flag.String("output", "testdata/port/render/typst.json", "fixture path")
	check := flag.Bool("check", false, "compare without writing")
	flag.Parse()
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
		if e != nil {
			continue
		}
		item := typstCase{ID: c.ID, Mode: c.Mode, Input: c.Input}
		if c.Mode == "source" || c.Mode == "ir" {
			// The emit corpus input is large; keep a digest, not a copy.
			item.Input = fmt.Sprintf("%x", sha256.Sum256([]byte(c.Input)))
		}
		rec(&item, sc)
		result.Cases = append(result.Cases, item)
	}
	for _, s := range synthetic {
		var spec sceneSpec
		if err := json.Unmarshal([]byte(s.spec), &spec); err != nil {
			return fmt.Errorf("%s: %w", s.id, err)
		}
		item := typstCase{ID: "synthetic-" + s.id, Mode: "spec", Input: s.spec}
		rec(&item, buildSpec(spec))
		result.Cases = append(result.Cases, item)
	}
	nilItem := typstCase{ID: "synthetic-nil-scene", Mode: "nil"}
	rec(&nilItem, nil)
	result.Cases = append(result.Cases, nilItem)

	if len(result.Cases) == 0 {
		return fmt.Errorf("empty Typst corpus")
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
			return fmt.Errorf("render Typst fixture is stale")
		}
	} else if err := os.WriteFile(*output, data, 0600); err != nil {
		return err
	}
	fmt.Printf("PASS render Typst fixture (%d cases)\n", len(result.Cases))
	return nil
}
