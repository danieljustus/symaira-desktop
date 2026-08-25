package scene

import (
	"math"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
)

// Primitive is the base interface implemented by all positioned scene elements.
type Primitive interface {
	Type() string
	Bounds() Rect
	ElementID() string
}

// TextAnchor specifies horizontal text alignment relative to (X, Y).
type TextAnchor string

const (
	AnchorStart  TextAnchor = "start"
	AnchorMiddle TextAnchor = "middle"
	AnchorEnd    TextAnchor = "end"
)

// TextBaseline specifies vertical text alignment relative to (X, Y).
type TextBaseline string

const (
	BaselineAlphabetic TextBaseline = "alphabetic"
	BaselineMiddle     TextBaseline = "middle"
	BaselineCentral    TextBaseline = "central"
	BaselineHanging    TextBaseline = "hanging"
	BaselineTop        TextBaseline = "top"
)

// Rect represents a rectangle with optional rounded corners.
type RectElement struct {
	ID          string  `json:"id,omitempty"`
	Class       string  `json:"class,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Rx          float64 `json:"rx,omitempty"`
	Ry          float64 `json:"ry,omitempty"`
	Fill        string  `json:"fill,omitempty"`
	Stroke      string  `json:"stroke,omitempty"`
	StrokeWidth float64 `json:"stroke_width,omitempty"`
	DashArray   string  `json:"dash_array,omitempty"`
	Opacity     float64 `json:"opacity,omitempty"`
	Link        string  `json:"link,omitempty"`
}

func (r *RectElement) Type() string      { return "rect" }
func (r *RectElement) ElementID() string { return r.ID }
func (r *RectElement) Bounds() Rect {
	sw := r.StrokeWidth / 2.0
	return Rect{
		X:      r.X - sw,
		Y:      r.Y - sw,
		Width:  r.Width + r.StrokeWidth,
		Height: r.Height + r.StrokeWidth,
	}
}

// Circle represents a circle centered at (CX, CY) with radius R.
type CircleElement struct {
	ID          string  `json:"id,omitempty"`
	Class       string  `json:"class,omitempty"`
	CX          float64 `json:"cx"`
	CY          float64 `json:"cy"`
	R           float64 `json:"r"`
	Fill        string  `json:"fill,omitempty"`
	Stroke      string  `json:"stroke,omitempty"`
	StrokeWidth float64 `json:"stroke_width,omitempty"`
	Opacity     float64 `json:"opacity,omitempty"`
	Link        string  `json:"link,omitempty"`
}

func (c *CircleElement) Type() string      { return "circle" }
func (c *CircleElement) ElementID() string { return c.ID }
func (c *CircleElement) Bounds() Rect {
	sw := c.StrokeWidth / 2.0
	r := c.R + sw
	return Rect{
		X:      c.CX - r,
		Y:      c.CY - r,
		Width:  r * 2,
		Height: r * 2,
	}
}

// Ellipse represents an ellipse centered at (CX, CY) with radii (RX, RY).
type EllipseElement struct {
	ID          string  `json:"id,omitempty"`
	Class       string  `json:"class,omitempty"`
	CX          float64 `json:"cx"`
	CY          float64 `json:"cy"`
	RX          float64 `json:"rx"`
	RY          float64 `json:"ry"`
	Fill        string  `json:"fill,omitempty"`
	Stroke      string  `json:"stroke,omitempty"`
	StrokeWidth float64 `json:"stroke_width,omitempty"`
	Opacity     float64 `json:"opacity,omitempty"`
	Link        string  `json:"link,omitempty"`
}

func (e *EllipseElement) Type() string      { return "ellipse" }
func (e *EllipseElement) ElementID() string { return e.ID }
func (e *EllipseElement) Bounds() Rect {
	sw := e.StrokeWidth / 2.0
	rx := e.RX + sw
	ry := e.RY + sw
	return Rect{
		X:      e.CX - rx,
		Y:      e.CY - ry,
		Width:  rx * 2,
		Height: ry * 2,
	}
}

// Line represents a 2D line segment between (X1, Y1) and (X2, Y2).
type LineElement struct {
	ID          string  `json:"id,omitempty"`
	Class       string  `json:"class,omitempty"`
	X1          float64 `json:"x1"`
	Y1          float64 `json:"y1"`
	X2          float64 `json:"x2"`
	Y2          float64 `json:"y2"`
	Stroke      string  `json:"stroke,omitempty"`
	StrokeWidth float64 `json:"stroke_width,omitempty"`
	DashArray   string  `json:"dash_array,omitempty"`
	MarkerStart string  `json:"marker_start,omitempty"`
	MarkerEnd   string  `json:"marker_end,omitempty"`
	Opacity     float64 `json:"opacity,omitempty"`
}

func (l *LineElement) Type() string      { return "line" }
func (l *LineElement) ElementID() string { return l.ID }
func (l *LineElement) Bounds() Rect {
	minX := math.Min(l.X1, l.X2)
	minY := math.Min(l.Y1, l.Y2)
	maxX := math.Max(l.X1, l.X2)
	maxY := math.Max(l.Y1, l.Y2)
	sw := l.StrokeWidth / 2.0
	return Rect{
		X:      minX - sw,
		Y:      minY - sw,
		Width:  (maxX - minX) + l.StrokeWidth,
		Height: (maxY - minY) + l.StrokeWidth,
	}
}

// Polyline represents connected straight line segments.
type PolylineElement struct {
	ID          string          `json:"id,omitempty"`
	Class       string          `json:"class,omitempty"`
	Points      []measure.Point `json:"points"`
	Fill        string          `json:"fill,omitempty"`
	Stroke      string          `json:"stroke,omitempty"`
	StrokeWidth float64         `json:"stroke_width,omitempty"`
	DashArray   string          `json:"dash_array,omitempty"`
	MarkerEnd   string          `json:"marker_end,omitempty"`
	Opacity     float64         `json:"opacity,omitempty"`
}

func (p *PolylineElement) Type() string      { return "polyline" }
func (p *PolylineElement) ElementID() string { return p.ID }
func (p *PolylineElement) Bounds() Rect {
	if len(p.Points) == 0 {
		return Rect{}
	}
	minX, minY := p.Points[0].X, p.Points[0].Y
	maxX, maxY := minX, minY
	for _, pt := range p.Points[1:] {
		if pt.X < minX {
			minX = pt.X
		}
		if pt.X > maxX {
			maxX = pt.X
		}
		if pt.Y < minY {
			minY = pt.Y
		}
		if pt.Y > maxY {
			maxY = pt.Y
		}
	}
	sw := p.StrokeWidth / 2.0
	return Rect{
		X:      minX - sw,
		Y:      minY - sw,
		Width:  (maxX - minX) + p.StrokeWidth,
		Height: (maxY - minY) + p.StrokeWidth,
	}
}

// Polygon represents a closed multi-point polygon.
type PolygonElement struct {
	ID          string          `json:"id,omitempty"`
	Class       string          `json:"class,omitempty"`
	Points      []measure.Point `json:"points"`
	Fill        string          `json:"fill,omitempty"`
	Stroke      string          `json:"stroke,omitempty"`
	StrokeWidth float64         `json:"stroke_width,omitempty"`
	DashArray   string          `json:"dash_array,omitempty"`
	Opacity     float64         `json:"opacity,omitempty"`
}

func (p *PolygonElement) Type() string      { return "polygon" }
func (p *PolygonElement) ElementID() string { return p.ID }
func (p *PolygonElement) Bounds() Rect {
	if len(p.Points) == 0 {
		return Rect{}
	}
	minX, minY := p.Points[0].X, p.Points[0].Y
	maxX, maxY := minX, minY
	for _, pt := range p.Points[1:] {
		if pt.X < minX {
			minX = pt.X
		}
		if pt.X > maxX {
			maxX = pt.X
		}
		if pt.Y < minY {
			minY = pt.Y
		}
		if pt.Y > maxY {
			maxY = pt.Y
		}
	}
	sw := p.StrokeWidth / 2.0
	return Rect{
		X:      minX - sw,
		Y:      minY - sw,
		Width:  (maxX - minX) + p.StrokeWidth,
		Height: (maxY - minY) + p.StrokeWidth,
	}
}

// Path represents arbitrary bezier/vector paths.
type PathElement struct {
	ID          string                `json:"id,omitempty"`
	Class       string                `json:"class,omitempty"`
	D           string                `json:"d,omitempty"` // SVG path data if pre-formatted
	Segments    []measure.PathSegment `json:"segments,omitempty"`
	Fill        string                `json:"fill,omitempty"`
	Stroke      string                `json:"stroke,omitempty"`
	StrokeWidth float64               `json:"stroke_width,omitempty"`
	DashArray   string                `json:"dash_array,omitempty"`
	MarkerEnd   string                `json:"marker_end,omitempty"`
	Opacity     float64               `json:"opacity,omitempty"`
	Link        string                `json:"link,omitempty"`
	boundsHint  *Rect
}

func (p *PathElement) Type() string      { return "path" }
func (p *PathElement) ElementID() string { return p.ID }
func (p *PathElement) SetBoundsHint(r Rect) {
	p.boundsHint = &r
}
func (p *PathElement) Bounds() Rect {
	if p.boundsHint != nil {
		return *p.boundsHint
	}
	if len(p.Segments) == 0 {
		return Rect{}
	}
	var minX, minY, maxX, maxY float64
	first := true
	for _, seg := range p.Segments {
		for _, pt := range seg.Args {
			if first {
				minX, maxX = pt.X, pt.X
				minY, maxY = pt.Y, pt.Y
				first = false
			} else {
				if pt.X < minX {
					minX = pt.X
				}
				if pt.X > maxX {
					maxX = pt.X
				}
				if pt.Y < minY {
					minY = pt.Y
				}
				if pt.Y > maxY {
					maxY = pt.Y
				}
			}
		}
	}
	sw := p.StrokeWidth / 2.0
	return Rect{
		X:      minX - sw,
		Y:      minY - sw,
		Width:  (maxX - minX) + p.StrokeWidth,
		Height: (maxY - minY) + p.StrokeWidth,
	}
}

// Text represents single-line rendered text.
type TextElement struct {
	ID         string             `json:"id,omitempty"`
	Class      string             `json:"class,omitempty"`
	X          float64            `json:"x"`
	Y          float64            `json:"y"`
	Text       string             `json:"text"`
	FontSize   float64            `json:"font_size"`
	FontFamily string             `json:"font_family,omitempty"`
	FontWeight measure.FontWeight `json:"font_weight,omitempty"`
	Fill       string             `json:"fill,omitempty"`
	Anchor     TextAnchor         `json:"anchor,omitempty"`
	Baseline   TextBaseline       `json:"baseline,omitempty"`
	TextLength float64            `json:"text_length,omitempty"`
	Opacity    float64            `json:"opacity,omitempty"`
	Link       string             `json:"link,omitempty"`
}

func (t *TextElement) Type() string      { return "text" }
func (t *TextElement) ElementID() string { return t.ID }
func (t *TextElement) Bounds() Rect {
	fontSize := t.FontSize
	if fontSize <= 0 {
		fontSize = 14.0
	}
	w := t.TextLength
	if w <= 0 {
		w, _ = measure.Default().MeasureText(t.Text, fontSize, t.FontWeight)
	}
	h := fontSize * 1.2
	ascent := fontSize * 0.95

	var x float64
	switch t.Anchor {
	case AnchorMiddle:
		x = t.X - w/2.0
	case AnchorEnd:
		x = t.X - w
	default: // AnchorStart
		x = t.X
	}

	var y float64
	switch t.Baseline {
	case BaselineMiddle, BaselineCentral:
		y = t.Y - h/2.0
	case BaselineHanging, BaselineTop:
		y = t.Y
	default: // BaselineAlphabetic
		y = t.Y - ascent
	}

	return Rect{
		X:      x,
		Y:      y,
		Width:  w,
		Height: h,
	}
}

// Group represents a hierarchical grouping of child primitives.
type GroupElement struct {
	ID        string      `json:"id,omitempty"`
	Class     string      `json:"class,omitempty"`
	Transform string      `json:"transform,omitempty"`
	Children  []Primitive `json:"children"`
	Link      string      `json:"link,omitempty"`
}

func (g *GroupElement) Type() string      { return "group" }
func (g *GroupElement) ElementID() string { return g.ID }
func (g *GroupElement) Bounds() Rect {
	if len(g.Children) == 0 {
		return Rect{}
	}
	r := g.Children[0].Bounds()
	for _, child := range g.Children[1:] {
		r = r.Union(child.Bounds())
	}
	return r
}
