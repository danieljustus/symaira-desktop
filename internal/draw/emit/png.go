package emit

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"golang.org/x/image/vector"
)

// RasterOptions configures pure-Go rasterization to bitmap formats.
type RasterOptions struct {
	Scale float64 // Scale multiplier (1.0 = 1x, 2.0 = 2x retina)
}

// Rasterize renders a scene.Scene into an in-memory image.RGBA buffer.
func Rasterize(sc *scene.Scene, opts RasterOptions) (*image.RGBA, error) {
	if sc == nil {
		return nil, fmt.Errorf("scene is nil")
	}

	scale := opts.Scale
	if scale <= 0 {
		scale = 1.0
	}

	imgW := int(math.Ceil(sc.Width * scale))
	imgH := int(math.Ceil(sc.Height * scale))
	if imgW <= 0 {
		imgW = 100
	}
	if imgH <= 0 {
		imgH = 100
	}

	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))

	// Background
	if sc.Background != "" && sc.Background != "none" && sc.Background != "transparent" {
		bgCol, err := parseColor(sc.Background, 1.0)
		if err == nil {
			draw.Draw(img, img.Bounds(), image.NewUniform(bgCol), image.Point{}, draw.Src)
		}
	}

	// Compute transform from Scene ViewBox to Image pixel coordinates
	vb := sc.ViewBox
	if vb.Width <= 0 {
		vb.Width = sc.Width
	}
	if vb.Height <= 0 {
		vb.Height = sc.Height
	}

	scaleX := (float64(imgW) / vb.Width)
	scaleY := (float64(imgH) / vb.Height)

	tx := func(x float64) float32 {
		return float32((x - vb.X) * scaleX)
	}
	ty := func(y float64) float32 {
		return float32((y - vb.Y) * scaleY)
	}
	ts := func(size float64) float32 {
		return float32(size * scaleX)
	}

	// Render primitives
	for _, p := range sc.Primitives {
		rasterizePrimitive(img, p, tx, ty, ts, imgW, imgH)
	}

	return img, nil
}

// EmitPNG encodes the scene into deterministic PNG bytes.
func EmitPNG(sc *scene.Scene, opts RasterOptions) ([]byte, error) {
	img, err := Rasterize(sc, opts)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := png.Encoder{
		CompressionLevel: png.DefaultCompression,
	}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	return buf.Bytes(), nil
}

func rasterizePrimitive(
	img *image.RGBA,
	p scene.Primitive,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	switch el := p.(type) {
	case *scene.RectElement:
		rasterizeRect(img, el, tx, ty, ts, w, h)
	case *scene.CircleElement:
		rasterizeCircle(img, el, tx, ty, ts, w, h)
	case *scene.EllipseElement:
		rasterizeEllipse(img, el, tx, ty, ts, w, h)
	case *scene.LineElement:
		rasterizeLine(img, el, tx, ty, ts, w, h)
	case *scene.PolylineElement:
		rasterizePolyline(img, el, tx, ty, ts, w, h)
	case *scene.PolygonElement:
		rasterizePolygon(img, el, tx, ty, ts, w, h)
	case *scene.PathElement:
		rasterizePath(img, el, tx, ty, ts, w, h)
	case *scene.TextElement:
		rasterizeText(img, el, tx, ty, ts, w, h)
	case *scene.GroupElement:
		for _, child := range el.Children {
			rasterizePrimitive(img, child, tx, ty, ts, w, h)
		}
	}
}

