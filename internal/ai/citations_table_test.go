package ai

import "testing"

func TestIsCitationColumn(t *testing.T) {
	cases := []struct {
		cell string
		want bool
	}{
		{"Gesprächspartner", true},
		{"gespraechspartner", true},
		{"Interview", true},
		{"Quelle", true},
		{"source file", true},
		{"Verfasser", true},
		{"Author", true},
		{"Sprecher", true},
		{"Befragter", true},
		{"Zitat", true},
		{"Citation", true},
		{"Datum", false},
		{"Thema", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isCitationColumn(tc.cell); got != tc.want {
			t.Errorf("isCitationColumn(%q) = %v, want %v", tc.cell, got, tc.want)
		}
	}
}

func TestTableCells(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"| a | b |", []string{"a", "b"}},
		{"| a | b | c |", []string{"a", "b", "c"}},
		{"a | b", []string{"a", "b"}},
		{"|  a  |  b  |", []string{"a", "b"}},
		{"no pipes here", []string{"no pipes here"}},
		{"", []string{""}},
	}
	for _, tc := range cases {
		got := tableCells(tc.line)
		if len(got) != len(tc.want) {
			t.Errorf("tableCells(%q) = %q, want %q", tc.line, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("tableCells(%q)[%d] = %q, want %q", tc.line, i, got[i], tc.want[i])
			}
		}
	}
}

func TestIsTableSeparator(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{

		{"| --- | :-: |", true},
		{"|---|---|", true},
		{"- | -", true},
		{"| --- | text |", false},
		{"| a | b |", false},
		{"| |", true}, // two empty cells — a degenerate but valid separator row
	}
	for _, tc := range cases {
		if got := isTableSeparator(tc.line); got != tc.want {
			t.Errorf("isTableSeparator(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}
