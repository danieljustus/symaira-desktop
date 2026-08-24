package draw

import (
	"fmt"
	"math"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// BuildChartScene constructs a positioned Scene for chart specifications.
func BuildChartScene(d *ir.Diagram) (*scene.Scene, error) {
	if d == nil || d.Chart == nil {
		return nil, fmt.Errorf("chart specification is required")
	}

	th := theme.Resolve(d.Theme)
	width := d.Width
	if width <= 0 {
		width = 600
	}
	height := d.Height
	if height <= 0 {
		height = 360
	}

	sc := scene.NewScene(width, height, th)
	chart := d.Chart

	// Title
	titleY := 35.0
	plotTop := 60.0
	if chart.Title != "" {
		sc.Add(&scene.TextElement{
			X:          width / 2.0,
			Y:          titleY,
			Text:       chart.Title,
			FontSize:   16,
			FontWeight: measure.WeightBold,
			Fill:       th.Text,
			Anchor:     scene.AnchorMiddle,
			Baseline:   scene.BaselineMiddle,
		})
	} else {
		plotTop = 30.0
	}

	plotLeft := 60.0
	plotRight := width - 40.0
	plotBottom := height - 50.0

	if chart.Legend {
		plotRight = width - 140.0
	}

	plotW := plotRight - plotLeft
	plotH := plotBottom - plotTop

	// Find data bounds
	var minY, maxY float64
	firstPoint := true
	for _, s := range chart.Series {
		for _, pt := range s.Data {
			if firstPoint {
				minY, maxY = pt.Y, pt.Y
				firstPoint = false
			} else {
				if pt.Y < minY {
					minY = pt.Y
				}
				if pt.Y > maxY {
					maxY = pt.Y
				}
			}
		}
	}

	if chart.YAxis.Min != nil {
		minY = *chart.YAxis.Min
	} else if minY > 0 {
		minY = 0 // Baseline zero for bar/line charts if all positive
	}
	if chart.YAxis.Max != nil {
		maxY = *chart.YAxis.Max
	}
	if maxY == minY {
		maxY = minY + 10
	}

	// Grid & Axes
	numTicks := 5
	for i := 0; i <= numTicks; i++ {
		tVal := float64(i) / float64(numTicks)
		yPos := plotBottom - tVal*plotH
		val := minY + tVal*(maxY-minY)

		// Grid line
		sc.Add(&scene.LineElement{
			X1:          plotLeft,
			Y1:          yPos,
			X2:          plotRight,
			Y2:          yPos,
			Stroke:      th.Grid,
			StrokeWidth: 1,
			DashArray:   "4 4",
		})

		// Y Tick label
		sc.Add(&scene.TextElement{
			X:          plotLeft - 10,
			Y:          yPos,
			Text:       fmt.Sprintf("%.1f", val),
			FontSize:   10,
			FontWeight: measure.WeightRegular,
			Fill:       th.TextMuted,
			Anchor:     scene.AnchorEnd,
			Baseline:   scene.BaselineMiddle,
		})
	}

	// Y-axis line & X-axis line
	sc.Add(&scene.LineElement{
		X1:          plotLeft,
		Y1:          plotTop,
		X2:          plotLeft,
		Y2:          plotBottom,
		Stroke:      th.Border,
		StrokeWidth: 1.5,
	})
	sc.Add(&scene.LineElement{
		X1:          plotLeft,
		Y1:          plotBottom,
		X2:          plotRight,
		Y2:          plotBottom,
		Stroke:      th.Border,
		StrokeWidth: 1.5,
	})

	// Render chart kinds
	switch chart.Type {
	case ir.ChartBar:
		renderBarChart(sc, chart, th, plotLeft, plotTop, plotW, plotH, plotBottom, minY, maxY)
	case ir.ChartLine:
		renderLineChart(sc, chart, th, plotLeft, plotTop, plotW, plotH, plotBottom, minY, maxY)
	case ir.ChartPie, ir.ChartDonut:
		renderPieChart(sc, chart, th, width, height, chart.Type == ir.ChartDonut)
	case ir.ChartScatter:
		renderScatterChart(sc, chart, th, plotLeft, plotTop, plotW, plotH, plotBottom, minY, maxY)
	default:
		renderBarChart(sc, chart, th, plotLeft, plotTop, plotW, plotH, plotBottom, minY, maxY)
	}

	// Legend
	if chart.Legend {
		legendX := plotRight + 20
		legendY := plotTop + 10
		for i, s := range chart.Series {
			color := s.Color
			if color == "" {
				color = th.Palette[i%len(th.Palette)]
			}
			sc.Add(&scene.RectElement{
				X:      legendX,
				Y:      legendY + float64(i)*24,
				Width:  12,
				Height: 12,
				Rx:     2,
				Ry:     2,
				Fill:   color,
			})
			sc.Add(&scene.TextElement{
				X:          legendX + 18,
				Y:          legendY + float64(i)*24 + 9,
				Text:       s.Name,
				FontSize:   12,
				FontWeight: measure.WeightRegular,
				Fill:       th.Text,
				Anchor:     scene.AnchorStart,
				Baseline:   scene.BaselineMiddle,
			})
		}
	}

	return sc, nil
}

func renderBarChart(
	sc *scene.Scene,
	chart *ir.ChartSpec,
	th theme.Theme,
	plotLeft, plotTop, plotW, plotH, plotBottom float64,
	minY, maxY float64,
) {
	numSeries := len(chart.Series)
	if numSeries == 0 {
		return
	}

	numCats := 0
	for _, s := range chart.Series {
		if len(s.Data) > numCats {
			numCats = len(s.Data)
		}
	}
	if numCats == 0 {
		return
	}

	catWidth := plotW / float64(numCats)
	barGroupPadding := catWidth * 0.2
	availableWidth := catWidth - barGroupPadding*2
	barWidth := availableWidth / float64(numSeries)

	for catIdx := 0; catIdx < numCats; catIdx++ {
		catCenterX := plotLeft + float64(catIdx)*catWidth + catWidth/2.0

		for sIdx, s := range chart.Series {
			if catIdx >= len(s.Data) {
				continue
			}
			pt := s.Data[catIdx]

			color := pt.Color
			if color == "" {
				color = s.Color
			}
			if color == "" {
				color = th.Palette[sIdx%len(th.Palette)]
			}

			normY := (pt.Y - minY) / (maxY - minY)
			barH := normY * plotH
			barX := plotLeft + float64(catIdx)*catWidth + barGroupPadding + float64(sIdx)*barWidth
			barY := plotBottom - barH

			sc.Add(&scene.RectElement{
				X:      barX,
				Y:      barY,
				Width:  barWidth - 2,
				Height: barH,
				Rx:     3,
				Ry:     3,
				Fill:   color,
			})

			// Label
			if pt.Label != "" && sIdx == 0 {
				sc.Add(&scene.TextElement{
					X:          catCenterX,
					Y:          plotBottom + 18,
					Text:       pt.Label,
					FontSize:   11,
					FontWeight: measure.WeightRegular,
					Fill:       th.TextMuted,
					Anchor:     scene.AnchorMiddle,
					Baseline:   scene.BaselineMiddle,
				})
			}
		}
	}
}

func renderLineChart(
	sc *scene.Scene,
	chart *ir.ChartSpec,
	th theme.Theme,
	plotLeft, plotTop, plotW, plotH, plotBottom float64,
	minY, maxY float64,
) {
	for sIdx, s := range chart.Series {
		if len(s.Data) == 0 {
			continue
		}

		color := s.Color
		if color == "" {
			color = th.Palette[sIdx%len(th.Palette)]
		}

		var points []measure.Point
		step := plotW / math.Max(1, float64(len(s.Data)-1))

		for i, pt := range s.Data {
			px := plotLeft + float64(i)*step
			normY := (pt.Y - minY) / (maxY - minY)
			py := plotBottom - normY*plotH
			points = append(points, measure.Point{X: px, Y: py})

			// Data point circle
			sc.Add(&scene.CircleElement{
				CX:          px,
				CY:          py,
				R:           4,
				Fill:        color,
				Stroke:      th.Background,
				StrokeWidth: 1.5,
			})

			if pt.Label != "" {
				sc.Add(&scene.TextElement{
					X:          px,
					Y:          plotBottom + 18,
					Text:       pt.Label,
					FontSize:   11,
					FontWeight: measure.WeightRegular,
					Fill:       th.TextMuted,
					Anchor:     scene.AnchorMiddle,
					Baseline:   scene.BaselineMiddle,
				})
			}
		}

		sc.Add(&scene.PolylineElement{
			Points:      points,
			Fill:        "none",
			Stroke:      color,
			StrokeWidth: 2.5,
		})
	}
}

func renderScatterChart(
	sc *scene.Scene,
	chart *ir.ChartSpec,
	th theme.Theme,
	plotLeft, plotTop, plotW, plotH, plotBottom float64,
	minY, maxY float64,
) {
	for sIdx, s := range chart.Series {
		color := s.Color
		if color == "" {
			color = th.Palette[sIdx%len(th.Palette)]
		}
		for _, pt := range s.Data {
			px := plotLeft + (pt.X/100.0)*plotW
			normY := (pt.Y - minY) / (maxY - minY)
			py := plotBottom - normY*plotH

			sc.Add(&scene.CircleElement{
				CX:     px,
				CY:     py,
				R:      5,
				Fill:   color,
				Stroke: th.Background,
			})
		}
	}
}

func renderPieChart(
	sc *scene.Scene,
	chart *ir.ChartSpec,
	th theme.Theme,
	width, height float64,
	isDonut bool,
) {
	cx := width / 2.0
	cy := height / 2.0
	radius := math.Min(width, height) * 0.35

	var total float64
	for _, s := range chart.Series {
		for _, pt := range s.Data {
			total += math.Max(0, pt.Y)
		}
	}
	if total <= 0 {
		return
	}

	startAngle := -math.Pi / 2.0
	colorIdx := 0

	for _, s := range chart.Series {
		for _, pt := range s.Data {
			if pt.Y <= 0 {
				continue
			}
			angle := (pt.Y / total) * 2 * math.Pi
			endAngle := startAngle + angle

			color := pt.Color
			if color == "" {
				color = th.Palette[colorIdx%len(th.Palette)]
				colorIdx++
			}

			// Arc path
			x1 := cx + radius*math.Cos(startAngle)
			y1 := cy + radius*math.Sin(startAngle)
			x2 := cx + radius*math.Cos(endAngle)
			y2 := cy + radius*math.Sin(endAngle)

			largeArc := 0
			if angle > math.Pi {
				largeArc = 1
			}

			pathD := fmt.Sprintf("M %.2f %.2f A %.2f %.2f 0 %d 1 %.2f %.2f L %.2f %.2f Z",
				x1, y1, radius, radius, largeArc, x2, y2, cx, cy)

			sc.Add(&scene.PathElement{
				D:           pathD,
				Fill:        color,
				Stroke:      th.Background,
				StrokeWidth: 2,
			})

			startAngle = endAngle
		}
	}

	if isDonut {
		sc.Add(&scene.CircleElement{
			CX:   cx,
			CY:   cy,
			R:    radius * 0.5,
			Fill: th.Background,
		})
	}
}