func rasterizeRect(
	img *image.RGBA,
	el *scene.RectElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	x := tx(el.X)
	y := ty(el.Y)
	rw := ts(el.Width)
	rh := ts(el.Height)
	rx := ts(el.Rx)
	ry := ts(el.Ry)

	// Fill
	if el.Fill != "" && el.Fill != "none" {
		fillCol, err := parseColor(el.Fill, el.Opacity)
		if err == nil {
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			addRoundRectPath(z, x, y, rw, rh, rx, ry)
			z.Draw(img, img.Bounds(), image.NewUniform(fillCol), image.Point{})
		}
	}

	// Stroke
	if el.Stroke != "" && el.Stroke != "none" && el.StrokeWidth > 0 {
		strokeCol, err := parseColor(el.Stroke, el.Opacity)
		if err == nil {
			sw := ts(el.StrokeWidth)
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over

			// Outer rect
			addRoundRectPath(z, x-sw/2, y-sw/2, rw+sw, rh+sw, rx+sw/2, ry+sw/2)
			// Inner rect (counter-clockwise)
			if rw > sw && rh > sw {
				addRoundRectPathCCW(z, x+sw/2, y+sw/2, rw-sw, rh-sw, math.Max(0, float64(rx-sw/2)), math.Max(0, float64(ry-sw/2)))
			}
			z.Draw(img, img.Bounds(), image.NewUniform(strokeCol), image.Point{})
		}
	}
}

func rasterizeCircle(
	img *image.RGBA,
	el *scene.CircleElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	cx := tx(el.CX)
	cy := ty(el.CY)
	r := ts(el.R)

	// Fill
	if el.Fill != "" && el.Fill != "none" {
		fillCol, err := parseColor(el.Fill, el.Opacity)
		if err == nil {
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			addEllipsePath(z, cx, cy, r, r)
			z.Draw(img, img.Bounds(), image.NewUniform(fillCol), image.Point{})
		}
	}

	// Stroke
	if el.Stroke != "" && el.Stroke != "none" && el.StrokeWidth > 0 {
		strokeCol, err := parseColor(el.Stroke, el.Opacity)
		if err == nil {
			sw := ts(el.StrokeWidth)
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			addEllipsePath(z, cx, cy, r+sw/2, r+sw/2)
			if r > sw/2 {
				addEllipsePathCCW(z, cx, cy, r-sw/2, r-sw/2)
			}
			z.Draw(img, img.Bounds(), image.NewUniform(strokeCol), image.Point{})
		}
	}
}

func rasterizeEllipse(
	img *image.RGBA,
	el *scene.EllipseElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	cx := tx(el.CX)
	cy := ty(el.CY)
	rx := ts(el.RX)
	ry := ts(el.RY)

	if el.Fill != "" && el.Fill != "none" {
		fillCol, err := parseColor(el.Fill, el.Opacity)
		if err == nil {
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			addEllipsePath(z, cx, cy, rx, ry)
			z.Draw(img, img.Bounds(), image.NewUniform(fillCol), image.Point{})
		}
	}

	if el.Stroke != "" && el.Stroke != "none" && el.StrokeWidth > 0 {
		strokeCol, err := parseColor(el.Stroke, el.Opacity)
		if err == nil {
			sw := ts(el.StrokeWidth)
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			addEllipsePath(z, cx, cy, rx+sw/2, ry+sw/2)
			if rx > sw/2 && ry > sw/2 {
				addEllipsePathCCW(z, cx, cy, rx-sw/2, ry-sw/2)
			}
			z.Draw(img, img.Bounds(), image.NewUniform(strokeCol), image.Point{})
		}
	}
}

func rasterizeLine(
	img *image.RGBA,
	el *scene.LineElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	if el.Stroke == "" || el.Stroke == "none" || el.StrokeWidth <= 0 {
		return
	}

	strokeCol, err := parseColor(el.Stroke, el.Opacity)
	if err != nil {
		return
	}

	x1, y1 := tx(el.X1), ty(el.Y1)
	x2, y2 := tx(el.X2), ty(el.Y2)
	sw := ts(el.StrokeWidth)

	dx := x2 - x1
	dy := y2 - y1
	lenSq := dx*dx + dy*dy
	if lenSq <= 0.0001 {
		return
	}
	length := float32(math.Sqrt(float64(lenSq)))
	nx := (-dy / length) * (sw / 2)
	ny := (dx / length) * (sw / 2)

	z := vector.NewRasterizer(w, h)
	z.DrawOp = draw.Over
	z.MoveTo(x1+nx, y1+ny)
	z.LineTo(x2+nx, y2+ny)
	z.LineTo(x2-nx, y2-ny)
	z.LineTo(x1-nx, y1-ny)
	z.ClosePath()
	z.Draw(img, img.Bounds(), image.NewUniform(strokeCol), image.Point{})
}

