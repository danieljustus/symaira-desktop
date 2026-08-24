// Package scene defines the positioned scene graph, primitives, and bounding box
// calculation that serves as the single choke point for SVG and raster emitters.
package scene

import (
	"errors"
	"fmt"
	"math"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// MarkerDef defines an SVG-style marker symbol (such as arrowheads).
type MarkerDef struct {
	ID           string    `json:"id"`
	ViewBox      Rect      `json:"view_box"`
	RefX         float64   `json:"ref_x"`
	RefY         float64   `json:"ref_y"`
	MarkerWidth  float64   `json:"marker_width"`
	MarkerHeight float64   `json:"marker_height"`
	Orient       string    `json:"orient"`
	Path         Primitive `json:"path"`
}

// Scene represents a complete positioned diagram ready for vector or raster emission.
type Scene struct {
	Width      float64           `json:"width"`
	Height     float64           `json:"height"`
	ViewBox    Rect              `json:"view_box"`
	Theme      theme.Theme       `json:"theme"`
	Background string            `json:"background,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Markers    []MarkerDef       `json:"markers,omitempty"`
	Primitives []Primitive       `json:"primitives"`
}

// NewScene creates an empty Scene with default theme settings.
func NewScene(width, height float64, th theme.Theme) *Scene {
	if th.Name == "" {
		th = theme.Resolve("symaira-dark")
	}
	return &Scene{
		Width:      width,
		Height:     height,
		ViewBox:    Rect{X: 0, Y: 0, Width: width, Height: height},
		Theme:      th,
		Background: th.Background,
		Metadata:   make(map[string]string),
		Markers:    DefaultMarkers(th),
		Primitives: nil,
	}
}

// Add appends one or more primitives to the scene.
func (s *Scene) Add(prims ...Primitive) {
	for _, p := range prims {
		if p != nil {
			s.Primitives = append(s.Primitives, p)
		}
	}
}

// AddMarker registers a reusable marker symbol.
func (s *Scene) AddMarker(m MarkerDef) {
	s.Markers = append(s.Markers, m)
}

// ComputeBounds calculates the minimal enclosing rectangle covering all primitives.
func (s *Scene) ComputeBounds() Rect {
	if len(s.Primitives) == 0 {
		return Rect{X: 0, Y: 0, Width: s.Width, Height: s.Height}
	}

	var b Rect
	first := true
	for _, p := range s.Primitives {
		pb := p.Bounds()
		if pb.IsEmpty() {
			continue
		}
		if first {
			b = pb
			first = false
		} else {
			b = b.Union(pb)
		}
	}

	if first {
		return Rect{X: 0, Y: 0, Width: s.Width, Height: s.Height}
	}
	return b
}

// FitToBounds adjusts ViewBox, Width, and Height to wrap all primitives tightly with padding.
func (s *Scene) FitToBounds(padding float64) {
	b := s.ComputeBounds()
	padded := b.Pad(padding)
	if padded.Width <= 0 {
		padded.Width = 100
	}
	if padded.Height <= 0 {
		padded.Height = 100
	}

	s.ViewBox = padded
	s.Width = padded.Width
	s.Height = padded.Height
}

// Validate checks internal consistency of the scene graph.
func (s *Scene) Validate() error {
	if math.IsNaN(s.Width) || math.IsInf(s.Width, 0) || s.Width < 0 {
		return fmt.Errorf("invalid scene width: %v", s.Width)
	}
	if math.IsNaN(s.Height) || math.IsInf(s.Height, 0) || s.Height < 0 {
		return fmt.Errorf("invalid scene height: %v", s.Height)
	}
	if s.ViewBox.Width < 0 || s.ViewBox.Height < 0 {
		return errors.New("scene viewBox has negative dimensions")
	}

	for i, p := range s.Primitives {
		if p == nil {
			return fmt.Errorf("scene primitive [%d] is nil", i)
		}
		pb := p.Bounds()
		if math.IsNaN(pb.X) || math.IsNaN(pb.Y) || math.IsNaN(pb.Width) || math.IsNaN(pb.Height) {
			return fmt.Errorf("scene primitive [%d] (%s) has NaN bounds: %+v", i, p.Type(), pb)
		}
	}

	return nil
}

// DefaultMarkers returns standard arrow markers styled for the given theme.
func DefaultMarkers(th theme.Theme) []MarkerDef {
	edgeColor := th.Edge
	if edgeColor == "" {
		edgeColor = "#B8B4A8"
	}
	primaryColor := th.Primary
	if primaryColor == "" {
		primaryColor = "#E5C397"
	}

	return []MarkerDef{
		{
			ID:           "arrow-end",
			ViewBox:      Rect{X: 0, Y: 0, Width: 10, Height: 10},
			RefX:         9,
			RefY:         5,
			MarkerWidth:  8,
			MarkerHeight: 8,
			Orient:       "auto",
			Path: &PolygonElement{
				Points: []measure.Point{
					{X: 0, Y: 1.5},
					{X: 9, Y: 5},
					{X: 0, Y: 8.5},
				},
				Fill: edgeColor,
			},
		},
		{
			ID:           "arrow-start",
			ViewBox:      Rect{X: 0, Y: 0, Width: 10, Height: 10},
			RefX:         1,
			RefY:         5,
			MarkerWidth:  8,
			MarkerHeight: 8,
			Orient:       "auto",
			Path: &PolygonElement{
				Points: []measure.Point{
					{X: 10, Y: 1.5},
					{X: 1, Y: 5},
					{X: 10, Y: 8.5},
				},
				Fill: edgeColor,
			},
		},
		{
			ID:           "dot-end",
			ViewBox:      Rect{X: 0, Y: 0, Width: 10, Height: 10},
			RefX:         5,
			RefY:         5,
			MarkerWidth:  6,
			MarkerHeight: 6,
			Orient:       "auto",
			Path: &CircleElement{
				CX:   5,
				CY:   5,
				R:    4,
				Fill: primaryColor,
			},
		},
	}
}
