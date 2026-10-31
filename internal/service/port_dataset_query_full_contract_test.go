package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

// The full dataset-query contract (DATA-002): every filter operator, ordering,
// grouping, aggregates, paging and dataset-sourced view projection, recorded
// from the real Go sidecar/service over hand-built rows whose stored JSON
// covers missing, null, mixed-type, array and non-ASCII values.
//
// Regenerate with:
// PORT_GENERATE=1 go test ./internal/service -run '^TestPortDatasetQueryFullContract$'

const (
	portDatasetQueryFullFixture    = "../../testdata/port/dataset/query-full.json"
	portDatasetQueryFullCLIFixture = "../../testdata/port/dataset/cli-query-full.json"
)

type portQFOptions struct {
	Columns     []string             `json:"columns,omitempty"`
	Filters     []dbviews.Filter     `json:"filters,omitempty"`
	FilterGroup *dbviews.FilterGroup `json:"filter_group,omitempty"`
	Sorts       []dbviews.Sort       `json:"sorts,omitempty"`
	GroupBy     string               `json:"group_by,omitempty"`
	Aggregates  []DatasetAggregate   `json:"aggregates,omitempty"`
	Limit       int                  `json:"limit,omitempty"`
}

type portQFView struct {
	Source      string                            `json:"source"`
	Filters     []dbviews.Filter                  `json:"filters,omitempty"`
	FilterGroup *dbviews.FilterGroup              `json:"filter_group,omitempty"`
	Sorts       []dbviews.Sort                    `json:"sorts,omitempty"`
	GroupBy     string                            `json:"group_by,omitempty"`
	Computed    map[string]dbviews.ComputedColumn `json:"computed,omitempty"`
}

type portQFCase struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Slug    string         `json:"slug"`
	Options *portQFOptions `json:"options,omitempty"`
	Offset  int            `json:"offset,omitempty"`
	View    *portQFView    `json:"view,omitempty"`
	Limit   int            `json:"limit,omitempty"`
	Error   string         `json:"error"`
	Result  any            `json:"result"`
}

type portQFRow struct {
	Key      string `json:"row_key"`
	Identity string `json:"identity"`
	Values   string `json:"values_json"`
	Path     string `json:"source_path"`
	Number   int    `json:"row_number"`
}

func portQFRows() []sidecar.DatasetRow {
	raw := []portQFRow{
		{"identity:r1", "r1", `{"id":"r1","status":"open","amount":10,"qty":3,"ratio":0.5,"due":"2026-01-15","seen":"2026-01-15T10:00:00Z","done":true,"tags":["red","Blue"],"note":"Alpha_beta","name":"ÄPFEL"}`, "datasets/orders.csv", 2},
		{"identity:r2", "r2", `{"id":"r2","status":"paid","amount":20.5,"qty":7,"ratio":1.25,"due":"2026-02-01","seen":"2026-02-01T08:30:00+01:00","done":false,"tags":"red","note":"100% sure","name":"äpfel"}`, "datasets/orders.csv", 3},
		{"identity:r3", "r3", `{"id":"r3","status":null,"amount":"30","qty":"x","ratio":2,"due":"not-a-date","done":"yes","tags":[],"note":"","name":"İstanbul"}`, "datasets/orders.csv", 4},
		{"identity:r4", "r4", `{"id":"r4","amount":-5,"qty":0,"due":"2025-12-31","done":1,"tags":["blue","green"],"note":"line\nbreak","name":"Zed"}`, "datasets/orders.csv", 5},
		{"identity:r5", "r5", `{"id":"r5","status":"OPEN","amount":20.5,"qty":7,"ratio":1000,"due":"2026-01-15","seen":"2026-01-15T09:00:00Z","done":"true","tags":"red,green","note":"x","name":"zed"}`, "datasets/orders.csv", 6},
		{"key:6", "", `{"id":"r6","status":"closed","amount":null,"tags":{"a":1},"name":"mid dle"}`, "datasets/orders.csv", 7},
		{"identity:r7", "r7", `{"id":"r7","status":"Paid","amount":9007199254740993,"qty":2,"ratio":0.1,"due":"2026-03-05","done":false,"tags":["Red"],"note":"%","name":"a_b"}`, "datasets/extra.csv", 2},
		{"identity:r8", "r8", `{"id":"r8","status":"open","amount":1234567.5,"qty":123456,"ratio":0.00001,"due":"2026-04-01","done":true,"tags":["red"],"note":"floats","name":"fl"}`, "datasets/extra.csv", 3},
	}
	rows := make([]sidecar.DatasetRow, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, sidecar.DatasetRow{DatasetSlug: "orders", RowKey: r.Key, Identity: r.Identity, ValuesJSON: r.Values, SourcePath: r.Path, RowNumber: r.Number})
	}
	return rows
}

