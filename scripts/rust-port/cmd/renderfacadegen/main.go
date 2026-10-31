// Command renderfacadegen records the public SymDraw facade (internal/draw
// draw.go) from the production Go engine: draw.Render over representative
// sources and formats, plus the facade's pure entry points.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/danieljustus/symaira-desktop/internal/draw"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

// pngBudget bounds the rasterized pixels of a recorded PNG render.
const pngBudget = 40000

type sourceCase struct{ ID, Mode, Input string }

type request struct {
	Via         string  `json:"via"` // source | ir | scene | scene+source | none | canceled
	Format      string  `json:"format"`
	Theme       string  `json:"theme"`
	Scale       float64 `json:"scale"`
	TextAsPaths bool    `json:"text_as_paths"`
	Padding     float64 `json:"padding"`
}

// fnum is a float64 whose non-finite values marshal as "NaN", "+Inf", "-Inf".
type fnum float64

func (f fnum) MarshalJSON() ([]byte, error) {
	switch {
	case math.IsNaN(float64(f)):
		return []byte(`"NaN"`), nil
	case math.IsInf(float64(f), 1):
		return []byte(`"+Inf"`), nil
	case math.IsInf(float64(f), -1):
		return []byte(`"-Inf"`), nil
	}
	return json.Marshal(float64(f))
}

type result struct {
	Format string `json:"format"`
	Width  fnum   `json:"width"`
	Height fnum   `json:"height"`
	// The IEEE-754 bit patterns are the exact record: JSON readers differ in
	// the last digit of a decimal float.
	WidthBits  string `json:"width_bits"`
	HeightBits string `json:"height_bits"`
	FontHash   string `json:"font_hash"`
	Length     int    `json:"length"`
	SHA256     string `json:"sha256"`
	Error      string `json:"error"`
}

