package measure

import (
	"fmt"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Point represents a 2D coordinate in user space.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PathOp represents the type of a path segment operation.
type PathOp string

const (
	OpMoveTo PathOp = "M"
	OpLineTo PathOp = "L"
	OpQuadTo PathOp = "Q"
	OpCubeTo PathOp = "C"
	OpClose  PathOp = "Z"
)

// PathSegment represents a single vector drawing step.
type PathSegment struct {
	Op   PathOp  `json:"op"`
	Args []Point `json:"args,omitempty"`
}

// Outlines returns the vector glyph outline segments for text placed at (startX, startY).
func (m *Measurer) Outlines(text string, fontSize float64, weight FontWeight, startX, startY float64) ([]PathSegment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	f := m.getFont(weight)
	scale := fixed.Int26_6(math.Round(fontSize * 64))

	var segments []PathSegment
	curX := fixed.Int26_6(math.Round(startX * 64))
	curY := fixed.Int26_6(math.Round(startY * 64))

	for _, r := range text {
		idx, err := f.GlyphIndex(&m.buf, r)
		if err != nil {
			idx = 0
		}

		segs, err := f.LoadGlyph(&m.buf, idx, scale, nil)
		if err == nil {
			for _, s := range segs {
				switch s.Op {
				case sfnt.SegmentOpMoveTo:
					segments = append(segments, PathSegment{
						Op: OpMoveTo,
						Args: []Point{
							{X: float64(curX+s.Args[0].X) / 64.0, Y: float64(curY-s.Args[0].Y) / 64.0},
						},
					})
				case sfnt.SegmentOpLineTo:
					segments = append(segments, PathSegment{
						Op: OpLineTo,
						Args: []Point{
							{X: float64(curX+s.Args[0].X) / 64.0, Y: float64(curY-s.Args[0].Y) / 64.0},
						},
					})
				case sfnt.SegmentOpQuadTo:
					segments = append(segments, PathSegment{
						Op: OpQuadTo,
						Args: []Point{
							{X: float64(curX+s.Args[0].X) / 64.0, Y: float64(curY-s.Args[0].Y) / 64.0},
							{X: float64(curX+s.Args[1].X) / 64.0, Y: float64(curY-s.Args[1].Y) / 64.0},
						},
					})
				case sfnt.SegmentOpCubeTo:
					segments = append(segments, PathSegment{
						Op: OpCubeTo,
						Args: []Point{
							{X: float64(curX+s.Args[0].X) / 64.0, Y: float64(curY-s.Args[0].Y) / 64.0},
							{X: float64(curX+s.Args[1].X) / 64.0, Y: float64(curY-s.Args[1].Y) / 64.0},
							{X: float64(curX+s.Args[2].X) / 64.0, Y: float64(curY-s.Args[2].Y) / 64.0},
						},
					})
				}
			}
		}

		adv, err := f.GlyphAdvance(&m.buf, idx, scale, font.HintingNone)
		if err != nil {
			adv = fixed.Int26_6(fontSize * 0.6 * 64)
		}
		curX += adv
	}

	return segments, nil
}

// SegmentsToSVGPath formats a list of path segments into a deterministic SVG path 'd' attribute string.
func SegmentsToSVGPath(segments []PathSegment) string {
	var sb strings.Builder
	for i, seg := range segments {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(string(seg.Op))
		for j, pt := range seg.Args {
			if j > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(fmt.Sprintf("%.2f %.2f", pt.X, pt.Y))
		}
	}
	return sb.String()
}
