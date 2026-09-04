package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestDatasetQueryOptionHelpersPreserveNestedFiltersAndColumns(t *testing.T) {
	handle := &DatasetDescription{DatasetSummary: DatasetSummary{Columns: map[string]dbviews.PropertyConfig{
		"name": {Type: "text"}, "amount": {Type: "number"}, "untagged": {},
	}}}
	opts := DatasetQueryOptions{
		Columns:     []string{" amount ", "name", "amount", ""},
		Filters:     []dbviews.Filter{{Key: "amount", Operator: "greater_than", Value: "1"}},
		FilterGroup: &dbviews.FilterGroup{Operator: "all", Filters: []dbviews.Filter{{Key: "name", Value: "x"}}, Groups: []dbviews.FilterGroup{{Operator: "any", Filters: []dbviews.Filter{{Key: "amount", Value: "2"}}}}},
		Sorts:       []dbviews.Sort{{Key: "amount", Ascending: false}},
		Aggregates:  []DatasetAggregate{{Column: "amount", Function: "sum", As: "total"}},
		Limit:       5,
	}
	converted, err := datasetSidecarQueryOptions(handle, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted.Columns) != 2 || converted.Columns[0] != "amount" || converted.Columns[1] != "name" || converted.Schema["untagged"] != "text" {
		t.Fatalf("unexpected converted columns/schema: %#v %#v", converted.Columns, converted.Schema)
	}
	if len(converted.Filters) != 1 || converted.Filters[0].Key != "amount" || converted.FilterGroup == nil || len(converted.FilterGroup.Groups) != 1 || converted.FilterGroup.Groups[0].Operator != "any" || len(converted.Sorts) != 1 || len(converted.Aggregates) != 1 {
		t.Fatalf("nested query options were not preserved: %#v", converted)
	}
	allColumns, err := datasetSidecarQueryOptions(handle, DatasetQueryOptions{})
	if err != nil || len(allColumns.Columns) != 3 || allColumns.Columns[0] != "amount" || allColumns.Columns[2] != "untagged" {
		t.Fatalf("default query columns = %#v, %v", allColumns.Columns, err)
	}
	if resultLimitOffset(-1) != 0 || resultLimitOffset(3) != 3 {
		t.Fatal("negative query offsets were not normalized")
	}
}

func TestQueryColumnsFallsBackToStoredJSONAndDeduplicatesRequests(t *testing.T) {
	requested := queryColumns([]string{" b ", "a", "b", ""}, nil, nil)
	if len(requested) != 2 || requested[0] != "b" || requested[1] != "a" {
		t.Fatalf("requested columns were unexpectedly normalized: %#v", requested)
	}
	rows := []sidecar.DatasetRow{{ValuesJSON: `{"z":1,"a":2}`}}
	fallback := queryColumns(nil, nil, rows)
	if len(fallback) != 2 || fallback[0] != "a" || fallback[1] != "z" {
		t.Fatalf("stored JSON fallback columns = %#v", fallback)
	}
	if got := queryColumns(nil, map[string]dbviews.PropertyConfig{"b": {}, "a": {}}, rows); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("schema columns = %#v", got)
	}
}

func TestDatasetCoverageAndRowCountHelpersHandleMissingDatesAndDuplicates(t *testing.T) {
	rows := []dataset.Row{
		{Key: "same", Values: map[string]interface{}{"when": "2026-02-03"}},
		{Key: "same", Values: map[string]interface{}{"when": "2026-01-01"}},
		{Key: "other", Values: map[string]interface{}{"when": ""}},
	}
	if uniqueDatasetRowCount(rows) != 2 {
		t.Fatal("duplicate dataset keys were counted as separate rows")
	}
	coverage := coverageForRows(rows, map[string]dbviews.PropertyConfig{"when": {Type: "date"}})
	if coverage.From != "2026-01-01" || coverage.To != "2026-02-03" {
		t.Fatalf("coverage = %#v", coverage)
	}
	if coverageForRows(rows, map[string]dbviews.PropertyConfig{"when": {Type: "text"}}) != (dataset.Coverage{}) || coverageForRows(rows, map[string]dbviews.PropertyConfig{"when": {Type: "date"}}) == (dataset.Coverage{}) {
		t.Fatal("coverage helper mishandled absent/date columns")
	}
}

