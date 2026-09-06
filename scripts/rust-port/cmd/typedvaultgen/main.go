// Command typedvaultgen freezes typed base and dataset read contracts.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

type typedCase struct {
	Name   string      `json:"name"`
	Path   string      `json:"path"`
	Input  string      `json:"input"`
	OK     bool        `json:"ok"`
	Output interface{} `json:"output,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type fixture struct {
	SchemaVersion int         `json:"schema_version"`
	BaseCases     []typedCase `json:"base_cases"`
	DatasetCases  []typedCase `json:"dataset_cases"`
}

func main() {
	check := flag.Bool("check", false, "compare with the committed fixture")
	output := flag.String("output", "testdata/port/vault/typed.json", "fixture path")
	flag.Parse()
	data, err := buildFixture()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil || !bytes.Equal(current, data) {
			fmt.Fprintln(os.Stderr, "typed vault fixture is stale; run make port-fixtures-generate")
			os.Exit(1)
		}
		fmt.Println("PASS typed vault fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		panic(err)
	}
	if err := os.WriteFile(*output, data, 0o600); err != nil {
		panic(err)
	}
	fmt.Println("PASS typed vault fixture generated")
}

func buildFixture() ([]byte, error) {
	baseFull := &dbviews.Base{
		ID: "kunden-uebersicht", Title: "Kunden Übersicht", Created: "2026-09-06T12:00:00Z",
		Description: "Unicode ✓", Tags: []string{"base", "geschäft"},
		Properties: map[string]dbviews.PropertyConfig{
			"status": {Type: "status", Label: "Status", Options: []string{"Neu", "Erledigt"}, Default: "Neu"},
		},
		Views: []dbviews.View{{
			ID: "offen", Name: "Offene Fälle", Type: "table", Source: "tag:kunde",
			Filters:     []dbviews.Filter{{Key: "status", Operator: "equals", Value: "Neu"}},
			FilterGroup: &dbviews.FilterGroup{Operator: "all", Filters: []dbviews.Filter{{Key: "person", Operator: "contains", Value: "Jörg"}}},
			Sorts:       []dbviews.Sort{{Key: "created", Ascending: false}}, Columns: []string{"title", "status"},
			Computed: map[string]dbviews.ComputedColumn{"summary": {Formula: "title + status"}},
			Template: &dbviews.Template{Ref: "templates/kunde.md", Defaults: map[string]string{"status": "Neu"}},
		}},
		Extras: map[string]interface{}{"future_field": "preserved"},
	}
	baseMinimal := &dbviews.Base{ID: "empty", Title: "Empty", Created: "2026-09-06", Views: nil}
	fullBaseBytes, err := dbviews.RenderBase(baseFull)
	if err != nil {
		return nil, err
	}
	minimalBaseBytes, err := dbviews.RenderBase(baseMinimal)
	if err != nil {
		return nil, err
	}
	baseInputs := []struct{ name, path, input string }{
		{"full", "bases/kunden-uebersicht.md", string(fullBaseBytes)},
		{"minimal", "bases/empty.md", string(minimalBaseBytes)},
		{"legacy-id-fallback", "bases/legacy.md", "---\ntype: base\ntitle: Legacy\ncreated: 2026-01-01\nviews: []\n---\n"},
		{"not-a-base", "bases/note.md", "---\ntype: note\ntitle: No\n---\n"},
		{"malformed-yaml", "bases/bad.md", "---\ntype: base\nbase_id: [\n---\n"},
	}

	datasetFull := &dataset.Handle{
		Path: "datasets/orders.md", Slug: "orders", Title: "Bestellungen ✓", Created: "2026-09-06T12:00:00Z",
		Source: "datasets/orders/source.csv",
		Schema: map[string]dbviews.PropertyConfig{
			"id":     {Type: "number", Label: "ID"},
			"status": {Type: "select", Options: []string{"offen", "erledigt"}},
		},
		Coverage:      dataset.Coverage{From: "2026-01-01", To: "2026-09-06"},
		Provenance:    dataset.Provenance{ImportedAt: "2026-09-06T12:00:00Z", SourceName: "orders.csv", SourceSHA256: strings.Repeat("a", 64)},
		IdentityField: "id", RefreshCommand: "symdesk dataset refresh orders", Sensitivity: "internal", RetentionRule: "default",
	}
	fullDatasetBytes, err := datasetFull.Render()
	if err != nil {
		return nil, err
	}
	legacyDataset := strings.ReplaceAll(string(fullDatasetBytes), "sensitivity: internal\n", "")
	legacyDataset = strings.ReplaceAll(legacyDataset, "retention_rule: default\n", "")
	datasetInputs := []struct{ name, path, input string }{
		{"full", "datasets/orders.md", string(fullDatasetBytes)},
		{"legacy-policy-defaults", "datasets/orders.md", legacyDataset},
		{"wrong-type", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "type: dataset", "type: note", 1)},
		{"missing-source", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "source: datasets/orders/source.csv", "source: \"\"", 1)},
		{"missing-title", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "title: Bestellungen ✓", "title: \"\"", 1)},
		{"missing-id", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "dataset_id: orders", "dataset_id: \"\"", 1)},
		{"partial-policy", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "retention_rule: default\n", "", 1)},
		{"invalid-sensitivity", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "sensitivity: internal", "sensitivity: secret", 1)},
		{"invalid-retention", "datasets/orders.md", strings.Replace(string(fullDatasetBytes), "retention_rule: default", "retention_rule: bad/rule", 1)},
		{"no-frontmatter", "datasets/orders.md", "not frontmatter\n"},
	}

	result := fixture{SchemaVersion: 1, BaseCases: make([]typedCase, 0, len(baseInputs)), DatasetCases: make([]typedCase, 0, len(datasetInputs))}
	for _, item := range baseInputs {
		parsed, parseErr := dbviews.ParseBase(item.path, []byte(item.input))
		result.BaseCases = append(result.BaseCases, makeCase(item.name, item.path, item.input, parsed, parseErr))
	}
	for _, item := range datasetInputs {
		parsed, parseErr := dataset.ParseHandle(item.path, []byte(item.input))
		result.DatasetCases = append(result.DatasetCases, makeCase(item.name, item.path, item.input, parsed, parseErr))
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func makeCase(name, path, input string, output interface{}, err error) typedCase {
	if err != nil {
		return typedCase{Name: name, Path: path, Input: input, OK: false, Error: err.Error()}
	}
	return typedCase{Name: name, Path: path, Input: input, OK: true, Output: output}
}
