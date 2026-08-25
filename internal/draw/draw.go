// Package draw is the top-level facade for the SymDraw deterministic diagram
// rendering pipeline, providing a clean entry point for rendering IR models or
// positioned scenes into vector SVG and raster PNG graphics.
package draw

import (
	"context"
	"fmt"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/fonts"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// ParseError is the typed error produced when parsing or validating diagram input.
type ParseError = parse.ParseError

// Format specifies the output serialization target.
type Format string

const (
	FormatSVG   Format = "svg"
	FormatPNG   Format = "png"
	FormatGIF   Format = "gif"
	FormatTypst Format = "typst"
)

// RenderRequest specifies a diagram rendering task.
type RenderRequest struct {
	Source      string       `json:"source,omitempty"` // Mermaid source or JSON IR
	IR          *ir.Diagram  `json:"ir,omitempty"`
	Scene       *scene.Scene `json:"scene,omitempty"`
	Format      Format       `json:"format,omitempty"`
	Theme       string       `json:"theme,omitempty"`
	Scale       float64      `json:"scale,omitempty"`
	TextAsPaths bool         `json:"text_as_paths,omitempty"`
	Padding     float64      `json:"padding,omitempty"`
}

// RenderResult contains the generated graphic and metadata.
type RenderResult struct {
	Data       []byte  `json:"-"`
	Format     Format  `json:"format"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	FontHash   string  `json:"font_hash"`
	DurationMS int64   `json:"duration_ms"`
}

// Render executes a diagram render request deterministically.
func Render(ctx context.Context, req RenderRequest) (*RenderResult, error) {
	start := time.Now()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	sc := req.Scene
	if sc == nil {
		diag := req.IR
		if diag == nil && req.Source != "" {
			var err error
			diag, err = Parse(req.Source)
			if err != nil {
				return nil, fmt.Errorf("parse diagram source: %w", err)
			}
		}
		if diag == nil {
			return nil, fmt.Errorf("either Source, IR or Scene must be provided in RenderRequest")
		}
		built, err := BuildSceneFromIR(diag)
		if err != nil {
			return nil, fmt.Errorf("build scene from ir: %w", err)
		}
		sc = built
	}

	if req.Theme != "" {
		th := theme.Resolve(req.Theme)
		sc.Theme = th
		if sc.Background == "" || sc.Background == "none" {
			sc.Background = th.Background
		}
	}

	if req.Padding > 0 {
		sc.FitToBounds(req.Padding)
	}

	format := req.Format
	if format == "" {
		format = FormatSVG
	}

	var out []byte
	var err error

	switch format {
	case FormatSVG:
		out, err = emit.EmitSVG(sc, emit.SVGEmitOptions{
			TextAsPaths: req.TextAsPaths,
			FontHash:    fonts.VersionKey(),
			Generator:   "symdraw",
		})
	case FormatPNG:
		scale := req.Scale
		if scale <= 0 {
			scale = 1.0
		}
		out, err = emit.EmitPNG(sc, emit.RasterOptions{
			Scale: scale,
		})
	case FormatTypst:
		typstStr, typstErr := emit.EmitTypst(sc)
		if typstErr != nil {
			err = typstErr
		} else {
			out = []byte(typstStr)
		}
	default:
		return nil, fmt.Errorf("unsupported render format %q", format)
	}

	if err != nil {
		return nil, fmt.Errorf("emit %s: %w", format, err)
	}

	return &RenderResult{
		Data:       out,
		Format:     format,
		Width:      sc.Width,
		Height:     sc.Height,
		FontHash:   fonts.VersionKey(),
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// RenderScene is a convenience function to render a scene directly to format.
func RenderScene(sc *scene.Scene, format Format) ([]byte, error) {
	res, err := Render(context.Background(), RenderRequest{
		Scene:  sc,
		Format: format,
	})
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// Parse auto-detects whether the source is Mermaid or JSON IR and parses it into an ir.Diagram.
func Parse(source string) (*ir.Diagram, error) {
	return parse.Source(source)
}

// ParseMermaid parses Mermaid flowchart/graph source into an ir.Diagram.
func ParseMermaid(source string) (*ir.Diagram, error) {
	return parse.ParseMermaid(source)
}

// ParseJSON parses a JSON diagram byte slice into an ir.Diagram with strict schema validation.
func ParseJSON(data []byte) (*ir.Diagram, error) {
	return parse.ParseJSON(data)
}

// DialectVersion returns the version of the supported Mermaid dialect subset.
func DialectVersion() string {
	return parse.DialectVersion
}

// Validate checks an IR Diagram against schema and structural rules.
func Validate(d *ir.Diagram) error {
	return ir.Validate(d)
}

// ValidateJSON validates a raw JSON byte slice against the IR schema with strict validation.
func ValidateJSON(data []byte) error {
	_, err := parse.ParseJSON(data)
	return err
}

// Kinds returns all supported diagram kinds.
func Kinds() []ir.DiagramKind {
	return []ir.DiagramKind{
		ir.KindGraph,
		ir.KindSequence,
		ir.KindTimeline,
		ir.KindTree,
		ir.KindChart,
		ir.KindCustom,
	}
}

// Themes returns the list of registered theme names.
func Themes() []string {
	return theme.Names()
}

// FontHash returns the deterministic SHA-256 hash of the embedded brand fonts.
func FontHash() string {
	return fonts.VersionKey()
}

// NewScene initializes a new Scene with the named theme.
func NewScene(width, height float64, themeName string) *scene.Scene {
	return scene.NewScene(width, height, theme.Resolve(themeName))
}

// DefaultMeasurer returns the global default Measurer instance.
func DefaultMeasurer() *measure.Measurer {
	return measure.Default()
}