func TestDatasetListDescribeAndExportValidationErrors(t *testing.T) {
	var nilService *Service
	if _, err := nilService.DatasetList(); err == nil || !strings.Contains(err.Error(), "requires a vault") {
		t.Fatalf("nil DatasetList error = %v", err)
	}
	if _, err := nilService.DatasetDescribe("events"); err == nil || !strings.Contains(err.Error(), "requires a vault") {
		t.Fatalf("nil DatasetDescribe error = %v", err)
	}
	if _, err := nilService.DatasetQuery("events", DatasetQueryOptions{}); err == nil || !strings.Contains(err.Error(), "requires a sidecar") {
		t.Fatalf("nil DatasetQuery error = %v", err)
	}
	if err := nilService.RebuildDatasets(); err == nil || !strings.Contains(err.Error(), "requires a sidecar") {
		t.Fatalf("nil RebuildDatasets error = %v", err)
	}
	if err := (&Service{VaultRoot: t.TempDir()}).RebuildDatasets(); err == nil || !strings.Contains(err.Error(), "requires a sidecar") {
		t.Fatalf("missing DB RebuildDatasets error = %v", err)
	}

	svc := newTestService(t)
	if _, err := svc.DatasetDescribe(" "); err == nil || !strings.Contains(err.Error(), "slug is required") {
		t.Fatalf("blank DatasetDescribe error = %v", err)
	}
	if _, err := svc.DatasetDescribe("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing DatasetDescribe error = %v", err)
	}
	if _, err := svc.Export("", "", filepath.Join(t.TempDir(), "out"), "pdf", ""); err == nil || !strings.Contains(err.Error(), "requires --note or --view") {
		t.Fatalf("empty Export error = %v", err)
	}
	if _, err := svc.Export("note.md", "", "", "xml", ""); err == nil || !strings.Contains(err.Error(), "unsupported export format") {
		t.Fatalf("unsupported Export error = %v", err)
	}
	if _, err := svc.Export("note.md", "", "", "csv", ""); err == nil || !strings.Contains(err.Error(), "only supported for views") {
		t.Fatalf("note CSV Export error = %v", err)
	}
	if defaultOutputPath("notes/one.md", "", "pdf") != "one.pdf" || defaultOutputPath("", "orders", "csv") != "orders.csv" || defaultOutputPath("", "", "html") != "export.html" {
		t.Fatal("default export paths changed")
	}
}

func TestDatasetListReadsAuthoritativeHandlesAndSkipsMalformedFiles(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.DatasetSync(validDatasetSyncOptions()); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(svc.VaultRoot, "datasets", "bad.md")
	if err := os.WriteFile(badPath, []byte("---\ntype: note\ntitle: Bad\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := svc.DatasetsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Slug != "events" || list[0].Rows != 1 {
		t.Fatalf("authoritative dataset list = %#v", list)
	}
	description, err := svc.DatasetDescribe("events")
	if err != nil || description.Rows != 1 || description.Title != "Events" {
		t.Fatalf("dataset description = %#v, %v", description, err)
	}
}

func TestReplaceDatasetRowsSortsAndSerializesMaterializedValues(t *testing.T) {
	svc := newTestService(t)
	rows := []dataset.Row{
		{Key: "z", Identity: "z", Values: map[string]interface{}{"value": 2}, SourcePath: "datasets/x/z.csv", RowNumber: 2},
		{Key: "a", Identity: "a", Values: map[string]interface{}{"value": 1}, SourcePath: "datasets/x/a.csv", RowNumber: 2},
		{Key: "a", Identity: "duplicate", Values: map[string]interface{}{"value": 3}},
	}
	if err := svc.replaceDatasetRows("x", rows); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.DB.DatasetRows("x")
	if err != nil || len(stored) != 2 || stored[0].RowKey != "a" || stored[1].RowKey != "z" {
		t.Fatalf("stored rows = %#v, %v", stored, err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(stored[0].ValuesJSON), &values); err != nil || values["value"] != float64(3) {
		t.Fatalf("serialized replacement row = %#v, %v", values, err)
	}
}