type facadeCase struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"` // render | call
	Ref         string   `json:"ref"`
	Mode        string   `json:"mode"`
	InputSHA256 string   `json:"input_sha256"`
	Request     *request `json:"request,omitempty"`
	Result      *result  `json:"result,omitempty"`
	Op          string   `json:"op,omitempty"`
	Arg         string   `json:"arg,omitempty"`
	Output      string   `json:"output,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type corpus struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         []facadeCase     `json:"cases"`
}

func decode(c sourceCase) (*ir.Diagram, error) {
	if c.Mode == "source" {
		return draw.Parse(c.Input)
	}
	d := &ir.Diagram{}
	return d, json.Unmarshal([]byte(c.Input), d)
}

// doRender runs the facade for one request over a freshly decoded case.
func doRender(c sourceCase, r request) result {
	req := draw.RenderRequest{Format: draw.Format(r.Format), Theme: r.Theme, Scale: r.Scale, TextAsPaths: r.TextAsPaths, Padding: r.Padding}
	ctx := context.Background()
	switch r.Via {
	case "source":
		req.Source = c.Input
	case "ir":
		d, _ := decode(c)
		req.IR = d
	case "scene", "scene+source":
		d, _ := decode(c)
		sc, err := draw.BuildSceneFromIR(d)
		if err != nil {
			panic(err)
		}
		req.Scene = sc
		if r.Via == "scene+source" {
			req.Source = "{ not a diagram"
		}
	case "none":
	case "canceled":
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
		req.Source = c.Input
	}
	res, err := draw.Render(ctx, req)
	if err != nil {
		return result{Error: err.Error()}
	}
	return result{
		Format: string(res.Format), Width: fnum(res.Width), Height: fnum(res.Height),
		WidthBits: fmt.Sprintf("%016x", math.Float64bits(res.Width)), HeightBits: fmt.Sprintf("%016x", math.Float64bits(res.Height)), FontHash: res.FontHash,
		Length: len(res.Data), SHA256: fmt.Sprintf("%x", sha256.Sum256(res.Data)),
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	output := flag.String("output", "testdata/port/render/facade.json", "fixture path")
	check := flag.Bool("check", false, "compare without writing")
	flag.Parse()
	// PNG renders go through the float32 rasterizer whose arithmetic depends
	// on arm64 FMA fusion (see renderpnggen).
	if runtime.GOARCH != "arm64" {
		if *check {
			fmt.Printf("SKIP render facade oracle check: fixture is recorded on arm64, host is %s\n", runtime.GOARCH)
			return nil
		}
		return fmt.Errorf("render facade fixture must be generated on arm64, host is %s", runtime.GOARCH)
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
	themes := append(draw.Themes(), "dark", "light", " Symaira-Dark ", "no-such-theme")
	result0 := corpus{SchemaVersion: 1, Oracle: pin}
	add := func(c sourceCase, r request) {
		id := fmt.Sprintf("%s#%s/%s", c.ID, r.Via, r.Format)
		res := doRender(c, r)
		result0.Cases = append(result0.Cases, facadeCase{
			ID: fmt.Sprintf("%s/%d", id, len(result0.Cases)), Kind: "render", Ref: c.ID, Mode: c.Mode,
			InputSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(c.Input))),
			Request:     &r, Result: &res,
		})
	}
	n := 0
	var first *sourceCase
	for _, c := range src.Cases {
		if c.Mode != "source" && c.Mode != "ir" {
			continue
		}
		d, err := decode(c)
		if err != nil {
			continue
		}
		if _, err := draw.BuildSceneFromIR(d); err != nil {
			// A rejected diagram still exercises the facade's error wrapping
			// for a few cases.
			if n%11 == 0 {
				add(c, request{Via: "source", Format: "svg"})
				if c.Mode == "ir" {
					add(c, request{Via: "ir", Format: "typst"})
				}
			}
			n++
			continue
		}
		n++
		if first == nil {
			cc := c
			first = &cc
		}
		th := themes[n%len(themes)]
		via := "source"
		if c.Mode == "ir" {
			via = "ir"
		}
		// Default format (SVG) through the natural entry point.
		add(c, request{Via: via})
		switch n % 4 {
		case 0:
			add(c, request{Via: via, Format: "typst", Theme: th})
			add(c, request{Via: "scene", Format: "svg", Theme: th, Padding: 12, TextAsPaths: true})
		case 1:
			add(c, request{Via: "scene", Format: "typst", Padding: 7.5})
			add(c, request{Via: via, Format: "svg", TextAsPaths: true, Theme: th})
		case 2:
			add(c, request{Via: via, Format: "svg", Theme: th, Padding: 20})
		case 3:
			add(c, request{Via: "scene", Format: "svg"})
		}
		// PNG only when the rendered raster stays small.
		scale := []float64{0, 1, 1.5, 2, -1}[n%5]
		pad := []float64{0, 0, 10, 0, 4}[n%5]
		probe := doRender(c, request{Via: via, Format: "svg", Theme: th, Padding: pad})
		eff := scale
		if eff <= 0 {
			eff = 1
		}
		w, h := math.Ceil(float64(probe.Width)*eff), math.Ceil(float64(probe.Height)*eff)
		if probe.Error == "" && w > 0 && h > 0 && w <= 4096 && h <= 4096 && w*h <= pngBudget && n%2 == 0 {
			add(c, request{Via: via, Format: "png", Theme: th, Scale: scale, Padding: pad})
		}
		if n%13 == 0 {
			add(c, request{Via: via, Format: "gif"})
			add(c, request{Via: via, Format: "PDF"})
		}
	}
	if first == nil {
		return fmt.Errorf("no renderable case")
	}
	// Request-shape controls over the first renderable case.
	add(*first, request{Via: "scene+source", Format: "typst"})
	add(*first, request{Via: "none", Format: "svg"})
	add(*first, request{Via: "canceled", Format: "svg"})
	add(*first, request{Via: "source", Format: "svg", Theme: "symaira-light", Padding: -5})

	call := func(op, arg, out string, err error) {
		fc := facadeCase{ID: "call/" + op + "/" + fmt.Sprint(len(result0.Cases)), Kind: "call", Op: op, Arg: arg, Output: out}
		if err != nil {
			fc.Error = err.Error()
		}
		result0.Cases = append(result0.Cases, fc)
	}
	kinds, _ := json.Marshal(draw.Kinds())
	call("kinds", "", string(kinds), nil)
	names, _ := json.Marshal(draw.Themes())
	call("themes", "", string(names), nil)
	call("font_hash", "", draw.FontHash(), nil)
	call("dialect_version", "", draw.DialectVersion(), nil)
	call("validate_nil", "", "", draw.Validate(nil))
	for _, in := range []string{
		``, `   `, `{`, `[]`, `{"kind":"graph"}`, `{"kind":"graph","bogus":1}`, `{"kind":"nope"}`,
		`{"kind":"graph","nodes":[{"id":"a"}],"edges":[{"from":"a","to":"missing"}]}`,
		`{"kind":"graph","direction":"UP"}`,
		`{"kind":"chart","chart":{"type":"bar"}}`,
	} {
		call("validate_json", in, "", draw.ValidateJSON([]byte(in)))
		_, err := draw.ParseJSON([]byte(in))
		call("parse_json", in, "", err)
	}
	for _, in := range []string{"", "  \n", "graph TD\nA-->B", "flowchart LR\nA[Start] --> B{Go?}", "not mermaid", `{"kind":"graph"}`, `{"kind":"x"}`} {
		_, err := draw.Parse(in)
		call("parse", in, "", err)
		_, err = draw.ParseMermaid(in)
		call("parse_mermaid", in, "", err)
	}
	for _, name := range append(draw.Themes(), "", "dark", "light", "Unknown") {
		var sc *scene.Scene = draw.NewScene(321.5, 123, name)
		js, err := json.Marshal(sc)
		call("new_scene", name, string(js), err)
	}
	if len(result0.Cases) == 0 {
		return fmt.Errorf("empty facade corpus")
	}
	data, err = json.MarshalIndent(result0, "", "  ")
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
			return fmt.Errorf("render facade fixture is stale")
		}
	} else if err := os.WriteFile(*output, data, 0600); err != nil {
		return err
	}
	fmt.Printf("PASS render facade fixture (%d cases)\n", len(result0.Cases))
	return nil
}
