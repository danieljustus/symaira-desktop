package draw

import (
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/layout"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

func renderLayoutEdge(sc *scene.Scene, edge ir.Edge, index int, route layout.Route, th theme.Theme) {
	if len(route.Points) < 2 {
		return
	}

	stroke := edge.Color
	if stroke == "" {
		stroke = th.Edge
	}
	strokeWidth := 1.5
	if edge.Style == ir.EdgeThick {
		strokeWidth = 3
	}
	dash := ""
	switch edge.Style {
	case ir.EdgeDashed:
		dash = "6 4"
	case ir.EdgeDotted:
		dash = "2 3"
	}

	if len(route.Points) == 2 {
		sc.Add(&scene.LineElement{
			ID:          fmt.Sprintf("edge-%d", index),
			X1:          route.Points[0].X,
			Y1:          route.Points[0].Y,
			X2:          route.Points[1].X,
			Y2:          route.Points[1].Y,
			Stroke:      stroke,
			StrokeWidth: strokeWidth,
			DashArray:   dash,
			MarkerEnd:   "arrow-end",
		})
	} else {
		sc.Add(&scene.PolylineElement{
			ID:          fmt.Sprintf("edge-%d", index),
			Points:      route.Points,
			Fill:        "none",
			Stroke:      stroke,
			StrokeWidth: strokeWidth,
			DashArray:   dash,
			MarkerEnd:   "arrow-end",
		})
	}

	if edge.Label != "" {
		mid := route.Points[len(route.Points)/2]
		sc.Add(&scene.TextElement{
			ID:         fmt.Sprintf("edge-%d-label", index),
			X:          mid.X + 6,
			Y:          mid.Y - 4,
			Text:       edge.Label,
			FontSize:   11,
			FontWeight: measure.WeightRegular,
			Fill:       th.TextMuted,
			Anchor:     scene.AnchorStart,
			Baseline:   scene.BaselineMiddle,
		})
	}
}