func rasterizePolyline(
	img *image.RGBA,
	el *scene.PolylineElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	if len(el.Points) < 2 {
		return
	}
	// Stroke each segment
	if el.Stroke != "" && el.Stroke != "none" && el.StrokeWidth > 0 {
		for i := 0; i < len(el.Points)-1; i++ {
			line := &scene.LineElement{
				X1:          el.Points[i].X,
				Y1:          el.Points[i].Y,
				X2:          el.Points[i+1].X,
				Y2:          el.Points[i+1].Y,
				Stroke:      el.Stroke,
				StrokeWidth: el.StrokeWidth,
				Opacity:     el.Opacity,
			}
			rasterizeLine(img, line, tx, ty, ts, w, h)
		}
	}
}

func rasterizePolygon(
	img *image.RGBA,
	el *scene.PolygonElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	if len(el.Points) < 3 {
		return
	}

	if el.Fill != "" && el.Fill != "none" {
		fillCol, err := parseColor(el.Fill, el.Opacity)
		if err == nil {
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			z.MoveTo(tx(el.Points[0].X), ty(el.Points[0].Y))
			for _, pt := range el.Points[1:] {
				z.LineTo(tx(pt.X), ty(pt.Y))
			}
			z.ClosePath()
			z.Draw(img, img.Bounds(), image.NewUniform(fillCol), image.Point{})
		}
	}

	if el.Stroke != "" && el.Stroke != "none" && el.StrokeWidth > 0 {
		for i := 0; i < len(el.Points); i++ {
			next := (i + 1) % len(el.Points)
			line := &scene.LineElement{
				X1:          el.Points[i].X,
				Y1:          el.Points[i].Y,
				X2:          el.Points[next].X,
				Y2:          el.Points[next].Y,
				Stroke:      el.Stroke,
				StrokeWidth: el.StrokeWidth,
				Opacity:     el.Opacity,
			}
			rasterizeLine(img, line, tx, ty, ts, w, h)
		}
	}
}

func rasterizePath(
	img *image.RGBA,
	el *scene.PathElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	if len(el.Segments) == 0 {
		return
	}

	if el.Fill != "" && el.Fill != "none" {
		fillCol, err := parseColor(el.Fill, el.Opacity)
		if err == nil {
			z := vector.NewRasterizer(w, h)
			z.DrawOp = draw.Over
			for _, seg := range el.Segments {
				switch seg.Op {
				case measure.OpMoveTo:
					z.MoveTo(tx(seg.Args[0].X), ty(seg.Args[0].Y))
				case measure.OpLineTo:
					z.LineTo(tx(seg.Args[0].X), ty(seg.Args[0].Y))
				case measure.OpQuadTo:
					z.QuadTo(tx(seg.Args[0].X), ty(seg.Args[0].Y), tx(seg.Args[1].X), ty(seg.Args[1].Y))
				case measure.OpCubeTo:
					z.CubeTo(tx(seg.Args[0].X), ty(seg.Args[0].Y), tx(seg.Args[1].X), ty(seg.Args[1].Y), tx(seg.Args[2].X), ty(seg.Args[2].Y))
				case measure.OpClose:
					z.ClosePath()
				}
			}
			z.Draw(img, img.Bounds(), image.NewUniform(fillCol), image.Point{})
		}
	}
}

