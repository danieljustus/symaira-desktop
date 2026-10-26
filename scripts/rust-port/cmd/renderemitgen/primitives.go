package main

import (
	"encoding/json"
	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// Direct scenes reach serialization fields not set by the diagram builder.
func primitiveCases() ([]testCase, error) {
	var out []testCase
	for _, background := range []string{"", "none", "transparent", "<&\"'\n"} {
		sc := scene.NewScene(160, 90, theme.Resolve("symaira-light"))
		sc.Background = background
		sc.Metadata = map[string]string{"z": "<&\"'\t\r\n\x00", "a\"": "日本語", "generator": "ignored", "font-hash": "custom"}
		sc.Add(&scene.GroupElement{ID: "group", Class: "c", Transform: "translate(1,2)", Link: "https://example.invalid/?a=1&b=2", Children: []scene.Primitive{
			&scene.EllipseElement{ID: "ellipse", CX: 1.25, CY: 2, RX: 3, RY: 4, Stroke: "red", StrokeWidth: 0.5, Opacity: 0.25, Link: "<link>"},
			&scene.PathElement{ID: "path", Segments: []measure.PathSegment{{Op: measure.OpMoveTo, Args: []measure.Point{{X: 1, Y: 2}}}, {Op: measure.OpLineTo, Args: []measure.Point{{X: 3, Y: 4}}}}, Fill: "none", Stroke: "blue", MarkerEnd: "arrow-end", DashArray: "2 3", Opacity: 0.5},
			&scene.TextElement{ID: "text", Class: "bold", Text: "<&\"'\n", FontWeight: measure.WeightBold, Anchor: scene.AnchorEnd, Baseline: scene.BaselineTop, TextLength: 42, Opacity: 0.75},
			&scene.LineElement{X1: 1, Y1: 2, X2: 3, Y2: 4, MarkerStart: "arrow-start", MarkerEnd: "arrow-end"},
		}})
		input, e := json.Marshal(sc)
		if e != nil {
			return nil, e
		}
		svg, e := emit.EmitSVG(sc, emit.SVGEmitOptions{FontHash: "override<&", Generator: "generator\""})
		if e != nil {
			return nil, e
		}
		out = append(out, testCase{ID: "primitives-" + background, Mode: "primitives", Input: string(input), Expected: string(svg)})
	}
	return out, nil
}
