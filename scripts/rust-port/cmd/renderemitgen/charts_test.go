package main

import (
	"encoding/json"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"testing"
)

func TestChartFMAGuard(t *testing.T) {
	d := &ir.Diagram{Chart: &ir.ChartSpec{Type: ir.ChartPie, Series: []ir.Series{{Data: []ir.DataPoint{{Y: 0}}}}}}
	if !chartFMAAmbiguous(d) {
		t.Fatal("zero-span tick sequence must be withheld")
	}
	for _, input := range chartInputs() {
		var d ir.Diagram
		if err := json.Unmarshal([]byte(input), &d); err != nil {
			t.Fatal(err)
		}
		if chartFMAAmbiguous(&d) {
			t.Fatal("exact-axis chart unexpectedly withheld")
		}
	}
}
