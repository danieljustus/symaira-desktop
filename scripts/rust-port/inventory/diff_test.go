package inventory

import (
	"fmt"
	"strings"
	"testing"
)

func TestFirstDifferenceNamesTheOffset(t *testing.T) {
	recorded := []byte(`{"data_home": "/fixture/data"}`)
	checked := []byte(`{"data_home": "\fixture\data"}`)
	report := FirstDifference(recorded, checked)
	if !strings.Contains(report, "byte 15") {
		t.Fatalf("FirstDifference() = %q, want the offset of the first differing byte", report)
	}
	if !strings.Contains(report, "recorded=") || !strings.Contains(report, "checked=") {
		t.Fatalf("FirstDifference() = %q, want both windows in the report", report)
	}
	if got := FirstDifference(recorded, recorded); !strings.Contains(got, fmt.Sprintf("byte %d", len(recorded))) {
		t.Fatalf("FirstDifference() = %q, want the shorter length for identical contents", got)
	}
}
