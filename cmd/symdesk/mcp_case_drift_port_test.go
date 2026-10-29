package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

// Diagnostics only: the caller still compares the entire original byte slices.
// Compare raw case spans by ID so one changed length cannot hide later drift.
func mcpSurfaceCaseDifferences(recorded, checked []byte, limit int) []string {
	decode := func(data []byte) ([]string, map[string]json.RawMessage) {
		var fixture struct {
			Cases []json.RawMessage `json:"cases"`
		}
		order := []string{}
		cases := map[string]json.RawMessage{}
		if json.Unmarshal(data, &fixture) != nil {
			return order, cases
		}
		for _, raw := range fixture.Cases {
			var c struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &c) == nil {
				order = append(order, c.ID)
				cases[c.ID] = raw
			}
		}
		return order, cases
	}
	order, left := decode(recorded)
	rightOrder, right := decode(checked)
	for _, id := range rightOrder {
		if _, ok := left[id]; !ok {
			order = append(order, id)
		}
	}
	differences := []string{}
	total := 0
	for _, id := range order {
		if bytes.Equal(left[id], right[id]) {
			continue
		}
		total++
		if len(differences) < limit {
			differences = append(differences, fmt.Sprintf("case %q: %s", id, inventory.FirstDifference(left[id], right[id])))
		}
	}
	if total > limit {
		differences = append(differences, fmt.Sprintf("%d additional differing cases omitted", total-limit))
	}
	return differences
}

func TestMCPSurfaceCaseDifferences(t *testing.T) {
	recorded := []byte(`{"cases":[{"id":"first","stdout":"one"},{"id":"second","stdout":"two"}]}`)
	checked := []byte(`{"cases":[{"id":"first","stdout":"longer"},{"id":"second","stdout":"wrong"},{"id":"added"}]}`)
	differences := mcpSurfaceCaseDifferences(recorded, checked, 50)
	if len(differences) != 3 || !strings.Contains(differences[0], `case "first"`) || !strings.Contains(differences[1], `case "second"`) || !strings.Contains(differences[2], `case "added"`) {
		t.Fatalf("missing case diagnostics: %v", differences)
	}
	if got := mcpSurfaceCaseDifferences(recorded, checked, 1); len(got) != 2 || !strings.Contains(got[1], "2 additional") {
		t.Fatalf("unbounded diagnostics: %v", got)
	}
	if got := mcpSurfaceCaseDifferences(recorded, recorded, 50); len(got) != 0 {
		t.Fatalf("equal cases: %v", got)
	}
}
