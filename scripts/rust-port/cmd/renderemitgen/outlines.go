package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/fonts"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

func outlineCases() ([]testCase, error) {
	var cases []testCase
	for fi, data := range [][]byte{fonts.Regular(), fonts.Bold()} {
		f, err := sfnt.Parse(data)
		if err != nil {
			return nil, err
		}
		for g := 0; g < f.NumGlyphs(); g++ {
			segs, err := f.LoadGlyph(nil, sfnt.GlyphIndex(g), fixed.Int26_6(848), nil)
			if err != nil {
				return nil, err
			}
			// Exact fixed coordinates, not rounded SVG: each operation and every integer.
			canonical := ""
			for _, s := range segs {
				canonical += fmt.Sprintf("%d", s.Op)
				n := 1
				if s.Op == sfnt.SegmentOpQuadTo {
					n = 2
				}
				if s.Op == sfnt.SegmentOpCubeTo {
					n = 3
				}
				for _, p := range s.Args[:n] {
					canonical += fmt.Sprintf(" %d %d", p.X, p.Y)
				}
				canonical += "\n"
			}
			input, _ := json.Marshal([]int{fi, g, 848})
			cases = append(cases, testCase{ID: fmt.Sprintf("glyph-%d-%d", fi, g), Mode: "glyph", Input: string(input), Expected: fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))})
		}
	}
	for _, weight := range []measure.FontWeight{measure.WeightRegular, measure.WeightBold} {
		for _, anchor := range []scene.TextAnchor{scene.AnchorStart, scene.AnchorMiddle, scene.AnchorEnd} {
			for _, baseline := range []scene.TextBaseline{scene.BaselineAlphabetic, scene.BaselineMiddle, scene.BaselineCentral, scene.BaselineTop, scene.BaselineHanging} {
				for _, value := range []string{"Hello Äéç ΩЖ ﬁ <&>", "日本語😀\n\t", " ", ""} {
					e := &scene.TextElement{ID: "outline", Class: "test", X: 80.125, Y: 40.25, Text: value, FontSize: 13.25, FontWeight: weight, Anchor: anchor, Baseline: baseline, TextLength: 73.5, Opacity: 0.5, Link: "https://example.invalid/?a=1&b=2"}
					sc := scene.NewScene(160, 90, theme.Resolve("symaira-light"))
					sc.Add(&scene.GroupElement{Transform: "translate(1,2)", Children: []scene.Primitive{e}})
					b, err := emit.EmitSVG(sc, emit.SVGEmitOptions{TextAsPaths: true})
					if err != nil {
						return nil, err
					}
					input, _ := json.Marshal(e)
					cases = append(cases, testCase{ID: fmt.Sprintf("outline-%d", len(cases)), Mode: "outline", Input: string(input), Expected: string(b)})
				}
			}
		}
	}
	return cases, nil
}