func portQFSchema() map[string]dbviews.PropertyConfig {
	return map[string]dbviews.PropertyConfig{
		"id": {Type: "text"}, "status": {Type: "text"}, "amount": {Type: "number"}, "qty": {Type: "integer"},
		"ratio": {Type: "float"}, "due": {Type: "date"}, "seen": {Type: "datetime"}, "done": {Type: "bool"},
		"tags": {Type: "multiselect"}, "note": {}, "name": {Type: "text"},
	}
}

func f(key, op, value string) dbviews.Filter {
	return dbviews.Filter{Key: key, Operator: op, Value: value}
}

func portQFCases() []portQFCase {
	svc := func(name string, o portQFOptions) portQFCase {
		return portQFCase{Name: name, Kind: "service", Slug: "orders", Options: &o}
	}
	var cases []portQFCase
	add := func(c portQFCase) { cases = append(cases, c) }
	add(svc("defaults", portQFOptions{}))
	add(svc("limit-and-cap", portQFOptions{Limit: 2, Columns: []string{"id", "status"}}))
	add(svc("limit-above-maximum", portQFOptions{Limit: 5000, Columns: []string{"id"}}))
	add(svc("negative-limit", portQFOptions{Limit: -3, Columns: []string{"id"}}))
	add(svc("duplicate-and-blank-columns", portQFOptions{Columns: []string{" id ", "id", "", "status"}}))
	add(svc("all-blank-columns", portQFOptions{Columns: []string{" ", ""}}))
	add(svc("pseudo-columns", portQFOptions{Columns: []string{"_key", "identity", "_identity", "id"}}))
	add(svc("unknown-column", portQFOptions{Columns: []string{"absent"}}))
	add(svc("unknown-filter-column", portQFOptions{Filters: []dbviews.Filter{f("absent", "equals", "x")}}))
	add(svc("unknown-filter-column-padded", portQFOptions{Filters: []dbviews.Filter{f(" status", "equals", "x")}}))
	add(svc("unknown-group-column", portQFOptions{GroupBy: "absent"}))
	add(svc("unknown-sort-column", portQFOptions{Sorts: []dbviews.Sort{{Key: "absent", Ascending: true}}}))
	add(svc("unknown-aggregate-function", portQFOptions{Aggregates: []DatasetAggregate{{Function: "median", Column: "amount"}}}))
	add(svc("unknown-aggregate-column", portQFOptions{Aggregates: []DatasetAggregate{{Function: "sum", Column: "absent"}}}))
	add(svc("unknown-group-filter-column", portQFOptions{FilterGroup: &dbviews.FilterGroup{Operator: "all", Filters: []dbviews.Filter{f("absent", "equals", "x")}}}))
	for _, op := range []string{"equals", "is", "=", "==", ""} {
		add(svc("text-"+op+"-case-insensitive", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", op, "OPEN")}}))
	}
	add(svc("text-equals-empty-value", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", "equals", "  ")}}))
	add(svc("text-equals-non-ascii-fold", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("name", "equals", "Äpfel")}}))
	add(svc("text-equals-turkish-dotted-i", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("name", "equals", "İstanbul")}}))
	for _, op := range []string{"not_equals", "is_not", "!="} {
		add(svc("text-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", op, "open")}}))
	}
	for _, op := range []string{"is_empty", "empty", "is_not_empty", "not_empty"} {
		add(svc("text-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", op, "")}}))
	}
	for _, op := range []string{"contains", "not_contains", "starts_with", "prefix", "ends_with", "suffix"} {
		add(svc("text-"+op+"-wildcards", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("note", op, "_")}}))
		add(svc("text-"+op+"-percent", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("note", op, "%")}}))
		add(svc("text-"+op+"-plain", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", op, "Pa")}}))
	}
	for _, op := range []string{"equals", "not_equals", "greater_than", "gt", ">", "greater_than_or_equal", "gte", ">=", "less_than", "lt", "<", "less_than_or_equal", "lte", "<="} {
		add(svc("number-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("amount", op, "20.5")}}))
		add(svc("integer-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("qty", op, "3")}}))
		add(svc("text-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", op, "open")}}))
		add(svc("date-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("due", op, "2026-01-15")}}))
		add(svc("datetime-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("seen", op, "2026-01-15T10:00:00Z")}}))
	}
	add(svc("number-non-numeric-operand", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("amount", "greater_than", "abc")}}))
	add(svc("date-non-date-operand", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("due", "after", "garbage")}}))
	for _, op := range []string{"after", "before", "on_or_after", "on_or_before"} {
		add(svc("date-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("due", op, "2026-01-15")}}))
		add(svc("text-column-"+op, portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("seen", op, "2026-01-15")}}))
	}
	add(svc("unknown-operator-falls-back-to-equals", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", "matches", "paid")}}))
	for _, op := range []string{"in", "not_in", "contains_any", "contains_none", "contains_all"} {
		add(svc("set-"+op+"-text", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("tags", op, "red, BLUE")}}))
		add(svc("set-"+op+"-bracketed", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("tags", op, "[red green]")}}))
		add(svc("set-"+op+"-empty", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("tags", op, " [] ")}}))
		add(svc("set-"+op+"-status", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", op, "open,paid")}}))
		add(svc("set-"+op+"-number", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("amount", op, "20.5, -5")}}))
		add(svc("set-"+op+"-bool", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("done", op, "TRUE false")}}))
		add(svc("set-"+op+"-identity", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("identity", op, "r1,r2")}}))
		add(svc("set-"+op+"-key", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("_key", op, "key:6 identity:r1")}}))
	}
	add(svc("identity-and-key-filters", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("identity", "starts_with", "R"), f("_key", "contains", "r")}}))
	add(svc("identity-empty", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("_identity", "is_empty", "")}}))
	add(svc("combined-flat-filters", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("status", "is_not_empty", ""), f("amount", "gt", "5")}}))
	nested := dbviews.FilterGroup{Operator: "any", Filters: []dbviews.Filter{f("status", "equals", "paid")}, Groups: []dbviews.FilterGroup{{
		Operator: "all", Filters: []dbviews.Filter{f("amount", "greater_than", "20")},
		Groups: []dbviews.FilterGroup{{Operator: "any", Filters: []dbviews.Filter{f("status", "is_empty", ""), f("status", "equals", "OPEN")}}},
	}}}
	add(svc("nested-group", portQFOptions{Columns: []string{"id"}, FilterGroup: &nested}))
	add(svc("empty-group-is-true", portQFOptions{Columns: []string{"id"}, FilterGroup: &dbviews.FilterGroup{Operator: "any"}}))
	add(svc("group-operator-unknown-is-all", portQFOptions{Columns: []string{"id"}, FilterGroup: &dbviews.FilterGroup{Operator: "xor", Filters: []dbviews.Filter{f("status", "equals", "open"), f("qty", "equals", "3")}}}))
	add(svc("group-plus-flat", portQFOptions{Columns: []string{"id"}, Filters: []dbviews.Filter{f("qty", "gte", "2")}, FilterGroup: &dbviews.FilterGroup{Operator: "any", Filters: []dbviews.Filter{f("status", "equals", "paid"), f("status", "equals", "open")}}}))
	add(svc("sort-amount-desc", portQFOptions{Columns: []string{"id", "amount"}, Sorts: []dbviews.Sort{{Key: "amount"}}}))
	add(svc("sort-amount-asc", portQFOptions{Columns: []string{"id", "amount"}, Sorts: []dbviews.Sort{{Key: "amount", Ascending: true}}}))
	add(svc("sort-text-asc", portQFOptions{Columns: []string{"id", "name"}, Sorts: []dbviews.Sort{{Key: "name", Ascending: true}}}))
	add(svc("sort-date-desc-then-id", portQFOptions{Columns: []string{"id", "due"}, Sorts: []dbviews.Sort{{Key: "due"}, {Key: "id", Ascending: true}}}))
	add(svc("sort-missing-last-both-directions", portQFOptions{Columns: []string{"id", "status"}, Sorts: []dbviews.Sort{{Key: "status", Ascending: true}}}))
	add(svc("sort-pseudo", portQFOptions{Columns: []string{"id"}, Sorts: []dbviews.Sort{{Key: "_key"}}}))
	add(svc("sort-with-limit", portQFOptions{Columns: []string{"id", "qty"}, Sorts: []dbviews.Sort{{Key: "qty"}}, Limit: 3}))
	add(svc("group-default-count", portQFOptions{GroupBy: "status"}))
	add(svc("group-count-star", portQFOptions{GroupBy: "status", Aggregates: []DatasetAggregate{{Function: "count"}}}))
	add(svc("group-count-column-and-alias", portQFOptions{GroupBy: "status", Aggregates: []DatasetAggregate{{Function: "COUNT", Column: "amount"}, {Function: "count", Column: "note", As: "notes"}}}))
	add(svc("group-all-functions", portQFOptions{GroupBy: "qty", Aggregates: []DatasetAggregate{
		{Function: "sum", Column: "amount"}, {Function: "min", Column: "amount"}, {Function: "max", Column: "amount"},
		{Function: " Average ", Column: "amount", As: "avg_amount"}, {Function: "count"},
	}}))
	add(svc("group-by-multiselect-json", portQFOptions{GroupBy: "tags"}))
	add(svc("group-by-bool", portQFOptions{GroupBy: "done"}))
	add(svc("group-by-identity", portQFOptions{GroupBy: "identity"}))
	add(svc("group-by-key-with-limit", portQFOptions{GroupBy: "_key", Limit: 3}))
	add(svc("group-with-filter", portQFOptions{GroupBy: "status", Filters: []dbviews.Filter{f("amount", "gte", "10")}, Aggregates: []DatasetAggregate{{Function: "sum", Column: "amount", As: "total"}}}))
	add(svc("aggregate-only-sum", portQFOptions{Aggregates: []DatasetAggregate{{Function: "sum", Column: "amount"}}}))
	add(svc("aggregate-only-everything", portQFOptions{Aggregates: []DatasetAggregate{
		{Function: "count"}, {Function: "count", Column: "status"}, {Function: "sum", Column: "qty"},
		{Function: "average", Column: "ratio"}, {Function: "min", Column: "due"}, {Function: "max", Column: "name"},
	}}))
	add(svc("aggregate-of-nothing-matching", portQFOptions{Filters: []dbviews.Filter{f("id", "equals", "nope")}, Aggregates: []DatasetAggregate{{Function: "sum", Column: "amount"}, {Function: "count"}}}))
	add(svc("aggregate-duplicate-names", portQFOptions{Aggregates: []DatasetAggregate{{Function: "sum", Column: "amount", As: "x"}, {Function: "count", As: "x"}}}))
	add(svc("aggregate-ignores-columns", portQFOptions{Columns: []string{"id"}, GroupBy: "status", Aggregates: []DatasetAggregate{{Function: "count"}}}))

	sc := func(name string, o portQFOptions, offset int) portQFCase {
		return portQFCase{Name: name, Kind: "sidecar", Slug: "orders", Options: &o, Offset: offset}
	}
	add(sc("sidecar-offset-page", portQFOptions{Columns: []string{"id"}, Limit: 2}, 2))
	add(sc("sidecar-offset-past-end", portQFOptions{Columns: []string{"id"}, Limit: 2}, 50))
	add(sc("sidecar-negative-offset", portQFOptions{Columns: []string{"id"}, Limit: 2}, -4))
	add(sc("sidecar-offset-sorted", portQFOptions{Columns: []string{"id"}, Sorts: []dbviews.Sort{{Key: "amount", Ascending: true}}, Limit: 3}, 1))
	add(sc("sidecar-grouped-offset", portQFOptions{GroupBy: "status", Limit: 2}, 1))
	add(sc("sidecar-aggregate-offset-hides-row", portQFOptions{Aggregates: []DatasetAggregate{{Function: "count"}}}, 1))
	add(sc("sidecar-row-metadata", portQFOptions{}, 0))

	vw := func(name string, v portQFView, limit int) portQFCase {
		return portQFCase{Name: name, Kind: "view", Slug: "orders", View: &v, Limit: limit}
	}
	add(vw("view-plain", portQFView{Source: "dataset:orders"}, 0))
	add(vw("view-source-whitespace-and-limit", portQFView{Source: "  dataset:orders  "}, 3))
	add(vw("view-bare-slug", portQFView{Source: "orders"}, 3))
	add(vw("view-empty-slug", portQFView{Source: "dataset:  "}, 3))
	add(vw("view-filters-sorts", portQFView{Source: "dataset:orders", Filters: []dbviews.Filter{f("qty", "gte", "2")}, Sorts: []dbviews.Sort{{Key: "amount"}}}, 10))
	add(vw("view-group-by", portQFView{Source: "dataset:orders", GroupBy: "status"}, 10))
	add(vw("view-computed-formula-and-rollup", portQFView{Source: "dataset:orders", Filters: []dbviews.Filter{f("id", "in", "r1,r2,r7,r8")}, Computed: map[string]dbviews.ComputedColumn{
		"label":  {Formula: "{id}/{amount}"},
		"stock":  {Formula: "{status}|{done}|{tags}|{qty}"},
		"links":  {Rollup: "count"},
		"nofn":   {},
		"ratio2": {Formula: "r={ratio} missing={absent}"},
		"floats": {Formula: "{amount}|{qty}|{ratio}|{done}"},
	}}, 10))
	add(vw("view-unknown-filter-column", portQFView{Source: "dataset:orders", Filters: []dbviews.Filter{f("absent", "equals", "x")}}, 10))
	return cases
}

