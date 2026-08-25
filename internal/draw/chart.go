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
	accessibilityTitle := chart.Title
	if accessibilityTitle == "" {
		accessibilityTitle = d.Title
	}
	if accessibilityTitle == "" {
		accessibilityTitle = fmt.Sprintf("%s chart", chart.Type)
	}
	sc.Metadata["title"] = accessibilityTitle
	sc.Metadata["description"] = fmt.Sprintf("%s chart with %d series", chart.Type, len(chart.Series))

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
				minY = math.Min(minY, pt.Y)
				maxY = math.Max(maxY, pt.Y)
			}
		}
	}
	if firstPoint {
		minY, maxY = 0, 1
	}
	if chart.YAxis.Min != nil {
		minY = *chart.YAxis.Min
	} else if minY > 0 {
		minY = 0
	}
	if chart.YAxis.Max != nil {
		maxY = *chart.YAxis.Max
	}
	minY, maxY, ticks := niceAxis(minY, maxY, 5)
	minX, maxX := 0.0, 1.0
	if chart.Type == ir.ChartScatter {
		firstX := true
		for _, s := range chart.Series {
			for _, pt := range s.Data {
				if firstX {
					minX, maxX = pt.X, pt.X
					firstX = false
				} else {
					minX = math.Min(minX, pt.X)
					maxX = math.Max(maxX, pt.X)
				}
			}
		}
		if chart.XAxis.Min != nil {
			minX = *chart.XAxis.Min
		}
		if chart.XAxis.Max != nil {
			maxX = *chart.XAxis.Max
		}
		minX, maxX, xTicks := niceAxis(minX, maxX, 5)
		for _, val := range xTicks {
			xPos := plotLeft + (val-minX)/(maxX-minX)*plotW
			sc.Add(&scene.LineElement{X1: xPos, Y1: plotTop, X2: xPos, Y2: plotBottom, Stroke: th.Grid, StrokeWidth: 1, DashArray: "4 4"})
			sc.Add(&scene.TextElement{X: xPos, Y: plotBottom + 18, Text: formatTick(val), FontSize: 10, Fill: th.TextMuted, Anchor: scene.AnchorMiddle, Baseline: scene.BaselineMiddle})
		}
	}

	// Grid & Axes
	for _, val := range ticks {
		tVal := (val - minY) / (maxY - minY)
		yPos := plotBottom - tVal*plotH
		sc.Add(&scene.LineElement{
			X1:          plotLeft,
			Y1:          yPos,
			X2:          plotRight,
			Y2:          yPos,
			Stroke:      th.Grid,
			StrokeWidth: 1,
			DashArray:   "4 4",
		})
		sc.Add(&scene.TextElement{
			X:          plotLeft - 10,
			Y:          yPos,
			Text:       formatTick(val),
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
		renderScatterChart(sc, chart, th, plotLeft, plotTop, plotW, plotH, plotBottom, minY, maxY, minX, maxX)
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

func niceAxis(minValue, maxValue float64, targetIntervals int) (float64, float64, []float64) {
	if targetIntervals < 1 {
		targetIntervals = 1
	}
	if maxValue <= minValue {
		span := math.Abs(minValue)
		if span == 0 {
			span = 1
		}
		minValue -= span / 2
		maxValue += span / 2
	}
	span := maxValue - minValue
	rawStep := span / float64(targetIntervals)
	power := math.Pow(10, math.Floor(math.Log10(rawStep)))
	unit := rawStep / power
	factor := 1.0
	switch {
	case unit > 5:
		factor = 10
	case unit > 2:
		factor = 5
	case unit > 1:
		factor = 2
	}
	step := factor * power
	axisMin := math.Floor(minValue/step) * step
	axisMax := math.Ceil(maxValue/step) * step
	count := int(math.Round((axisMax - axisMin) / step))
	ticks := make([]float64, 0, count+1)
	for i := 0; i <= count; i++ {
		ticks = append(ticks, axisMin+float64(i)*step)
	}
	return axisMin, axisMax, ticks
}

func formatTick(value float64) string {
	if math.Abs(value) < 1e-9 {
		value = 0
	}
	return fmt.Sprintf("%g", value)
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

	baselineY := plotBottom - ((0 - minY) / (maxY - minY) * plotH)
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
			valueY := plotBottom - normY*plotH
			barY := math.Min(valueY, baselineY)
			barH := math.Abs(valueY - baselineY)
			barX := plotLeft + float64(catIdx)*catWidth + barGroupPadding + float64(sIdx)*barWidth

			sc.Add(&scene.RectElement{
				X:      barX,
				Y:      barY,
				Width:  barWidth - 2,
				Height: barH,
				Rx:     3,
				Ry:     3,
				Fill:   color,
			})

			label := pt.Label
			if catIdx < len(chart.XAxis.Labels) {
				label = chart.XAxis.Labels[catIdx]
			}
			if label != "" && sIdx == 0 {
				sc.Add(&scene.TextElement{
					X:          catCenterX,
					Y:          plotBottom + 18,
					Text:       label,
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

			label := pt.Label
			if i < len(chart.XAxis.Labels) {
				label = chart.XAxis.Labels[i]
			}
			if label != "" {
				sc.Add(&scene.TextElement{
					X:          px,
					Y:          plotBottom + 18,
					Text:       label,
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
	minY, maxY, minX, maxX float64,
) {
	for sIdx, s := range chart.Series {
		color := s.Color
		if color == "" {
			color = th.Palette[sIdx%len(th.Palette)]
		}
		for _, pt := range s.Data {
			px := plotLeft + ((pt.X-minX)/(maxX-minX))*plotW
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
