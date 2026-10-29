package main

import (
	"encoding/json"
	"math"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

// chartFMAAmbiguous mirrors only the axis tick arithmetic to withhold inputs
// whose visible tick strings depend on Go's architecture-specific fusion.
// Expected SVG is always obtained from the production builder/emitter.
func chartFMAAmbiguous(d *ir.Diagram) bool {
	if d.Chart == nil {
		return false
	}
	c := d.Chart
	check := func(lo, hi float64) bool {
		if hi <= lo {
			span := math.Abs(lo)
			if span == 0 {
				span = 1
			}
			lo -= span / 2
			hi += span / 2
		}
		raw := (hi - lo) / 5
		power := math.Pow(10, math.Floor(math.Log10(raw)))
		unit := raw / power
		factor := 1.0
		if unit > 5 {
			factor = 10
		} else if unit > 2 {
			factor = 5
		} else if unit > 1 {
			factor = 2
		}
		step := factor * power
		a := math.Floor(lo/step) * step
		b := math.Ceil(hi/step) * step
		for i := 0; i <= int(math.Round((b-a)/step)); i++ {
			product := float64(float64(i) * step)
			if a+product != math.FMA(float64(i), step, a) {
				return true
			}
		}
		return false
	}
	lo, hi := 0.0, 1.0
	first := true
	for _, s := range c.Series {
		for _, p := range s.Data {
			if first {
				lo, hi = p.Y, p.Y
				first = false
			} else {
				lo = math.Min(lo, p.Y)
				hi = math.Max(hi, p.Y)
			}
		}
	}
	if c.YAxis.Min != nil {
		lo = *c.YAxis.Min
	} else if lo > 0 {
		lo = 0
	}
	if c.YAxis.Max != nil {
		hi = *c.YAxis.Max
	}
	if check(lo, hi) {
		return true
	}
	if c.Type == ir.ChartScatter {
		lo, hi = 0, 1
		first = true
		for _, s := range c.Series {
			for _, p := range s.Data {
				if first {
					lo, hi = p.X, p.X
					first = false
				} else {
					lo = math.Min(lo, p.X)
					hi = math.Max(hi, p.X)
				}
			}
		}
		if c.XAxis.Min != nil {
			lo = *c.XAxis.Min
		}
		if c.XAxis.Max != nil {
			hi = *c.XAxis.Max
		}
		return check(lo, hi)
	}
	return false
}

func chartInputs() []string {
	var inputs []string
	for _, kind := range []ir.ChartType{ir.ChartBar, ir.ChartLine, ir.ChartScatter, ir.ChartPie, ir.ChartDonut, ir.ChartArea} {
		for _, theme := range []string{"symaira-dark", "symaira-light"} {
			for _, legend := range []bool{false, true} {
				for _, empty := range []bool{false, true} {
					min, max := -100.0, 100.0
					d := ir.Diagram{Kind: ir.KindChart, Theme: theme, Width: 640, Height: 400, Title: "Fallback <&>", Chart: &ir.ChartSpec{Type: kind, Title: "Chart <&>", Legend: legend, YAxis: ir.AxisSpec{Min: &min, Max: &max}, XAxis: ir.AxisSpec{Labels: []string{"override", ""}, Min: &min, Max: &max}, Series: []ir.Series{{Name: "first", Color: "red", Data: []ir.DataPoint{{X: -25, Y: -30, Label: "a"}, {X: 25, Y: 70, Label: "b", Color: "blue"}, {X: 75, Y: 100, Label: "c"}}}, {Name: "second", Data: []ir.DataPoint{{X: 0, Y: 20, Label: "d"}}}}}}
					if empty {
						d.Chart.Series = []ir.Series{{Name: "empty"}}
						d.Chart.Title = ""
					}
					b, err := json.Marshal(d)
					if err != nil {
						panic(err)
					}
					inputs = append(inputs, string(b))
				}
			}
		}
	}
	return inputs
}