func TestPortDatasetQueryFullContract(t *testing.T) {
	root := t.TempDir()
	db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc := New(root, db)
	if _, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id", Schema: portQFSchema(),
		Provenance: dataset.Provenance{ImportedAt: "2026-09-25T00:00:00Z", SourceName: "fixture", SourceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Rows:       []DatasetSyncRow{{Identity: "seed", Values: map[string]interface{}{"id": "seed"}}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := portQFRows()
	if err := db.ReplaceDatasetRows("orders", rows); err != nil {
		t.Fatal(err)
	}
	description, err := svc.DatasetDescribe("orders")
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]string{}
	for column, property := range description.Columns {
		typ := property.Type
		if typ == "" {
			typ = "text"
		}
		schema[column] = typ
	}

	cases := portQFCases()
	failures, successes := 0, 0
	for i := range cases {
		c := &cases[i]
		var result any
		var runErr error
		switch c.Kind {
		case "service":
			opts := DatasetQueryOptions{Columns: c.Options.Columns, Filters: c.Options.Filters, FilterGroup: c.Options.FilterGroup, Sorts: c.Options.Sorts, GroupBy: c.Options.GroupBy, Aggregates: c.Options.Aggregates, Limit: c.Options.Limit}
			result, runErr = svc.DatasetQuery(c.Slug, opts)
		case "sidecar":
			opts := DatasetQueryOptions{Columns: c.Options.Columns, Filters: c.Options.Filters, FilterGroup: c.Options.FilterGroup, Sorts: c.Options.Sorts, GroupBy: c.Options.GroupBy, Aggregates: c.Options.Aggregates, Limit: c.Options.Limit}
			var q sidecar.DatasetQueryOptions
			q, runErr = datasetSidecarQueryOptions(description, opts)
			if runErr == nil {
				q.Offset = c.Offset
				var out *sidecar.DatasetQueryResult
				out, runErr = db.QueryDataset(c.Slug, q)
				if runErr == nil {
					type row struct {
						RowKey     string                 `json:"row_key"`
						Identity   string                 `json:"identity"`
						Values     map[string]interface{} `json:"values"`
						SourcePath string                 `json:"source_path"`
						RowNumber  int                    `json:"row_number"`
					}
					rs := make([]row, 0, len(out.Rows))
					for _, r := range out.Rows {
						rs = append(rs, row{r.RowKey, r.Identity, r.Values, r.SourcePath, r.RowNumber})
					}
					result = map[string]any{"rows": rs, "total_rows": out.TotalRows, "limit": out.Limit, "offset": out.Offset}
				}
			}
		case "view":
			view := dbviews.View{Source: c.View.Source, Filters: c.View.Filters, FilterGroup: c.View.FilterGroup, Sorts: c.View.Sorts, GroupBy: c.View.GroupBy, Computed: c.View.Computed}
			var viewRows []map[string]interface{}
			var total int
			viewRows, total, runErr = svc.executeDatasetViewWithTotal(&view, c.Limit)
			if runErr == nil {
				result = map[string]any{"rows": viewRows, "total_rows": total}
			}
		default:
			t.Fatalf("unknown kind %q", c.Kind)
		}
		if runErr != nil {
			c.Error = runErr.Error()
			failures++
		} else {
			c.Result = result
			successes++
		}
	}
	if successes < 100 || failures < 8 {
		t.Fatalf("corpus too thin: %d successes, %d failures", successes, failures)
	}

	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	hashes := map[string]string{}
	for _, rel := range []string{"internal/sidecar/dataset_query.go", "internal/service/datasets.go", "internal/service/dataset_view_exec.go", "internal/dbviews/views.go"} {
		b, err := os.ReadFile(filepath.Join(repo, rel)) //nolint:gosec // fixed repository paths
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		hashes[rel] = hex.EncodeToString(sum[:])
	}
	fixture := map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"schema":         schema,
		"rows":           portQFRowFixture(rows),
		"cases":          cases,
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	cli := portQFCLIFixture(t)
	writeOrCompare(t, portDatasetQueryFullFixture, encoded)
	writeOrCompare(t, portDatasetQueryFullCLIFixture, cli)
	fmt.Printf("dataset full query: %d cases (%d ok, %d errors)\n", len(cases), successes, failures)
}

func portQFRowFixture(rows []sidecar.DatasetRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"row_key": r.RowKey, "identity": r.Identity, "values_json": r.ValuesJSON, "source_path": r.SourcePath, "row_number": r.RowNumber})
	}
	return out
}

