package draw

import (
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// BuildSceneFromIR translates an IR Diagram into a positioned scene.Scene graph.
func BuildSceneFromIR(d *ir.Diagram) (*scene.Scene, error) {
	if err := ir.Validate(d); err != nil {
		return nil, fmt.Errorf("validate ir diagram: %w", err)
	}

	if d.Kind == ir.KindChart || d.Chart != nil {
		return BuildChartScene(d)
	}

	th := theme.Resolve(d.Theme)
	width := d.Width
	if width <= 0 {
		width = 800
	}
	height := d.Height
	if height <= 0 {
		height = 600
	}

	sc := scene.NewScene(width, height, th)
	measurer := measure.Default()
	measureOpts := measure.DefaultNodeMeasureOptions()

	nodeBounds := make(map[string]scene.Rect)

	// If nodes are not positioned, apply simple default grid/horizontal placement
	hasPositions := false
	for _, n := range d.Nodes {
		if n.X != 0 || n.Y != 0 {
			hasPositions = true
			break
		}
	}

	curX := 60.0
	curY := 60.0

	for i, node := range d.Nodes {
		w, h, lines := measurer.MeasureNode(node.Label, node.Shape, measureOpts)
		if node.Width > 0 {
			w = node.Width
		}
		if node.Height > 0 {
			h = node.Height
		}

		nx := node.X
		ny := node.Y
		if !hasPositions {
			if d.Direction == ir.DirLR || d.Direction == ir.DirRL {
				nx = curX
				ny = 100.0
				curX += w + 60.0
			} else { // TD / TB default
				nx = 100.0
				ny = curY
				curY += h + 60.0
			}
		}

		nodeBounds[node.ID] = scene.Rect{X: nx, Y: ny, Width: w, Height: h}

		// Node styling
		fill := node.Style.Fill
		if fill == "" {
			fill = th.Surface
		}
		stroke := node.Style.Stroke
		if stroke == "" {
			stroke = th.Border
		}
		sw := 1.5
		if node.Style.StrokeWidth != nil {
			sw = *node.Style.StrokeWidth
		}

		var prim scene.Primitive
		switch node.Shape {
		case ir.ShapeCircle:
			r := w / 2.0
			prim = &scene.CircleElement{
				ID:          node.ID,
				CX:          nx + r,
				CY:          ny + r,
				R:           r,
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
				Link:        node.Note,
			}

		case ir.ShapeDiamond:
			cx := nx + w/2.0
			cy := ny + h/2.0
			prim = &scene.PolygonElement{
				ID: node.ID,
				Points: []measure.Point{
					{X: cx, Y: ny},
					{X: nx + w, Y: cy},
					{X: cx, Y: ny + h},
					{X: nx, Y: cy},
				},
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
			}

		case ir.ShapeCylinder:
			prim = &scene.RectElement{
				ID:          node.ID,
				X:           nx,
				Y:           ny,
				Width:       w,
				Height:      h,
				Rx:          12,
				Ry:          12,
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
				Link:        node.Note,
			}

		case ir.ShapePill, ir.ShapeStadium:
			rx := h / 2.0
			prim = &scene.RectElement{
				ID:          node.ID,
				X:           nx,
				Y:           ny,
				Width:       w,
				Height:      h,
				Rx:          rx,
				Ry:          rx,
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
				Link:        node.Note,
			}

		case ir.ShapeHexagon:
			cx1 := nx + h*0.25
			cx2 := nx + w - h*0.25
			cy := ny + h/2.0
			prim = &scene.PolygonElement{
				ID: node.ID,
				Points: []measure.Point{
					{X: cx1, Y: ny},
					{X: cx2, Y: ny},
					{X: nx + w, Y: cy},
					{X: cx2, Y: ny + h},
					{X: cx1, Y: ny + h},
					{X: nx, Y: cy},
				},
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
			}

		case ir.ShapeRound:
			prim = &scene.RectElement{
				ID:          node.ID,
				X:           nx,
				Y:           ny,
				Width:       w,
				Height:      h,
				Rx:          8,
				Ry:          8,
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
				Link:        node.Note,
			}

		default: // ShapeRect
			prim = &scene.RectElement{
				ID:          node.ID,
				X:           nx,
				Y:           ny,
				Width:       w,
				Height:      h,
				Fill:        fill,
				Stroke:      stroke,
				StrokeWidth: sw,
				Link:        node.Note,
			}
		}

		sc.Add(prim)

		// Render text lines inside node
		if len(lines) > 0 {
			textColor := node.Style.TextColor
			if textColor == "" {
				textColor = th.Text
			}

			fontSize := measureOpts.FontSize
			lineMetrics, _ := measurer.MeasureTextDetailed("Ag", fontSize, measure.WeightRegular)
			lineH := lineMetrics.LineHeight
			lineSpacing := measureOpts.LineSpacing
			totalTextH := float64(len(lines))*lineH + float64(len(lines)-1)*lineSpacing

			startY := ny + (h-totalTextH)/2.0 + lineMetrics.Ascent
			centerX := nx + w/2.0

			for lineIdx, lineStr := range lines {
				lineY := startY + float64(lineIdx)*(lineH+lineSpacing)
				sc.Add(&scene.TextElement{
					ID:         fmt.Sprintf("%s-text-%d", node.ID, lineIdx),
					X:          centerX,
					Y:          lineY,
					Text:       lineStr,
					FontSize:   fontSize,
					FontWeight: measure.WeightRegular,
					Fill:       textColor,
					Anchor:     scene.AnchorMiddle,
					Baseline:   scene.BaselineAlphabetic,
				})
			}
		}
		_ = i
	}

	// Render Edges
	for i, edge := range d.Edges {
		fromBox, ok1 := nodeBounds[edge.From]
		toBox, ok2 := nodeBounds[edge.To]
		if !ok1 || !ok2 {
			continue
		}

		x1 := fromBox.X + fromBox.Width/2.0
		y1 := fromBox.Y + fromBox.Height
		x2 := toBox.X + toBox.Width/2.0
		y2 := toBox.Y

		if d.Direction == ir.DirLR {
			x1 = fromBox.X + fromBox.Width
			y1 = fromBox.Y + fromBox.Height/2.0
			x2 = toBox.X
			y2 = toBox.Y + toBox.Height/2.0
		}

		edgeColor := edge.Color
		if edgeColor == "" {
			edgeColor = th.Edge
		}

		markerEnd := ""
		if edge.Arrow == ir.ArrowSingle || edge.Arrow == "" {
			markerEnd = "arrow-end"
		} else if edge.Arrow == ir.ArrowDouble {
			markerEnd = "arrow-end"
		}

		dash := ""
		if edge.Style == ir.EdgeDashed {
			dash = "6 4"
		} else if edge.Style == ir.EdgeDotted {
			dash = "2 3"
		}

		sw := 1.5
		if edge.Style == ir.EdgeThick {
			sw = 3.0
		}

		sc.Add(&scene.LineElement{
			ID:          fmt.Sprintf("edge-%d", i),
			X1:          x1,
			Y1:          y1,
			X2:          x2,
			Y2:          y2,
			Stroke:      edgeColor,
			StrokeWidth: sw,
			DashArray:   dash,
			MarkerEnd:   markerEnd,
		})

		// Edge label
		if edge.Label != "" {
			midX := (x1 + x2) / 2.0
			midY := (y1 + y2) / 2.0
			sc.Add(&scene.TextElement{
				X:          midX + 6,
				Y:          midY - 4,
				Text:       edge.Label,
				FontSize:   11,
				FontWeight: measure.WeightRegular,
				Fill:       th.TextMuted,
				Anchor:     scene.AnchorStart,
				Baseline:   scene.BaselineMiddle,
			})
		}
	}

	// Render Groups
	for i, grp := range d.Groups {
		if len(grp.Members) == 0 {
			continue
		}
		var gb scene.Rect
		first := true
		for _, m := range grp.Members {
			if box, ok := nodeBounds[m]; ok {
				if first {
					gb = box
					first = false
				} else {
					gb = gb.Union(box)
				}
			}
		}
		if !first {
			padded := gb.Pad(20)
			sc.Add(&scene.RectElement{
				ID:          fmt.Sprintf("group-%d", i),
				X:           padded.X,
				Y:           padded.Y,
				Width:       padded.Width,
				Height:      padded.Height,
				Rx:          6,
				Ry:          6,
				Fill:        th.GroupBackground,
				Stroke:      th.GroupBorder,
				StrokeWidth: 1.0,
				DashArray:   "4 4",
			})
			if grp.Label != "" {
				sc.Add(&scene.TextElement{
					X:          padded.X + 12,
					Y:          padded.Y + 16,
					Text:       grp.Label,
					FontSize:   12,
					FontWeight: measure.WeightBold,
					Fill:       th.TextMuted,
					Anchor:     scene.AnchorStart,
					Baseline:   scene.BaselineMiddle,
				})
			}
		}
	}

	sc.FitToBounds(30)
	return sc, nil
}
