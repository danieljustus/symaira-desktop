package barcode

import (
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions("PATCHT", "INV-001")
	if opts.SeparatorPrefix != "PATCHT" {
		t.Errorf("SeparatorPrefix = %q, want PATCHT", opts.SeparatorPrefix)
	}
	if opts.SeparatorValue != "INV-001" {
		t.Errorf("SeparatorValue = %q, want INV-001", opts.SeparatorValue)
	}
	if opts.PDFToPPM != "pdftoppm" {
		t.Errorf("PDFToPPM = %q", opts.PDFToPPM)
	}
	if opts.ZBarImg != "zbarimg" {
		t.Errorf("ZBarImg = %q", opts.ZBarImg)
	}
	if opts.PDFInfo != "pdfinfo" {
		t.Errorf("PDFInfo = %q", opts.PDFInfo)
	}
	if opts.PDFSeparate != "pdfseparate" {
		t.Errorf("PDFSeparate = %q", opts.PDFSeparate)
	}
	if opts.PDFUnite != "pdfunite" {
		t.Errorf("PDFUnite = %q", opts.PDFUnite)
	}
}

func TestMatches_ExactValue(t *testing.T) {
	opts := Options{SeparatorValue: "INV-001"}
	if !opts.matches("INV-001") {
		t.Error("expected exact match")
	}
	if opts.matches("INV-002") {
		t.Error("should not match different value")
	}
	if opts.matches("INV-001-extra") {
		t.Error("should not match value with extra chars")
	}
}

func TestMatches_Prefix(t *testing.T) {
	opts := Options{SeparatorPrefix: "PATCHT"}
	if !opts.matches("PATCHT-001") {
		t.Error("expected prefix match")
	}
	if !opts.matches("PATCHT") {
		t.Error("expected exact prefix match")
	}
	if opts.matches("NOTPATCHT-001") {
		t.Error("should not match without prefix")
	}
}

func TestMatches_BothSet(t *testing.T) {
	opts := Options{SeparatorPrefix: "PATCHT", SeparatorValue: "EXACT-001"}
	if !opts.matches("EXACT-001") {
		t.Error("expected exact value match")
	}
	if !opts.matches("PATCHT-001") {
		t.Error("expected prefix match")
	}
	if opts.matches("OTHER") {
		t.Error("should not match unrelated value")
	}
}

func TestMatches_NeitherSet(t *testing.T) {
	opts := Options{}
	if opts.matches("anything") {
		t.Error("should not match when neither prefix nor value set")
	}
}

func TestEnabled(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		value  string
		want   bool
	}{
		{"neither", "", "", false},
		{"prefix only", "PATCHT", "", true},
		{"value only", "", "INV-001", true},
		{"both", "PATCHT", "INV-001", true},
		{"whitespace prefix", "  ", "", false},
		{"whitespace value", "", "  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{SeparatorPrefix: tc.prefix, SeparatorValue: tc.value}
			if got := opts.Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSeparate_DisabledReturnsEmpty(t *testing.T) {
	opts := Options{}
	result, err := opts.Separate(nil, "/tmp/any.pdf", "/tmp/out")
	if err != nil {
		t.Fatal(err)
	}
	if result.Paths != nil {
		t.Errorf("expected nil paths, got %v", result.Paths)
	}
}

func TestUniqueSorted(t *testing.T) {
	cases := []struct {
		input []int
		want  []int
	}{
		{nil, nil},
		{[]int{3, 1, 2}, []int{1, 2, 3}},
		{[]int{1, 1, 2, 2}, []int{1, 2}},
		{[]int{5}, []int{5}},
	}
	for _, tc := range cases {
		got := uniqueSorted(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("uniqueSorted(%v) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("uniqueSorted(%v)[%d] = %d, want %d", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}