func writeOrCompare(t *testing.T, path string, data []byte) {
	t.Helper()
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // fixed fixture path
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatalf("read %s (generate with PORT_GENERATE=1): %v", path, err)
	}
	if !bytes.Equal(current, data) {
		t.Fatalf("%s is stale against the current Go oracle; regenerate deliberately", path)
	}
}

type portQFCLICase struct {
	ID                   string        `json:"id"`
	Stage                string        `json:"stage"`
	PrepareArgs          []string      `json:"prepare_args"`
	Args                 []string      `json:"args"`
	CompareSidecarLayout bool          `json:"compare_sidecar_layout"`
	Setup                []portQFSetup `json:"setup"`
}

type portQFSetup struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    int    `json:"mode"`
}

func portQFCLIFixture(t *testing.T) []byte {
	t.Helper()
	rows := `[{"identity":"a","values":{"id":"a","amount":10,"qty":3,"status":"open","due":"2026-01-15","tags":"red"}},` +
		`{"identity":"b","values":{"id":"b","amount":20,"qty":7,"status":null,"due":"2026-02-01","tags":"blue"}},` +
		`{"identity":"c","values":{"id":"c","amount":30.5,"qty":7,"due":"2026-02-28"}},` +
		`{"identity":"d","values":{"id":"d","amount":-4,"qty":0,"status":"","due":"2025-12-31","tags":"red"}},` +
		`{"identity":"e","values":{"id":"e","amount":50,"qty":3,"status":"paid","due":"2026-01-15"}},` +
		`{"identity":"f","values":{"id":"f","amount":50,"qty":3,"status":"PAID","due":"2026-03-01","tags":"green"}}]`
	prepare := []string{"dataset", "sync", "orders", "--rows", rows, "--provenance", `{"source_name":"fixture","source_sha256":"sha","imported_at":"2026-04-03T10:00:00Z"}`,
		"--identity-field", "id", "--schema", `{"amount":{"type":"number"},"qty":{"type":"integer"},"status":{"type":"text"},"due":{"type":"date"},"tags":{"type":"text"}}`,
		"--json", "--vault", "${WORKSPACE}/vault"}
	type spec struct {
		id   string
		args []string
	}
	q := func(id string, args ...string) spec {
		return spec{id, append([]string{"dataset", "query", "orders"}, append(args, "--json", "--vault", "${WORKSPACE}/vault")...)}
	}
	specs := []spec{
		q("query-full-group-by-count-default", "--group-by", "status"),
		q("query-full-group-by-count-star", "--group-by", "status", "--aggregates", `[{"function":"count"}]`),
		q("query-full-group-by-qty-all-functions", "--group-by", "qty", "--aggregates", `[{"function":"sum","column":"amount"},{"function":"min","column":"amount"},{"function":"max","column":"amount"},{"function":"average","column":"amount","as":"mean"},{"function":"count","column":"status","as":"statuses"}]`),
		q("query-full-aggregate-only", "--aggregates", `[{"function":"sum","column":"amount"},{"function":"count"}]`),
		q("query-full-aggregate-with-filter", "--aggregates", `[{"function":"sum","column":"amount","as":"total"}]`, "--filters", `[{"key":"qty","operator":"gte","value":"3"}]`),
		q("query-full-group-limit", "--group-by", "qty", "--limit", "1"),
		q("query-full-unknown-aggregate", "--aggregates", `[{"function":"median","column":"amount"}]`),
		q("query-full-unknown-aggregate-column", "--aggregates", `[{"function":"sum","column":"absent"}]`),
		q("query-full-unknown-group-column", "--group-by", "absent"),
		q("query-full-bad-aggregates-json", "--aggregates", `[{"function":`),
		q("query-full-number-comparison-operators", "--columns", "id", "--filters", `[{"key":"amount","operator":"gte","value":"20"},{"key":"amount","operator":"lt","value":"50"}]`),
		q("query-full-less-than-or-equal-alias", "--columns", "id", "--filters", `[{"key":"amount","operator":"<=","value":"10"}]`),
		q("query-full-date-operators", "--columns", "id,due", "--filters", `[{"key":"due","operator":"on_or_after","value":"2026-01-15"},{"key":"due","operator":"before","value":"2026-03-01"}]`),
		q("query-full-date-greater-than", "--columns", "id", "--filters", `[{"key":"due","operator":"greater_than","value":"2026-01-15"}]`),
		q("query-full-in-and-not-in", "--columns", "id", "--filters", `[{"key":"status","operator":"in","value":"open, paid"},{"key":"id","operator":"not_in","value":"e"}]`),
		q("query-full-contains-all-any-none", "--columns", "id", "--filters", `[{"key":"tags","operator":"contains_any","value":"red green"},{"key":"tags","operator":"contains_none","value":"blue"}]`),
		q("query-full-unknown-operator-equals", "--columns", "id", "--filters", `[{"key":"status","operator":"matches","value":"PAID"}]`),
		q("query-full-identity-pseudo-filter", "--columns", "_key,identity", "--filters", `[{"key":"identity","operator":"in","value":"a,f"}]`),
		q("query-full-limit-clamped", "--columns", "id", "--limit", "5000"),
		q("query-full-negative-limit", "--columns", "id", "--limit", "-1"),
		q("query-full-large-number-column", "--columns", "id,amount", "--filters", `[{"key":"amount","operator":"gt","value":"30"}]`),
	}
	cases := make([]portQFCLICase, 0, len(specs)+1)
	setup := []portQFSetup{{Path: "vault/.seed", Content: "seed\n", Mode: 420}}
	for _, s := range specs {
		cases = append(cases, portQFCLICase{ID: s.id, Stage: "dataset-cli", PrepareArgs: prepare, Args: s.args, CompareSidecarLayout: true, Setup: setup})
	}
	// Console (non-JSON) rendering of an aggregate result.
	cases = append(cases, portQFCLICase{ID: "query-full-group-by-console-output", Stage: "dataset-cli", PrepareArgs: prepare,
		Args:                 []string{"dataset", "query", "orders", "--group-by", "qty", "--aggregates", `[{"function":"average","column":"amount"},{"function":"count"}]`, "--vault", "${WORKSPACE}/vault"},
		CompareSidecarLayout: true, Setup: setup})
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatalf("duplicate CLI case id %s", ids[i])
		}
	}
	encoded, err := json.MarshalIndent(struct {
		SchemaVersion int              `json:"schema_version"`
		Oracle        inventory.Oracle `json:"oracle"`
		Cases         []portQFCLICase  `json:"cases"`
	}{1, fixtureoracle.Current(), cases}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