func rasterizeText(
	img *image.RGBA,
	el *scene.TextElement,
	tx, ty func(float64) float32,
	ts func(float64) float32,
	w, h int,
) {
	fontSize := el.FontSize
	if fontSize <= 0 {
		fontSize = 14.0
	}
	fill := el.Fill
	if fill == "" {
		fill = "#F5F4F0"
	}
	textCol, err := parseColor(fill, el.Opacity)
	if err != nil {
		return
	}

	textLen := el.TextLength
	if textLen <= 0 {
		textLen, _ = measure.Default().MeasureText(el.Text, fontSize, el.FontWeight)
	}

	startX := el.X
	switch el.Anchor {
	case scene.AnchorMiddle:
		startX = el.X - textLen/2.0
	case scene.AnchorEnd:
		startX = el.X - textLen
	}

	startY := el.Y
	metrics, _ := measure.Default().MeasureTextDetailed("Ag", fontSize, el.FontWeight)
	switch el.Baseline {
	case scene.BaselineMiddle, scene.BaselineCentral:
		startY = el.Y + metrics.LineHeight/2.0 - metrics.Descent
	case scene.BaselineHanging, scene.BaselineTop:
		startY = el.Y + metrics.Ascent
	}

	segs, err := measure.Default().Outlines(el.Text, fontSize, el.FontWeight, startX, startY)
	if err != nil || len(segs) == 0 {
		return
	}

	z := vector.NewRasterizer(w, h)
	z.DrawOp = draw.Over

	for _, s := range segs {
		switch s.Op {
		case measure.OpMoveTo:
			z.MoveTo(tx(s.Args[0].X), ty(s.Args[0].Y))
		case measure.OpLineTo:
			z.LineTo(tx(s.Args[0].X), ty(s.Args[0].Y))
		case measure.OpQuadTo:
			z.QuadTo(tx(s.Args[0].X), ty(s.Args[0].Y), tx(s.Args[1].X), ty(s.Args[1].Y))
		case measure.OpCubeTo:
			z.CubeTo(tx(s.Args[0].X), ty(s.Args[0].Y), tx(s.Args[1].X), ty(s.Args[1].Y), tx(s.Args[2].X), ty(s.Args[2].Y))
		case measure.OpClose:
			z.ClosePath()
		}
	}

	z.Draw(img, img.Bounds(), image.NewUniform(textCol), image.Point{})
}

// Helpers for paths
func addRoundRectPath(z *vector.Rasterizer, x, y, w, h, rx, ry float32) {
	if rx <= 0 || ry <= 0 {
		z.MoveTo(x, y)
		z.LineTo(x+w, y)
		z.LineTo(x+w, y+h)
		z.LineTo(x, y+h)
		z.ClosePath()
		return
	}

	const kappa = float32(0.5522847498)
	kx := rx * (1 - kappa)
	ky := ry * (1 - kappa)

	z.MoveTo(x+rx, y)
	z.LineTo(x+w-rx, y)
	z.CubeTo(x+w-kx, y, x+w, y+ky, x+w, y+ry)
	z.LineTo(x+w, y+h-ry)
	z.CubeTo(x+w, y+h-ky, x+w-kx, y+h, x+w-rx, y+h)
	z.LineTo(x+rx, y+h)
	z.CubeTo(x+kx, y+h, x, y+h-ky, x, y+h-ry)
	z.LineTo(x, y+ry)
	z.CubeTo(x, y+ky, x+kx, y, x+rx, y)
	z.ClosePath()
}

func addRoundRectPathCCW(z *vector.Rasterizer, x, y, w, h float32, rx, ry float64) {
	rxf := float32(rx)
	ryf := float32(ry)
	if rxf <= 0 || ryf <= 0 {
		z.MoveTo(x, y)
		z.LineTo(x, y+h)
		z.LineTo(x+w, y+h)
		z.LineTo(x+w, y)
		z.ClosePath()
		return
	}

	const kappa = float32(0.5522847498)
	kx := rxf * (1 - kappa)
	ky := ryf * (1 - kappa)

	z.MoveTo(x+rxf, y)
	z.CubeTo(x+kx, y, x, y+ky, x, y+ryf)
	z.LineTo(x, y+h-ryf)
	z.CubeTo(x, y+h-ky, x+kx, y+h, x+rxf, y+h)
	z.LineTo(x+w-rxf, y+h)
	z.CubeTo(x+w-kx, y+h, x+w, y+h-ky, x+w, y+h-ryf)
	z.LineTo(x+w, y+ryf)
	z.CubeTo(x+w, y+ky, x+w-kx, y, x+w-rxf, y)
	z.ClosePath()
}

