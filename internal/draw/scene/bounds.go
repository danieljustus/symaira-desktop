package scene

import "math"

// Rect represents a 2D bounding rectangle.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// MaxX returns the rightmost X coordinate.
func (r Rect) MaxX() float64 {
	return r.X + r.Width
}

// MaxY returns the bottommost Y coordinate.
func (r Rect) MaxY() float64 {
	return r.Y + r.Height
}

// IsEmpty reports whether the rectangle has zero or negative area.
func (r Rect) IsEmpty() bool {
	return r.Width <= 0 || r.Height <= 0
}

// Union computes the smallest bounding box containing both r and other.
func (r Rect) Union(other Rect) Rect {
	if r.IsEmpty() {
		return other
	}
	if other.IsEmpty() {
		return r
	}

	minX := math.Min(r.X, other.X)
	minY := math.Min(r.Y, other.Y)
	maxX := math.Max(r.MaxX(), other.MaxX())
	maxY := math.Max(r.MaxY(), other.MaxY())

	return Rect{
		X:      minX,
		Y:      minY,
		Width:  maxX - minX,
		Height: maxY - minY,
	}
}

// Contains reports whether other is completely enclosed within r (within floating-point epsilon).
func (r Rect) Contains(other Rect) bool {
	if other.IsEmpty() {
		return true
	}
	if r.IsEmpty() {
		return false
	}
	eps := 0.01
	return other.X >= r.X-eps &&
		other.Y >= r.Y-eps &&
		other.MaxX() <= r.MaxX()+eps &&
		other.MaxY() <= r.MaxY()+eps
}

// Pad returns a new Rect expanded by padding on all sides.
func (r Rect) Pad(padding float64) Rect {
	if r.IsEmpty() {
		return Rect{X: -padding, Y: -padding, Width: padding * 2, Height: padding * 2}
	}
	return Rect{
		X:      r.X - padding,
		Y:      r.Y - padding,
		Width:  r.Width + padding*2,
		Height: r.Height + padding*2,
	}
}