func addEllipsePath(z *vector.Rasterizer, cx, cy, rx, ry float32) {
	const kappa = float32(0.5522847498)
	ox := rx * kappa
	oy := ry * kappa

	z.MoveTo(cx-rx, cy)
	z.CubeTo(cx-rx, cy-oy, cx-ox, cy-ry, cx, cy-ry)
	z.CubeTo(cx+ox, cy-ry, cx+rx, cy-oy, cx+rx, cy)
	z.CubeTo(cx+rx, cy+oy, cx+ox, cy+ry, cx, cy+ry)
	z.CubeTo(cx-ox, cy+ry, cx-rx, cy+oy, cx-rx, cy)
	z.ClosePath()
}

func addEllipsePathCCW(z *vector.Rasterizer, cx, cy, rx, ry float32) {
	const kappa = float32(0.5522847498)
	ox := rx * kappa
	oy := ry * kappa

	z.MoveTo(cx-rx, cy)
	z.CubeTo(cx-rx, cy+oy, cx-ox, cy+ry, cx, cy+ry)
	z.CubeTo(cx+ox, cy+ry, cx+rx, cy+oy, cx+rx, cy)
	z.CubeTo(cx+rx, cy-oy, cx+ox, cy-ry, cx, cy-ry)
	z.CubeTo(cx-ox, cy-ry, cx-rx, cy-oy, cx-rx, cy)
	z.ClosePath()
}

// Color parsing
func parseColor(c string, opacity float64) (color.Color, error) {
	c = strings.TrimSpace(strings.ToLower(c))
	if c == "" || c == "none" || c == "transparent" {
		return color.Transparent, nil
	}

	alphaMul := 1.0
	if opacity > 0 && opacity <= 1.0 {
		alphaMul = opacity
	}

	if strings.HasPrefix(c, "#") {
		hex := c[1:]
		switch len(hex) {
		case 3: // #RGB
			r, _ := strconv.ParseUint(string([]byte{hex[0], hex[0]}), 16, 8)
			g, _ := strconv.ParseUint(string([]byte{hex[1], hex[1]}), 16, 8)
			b, _ := strconv.ParseUint(string([]byte{hex[2], hex[2]}), 16, 8)
			return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(255 * alphaMul)}, nil
		case 4: // #RGBA
			r, _ := strconv.ParseUint(string([]byte{hex[0], hex[0]}), 16, 8)
			g, _ := strconv.ParseUint(string([]byte{hex[1], hex[1]}), 16, 8)
			b, _ := strconv.ParseUint(string([]byte{hex[2], hex[2]}), 16, 8)
			a, _ := strconv.ParseUint(string([]byte{hex[3], hex[3]}), 16, 8)
			return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(float64(a) * alphaMul)}, nil
		case 6: // #RRGGBB
			r, _ := strconv.ParseUint(hex[0:2], 16, 8)
			g, _ := strconv.ParseUint(hex[2:4], 16, 8)
			b, _ := strconv.ParseUint(hex[4:6], 16, 8)
			return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(255 * alphaMul)}, nil
		case 8: // #RRGGBBAA
			r, _ := strconv.ParseUint(hex[0:2], 16, 8)
			g, _ := strconv.ParseUint(hex[2:4], 16, 8)
			b, _ := strconv.ParseUint(hex[4:6], 16, 8)
			a, _ := strconv.ParseUint(hex[6:8], 16, 8)
			return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(float64(a) * alphaMul)}, nil
		}
	}

	switch c {
	case "white":
		return color.NRGBA{R: 255, G: 255, B: 255, A: uint8(255 * alphaMul)}, nil
	case "black":
		return color.NRGBA{R: 0, G: 0, B: 0, A: uint8(255 * alphaMul)}, nil
	}

	return nil, fmt.Errorf("unsupported color format %q", c)
}
