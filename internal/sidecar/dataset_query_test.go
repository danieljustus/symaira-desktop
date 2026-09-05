package sidecar

import (
	"fmt"
	"testing"
)

var datasetQuerySchema = map[string]string{
	"name":     "text",
	"amount":   "number",
	"date":     "date",
	"status":   "text",
	"tags":     "text",
	"empty":    "text",
	"category": "text",
}

func seedDatasetQueryRows(t *testing.T, db *DB, slug string) {
	t.Helper()
	rows := []DatasetRow{
		{DatasetSlug: slug, RowKey: "a", Identity: "alice", ValuesJSON: `{"name":"Alice","amount":10,"date":"2026-01-02","status":"open","tags":["red","blue"],"empty":"","category":"alpha"}`, SourcePath: "data/a.csv", RowNumber: 2},
		{DatasetSlug: slug, RowKey: "b", Identity: "bob", ValuesJSON: `{"name":"Bob","amount":20,"date":"2026-02-03","status":"paid","tags":["blue"],"category":"beta"}`, SourcePath: "data/b.csv", RowNumber: 3},
		{DatasetSlug: slug, RowKey: "c", Identity: "carol", ValuesJSON: `{"name":"Carol","amount":30,"date":"2026-03-04","status":"closed","tags":["green"],"empty":null,"category":"alpha"}`, SourcePath: "data/c.csv", RowNumber: 4},
		{DatasetSlug: slug, RowKey: "d", ValuesJSON: `{"name":"dave","amount":5,"date":"2025-12-31","status":"open","tags":[],"category":"gamma"}`, SourcePath: "data/d.csv", RowNumber: 5},
	}
	if err := db.ReplaceDatasetRows(slug, rows); err != nil {
		t.Fatalf("seed dataset rows: %v", err)
	}
}

func datasetQueryKeys(rows []DatasetQueryRow) []string {
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.RowKey)
	}
	return keys
}

func queryDataset(t *testing.T, db *DB, slug string, opts DatasetQueryOptions) *DatasetQueryResult {
	t.Helper()
	result, err := db.QueryDataset(slug, opts)
	if err != nil {
		t.Fatalf("QueryDataset: %v", err)
	}
	return result
}

func TestQueryDatasetValidation(t *testing.T) {
	db := setupTestDB(t)
	seedDatasetQueryRows(t, db, "sales")

	tests := []struct {
		name string
		opts DatasetQueryOptions
		want string
	}{
		{name: "empty slug", opts: DatasetQueryOptions{Schema: datasetQuerySchema}, want: "dataset slug is required"},
		{name: "empty schema", opts: DatasetQueryOptions{}, want: "dataset schema is required"},
		{name: "unknown selected column", opts: DatasetQueryOptions{Schema: datasetQuerySchema, Columns: []string{"unknown"}}, want: `dataset column "unknown" not found`},
		{name: "unknown group column", opts: DatasetQueryOptions{Schema: datasetQuerySchema, GroupBy: "unknown"}, want: `dataset group column "unknown" not found`},
		{name: "unknown filter column", opts: DatasetQueryOptions{Schema: datasetQuerySchema, Filters: []DatasetFilter{{Key: "unknown", Operator: "equals", Value: "x"}}}, want: `dataset column "unknown" not found`},
		{name: "unknown nested filter column", opts: DatasetQueryOptions{Schema: datasetQuerySchema, FilterGroup: &DatasetFilterGroup{Groups: []DatasetFilterGroup{{Filters: []DatasetFilter{{Key: "unknown"}}}}}}, want: `dataset column "unknown" not found`},
		{name: "unknown sort column", opts: DatasetQueryOptions{Schema: datasetQuerySchema, Sorts: []DatasetSort{{Key: "unknown", Ascending: true}}}, want: `dataset sort column "unknown" not found`},
		{name: "unsupported aggregate", opts: DatasetQueryOptions{Schema: datasetQuerySchema, Aggregates: []DatasetAggregate{{Function: "median", Column: "amount"}}}, want: `unsupported dataset aggregate "median"`},
		{name: "unknown aggregate column", opts: DatasetQueryOptions{Schema: datasetQuerySchema, Aggregates: []DatasetAggregate{{Function: "sum", Column: "unknown"}}}, want: `dataset aggregate column "unknown" not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slug := "sales"
			if tt.name == "empty slug" {
				slug = ""
			}
			_, err := db.QueryDataset(slug, tt.opts)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestQueryDatasetDefaultsCapsOffsetsAndPseudoColumns(t *testing.T) {
	db := setupTestDB(t)
	seedDatasetQueryRows(t, db, "sales")

	result := queryDataset(t, db, "sales", DatasetQueryOptions{Schema: datasetQuerySchema, Limit: 0})
	if result.TotalRows != 4 || result.Limit != 10 || result.Offset != 0 || len(result.Rows) != 4 {
		t.Fatalf("default page = total %d limit %d offset %d rows %d", result.TotalRows, result.Limit, result.Offset, len(result.Rows))
	}
	if got := datasetQueryKeys(result.Rows); fmt.Sprint(got) != "[a b c d]" {
		t.Fatalf("default row order = %v", got)
	}

	result = queryDataset(t, db, "sales", DatasetQueryOptions{Schema: datasetQuerySchema, Columns: []string{"_key", "identity", "name"}, Sorts: []DatasetSort{{Key: "_key", Ascending: false}}, Limit: 2, Offset: -4})
	if result.Offset != 0 || fmt.Sprint(datasetQueryKeys(result.Rows)) != "[d c]" {
		t.Fatalf("negative offset/page = offset %d rows %v", result.Offset, datasetQueryKeys(result.Rows))
	}
	if result.Rows[0].Identity != "" || result.Rows[0].Values["name"] != "dave" {
		t.Fatalf("pseudo columns/decoded values = %#v", result.Rows[0])
	}

	many := make([]DatasetRow, 0, DatasetQueryMaxRows+1)
	for i := 0; i < DatasetQueryMaxRows+1; i++ {
		many = append(many, DatasetRow{DatasetSlug: "many", RowKey: fmt.Sprintf("row-%04d", i), ValuesJSON: fmt.Sprintf(`{"amount":%d}`, i)})
	}
	if err := db.ReplaceDatasetRows("many", many); err != nil {
		t.Fatal(err)
	}
	result = queryDataset(t, db, "many", DatasetQueryOptions{Schema: map[string]string{"amount": "number"}, Limit: DatasetQueryMaxRows + 50})
	if result.TotalRows != DatasetQueryMaxRows+1 || result.Limit != DatasetQueryMaxRows || len(result.Rows) != DatasetQueryMaxRows {
		t.Fatalf("max page = total %d limit %d rows %d", result.TotalRows, result.Limit, len(result.Rows))
	}
}

func TestQueryDatasetTypedFiltersAndOperators(t *testing.T) {
	db := setupTestDB(t)
	seedDatasetQueryRows(t, db, "sales")

	tests := []struct {
		name   string
		filter DatasetFilter
		want   string
	}{
		{name: "numeric greater", filter: DatasetFilter{Key: "amount", Operator: "greater_than", Value: "10"}, want: "b c"},
		{name: "numeric gte", filter: DatasetFilter{Key: "amount", Operator: "gte", Value: "20"}, want: "b c"},
		{name: "numeric less", filter: DatasetFilter{Key: "amount", Operator: "lt", Value: "10"}, want: "d"},
		{name: "numeric lte", filter: DatasetFilter{Key: "amount", Operator: "less_than_or_equal", Value: "10"}, want: "a d"},
		{name: "date after", filter: DatasetFilter{Key: "date", Operator: "after", Value: "2026-01-02"}, want: "b c"},
		{name: "date before", filter: DatasetFilter{Key: "date", Operator: "before", Value: "2026-01-02"}, want: "d"},
		{name: "date on or after", filter: DatasetFilter{Key: "date", Operator: "on_or_after", Value: "2026-02-03"}, want: "b c"},
		{name: "date on or before", filter: DatasetFilter{Key: "date", Operator: "on_or_before", Value: "2026-01-02"}, want: "a d"},
		{name: "text equals", filter: DatasetFilter{Key: "name", Operator: "equals", Value: "ALICE"}, want: "a"},
		{name: "text not equals", filter: DatasetFilter{Key: "status", Operator: "not_equals", Value: "open"}, want: "b c"},
		{name: "contains", filter: DatasetFilter{Key: "name", Operator: "contains", Value: "ar"}, want: "c"},
		{name: "not contains", filter: DatasetFilter{Key: "name", Operator: "not_contains", Value: "a"}, want: "b"},
		{name: "prefix", filter: DatasetFilter{Key: "name", Operator: "prefix", Value: "A"}, want: "a"},
		{name: "suffix", filter: DatasetFilter{Key: "name", Operator: "suffix", Value: "e"}, want: "a d"},
		{name: "empty", filter: DatasetFilter{Key: "empty", Operator: "is_empty"}, want: "a b c d"},
		{name: "not empty", filter: DatasetFilter{Key: "empty", Operator: "is_not_empty"}, want: ""},
		{name: "set in", filter: DatasetFilter{Key: "tags", Operator: "in", Value: "red, green"}, want: "a c"},
		{name: "set not in", filter: DatasetFilter{Key: "tags", Operator: "not_in", Value: "red blue"}, want: "c d"},
		{name: "array any", filter: DatasetFilter{Key: "tags", Operator: "contains_any", Value: "red, green"}, want: "a c"},
		{name: "array none", filter: DatasetFilter{Key: "tags", Operator: "contains_none", Value: "red green"}, want: "b d"},
		{name: "array all", filter: DatasetFilter{Key: "tags", Operator: "contains_all", Value: "red blue"}, want: "a"},
		{name: "empty set in", filter: DatasetFilter{Key: "status", Operator: "in", Value: "[]"}, want: ""},
		{name: "empty set not in", filter: DatasetFilter{Key: "status", Operator: "not_in", Value: "[]"}, want: "a b c d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := queryDataset(t, db, "sales", DatasetQueryOptions{Schema: datasetQuerySchema, Filters: []DatasetFilter{tt.filter}})
			got := fmt.Sprint(datasetQueryKeys(result.Rows))
			want := "[" + tt.want + "]"
			if tt.want == "" {
				want = "[]"
			}
			if got != want {
				t.Fatalf("rows = %s, want %s", got, want)
			}
		})
	}
}

func TestQueryDatasetNestedFilterGroups(t *testing.T) {
	db := setupTestDB(t)
	seedDatasetQueryRows(t, db, "sales")

	result := queryDataset(t, db, "sales", DatasetQueryOptions{
		Schema: datasetQuerySchema,
		FilterGroup: &DatasetFilterGroup{
			Operator: "any",
			Filters:  []DatasetFilter{{Key: "status", Operator: "equals", Value: "paid"}},
			Groups: []DatasetFilterGroup{{
				Operator: "all",
				Filters: []DatasetFilter{
					{Key: "amount", Operator: "greater_than", Value: "5"},
					{Key: "status", Operator: "equals", Value: "open"},
				},
			}},
		},
	})
	if got := fmt.Sprint(datasetQueryKeys(result.Rows)); got != "[a b]" {
		t.Fatalf("nested any/all rows = %s", got)
	}

	result = queryDataset(t, db, "sales", DatasetQueryOptions{
		Schema:      datasetQuerySchema,
		FilterGroup: &DatasetFilterGroup{Operator: "all"},
	})
	if result.TotalRows != 4 {
		t.Fatalf("empty group total = %d, want 4", result.TotalRows)
	}
}

func TestQueryDatasetTypedSortingAndSpecialValues(t *testing.T) {
	db := setupTestDB(t)
	seedDatasetQueryRows(t, db, "sales")

	result := queryDataset(t, db, "sales", DatasetQueryOptions{
		Schema: datasetQuerySchema,
		Sorts:  []DatasetSort{{Key: "amount", Ascending: false}, {Key: "name", Ascending: true}},
	})
	if got := fmt.Sprint(datasetQueryKeys(result.Rows)); got != "[c b a d]" {
		t.Fatalf("typed numeric sort = %s", got)
	}

	specialSchema := map[string]string{"sql;column": "text"}
	if err := db.ReplaceDatasetRows("special", []DatasetRow{{DatasetSlug: "special", RowKey: "special", ValuesJSON: `{"sql;column":"O'Reilly'); DROP TABLE dataset_rows;--"}`}}); err != nil {
		t.Fatal(err)
	}
	result = queryDataset(t, db, "special", DatasetQueryOptions{
		Schema:  specialSchema,
		Columns: []string{"sql;column"},
		Filters: []DatasetFilter{{Key: "sql;column", Operator: "contains", Value: "DROP TABLE"}},
	})
	if result.TotalRows != 1 || result.Rows[0].Values["sql;column"] != "O'Reilly'); DROP TABLE dataset_rows;--" {
		t.Fatalf("special column/value result = %#v", result)
	}
	if count, err := db.DatasetRowCount("special"); err != nil || count != 1 {
		t.Fatalf("special dataset count = %d, %v", count, err)
	}
}

func TestQueryDatasetAggregatesAndGroupedCap(t *testing.T) {
	db := setupTestDB(t)
	seedDatasetQueryRows(t, db, "sales")

	result := queryDataset(t, db, "sales", DatasetQueryOptions{
		Schema: datasetQuerySchema,
		Aggregates: []DatasetAggregate{
			{Function: "count", As: "rows"},
			{Function: "count", Column: "amount", As: "amount_count"},
			{Function: "sum", Column: "amount", As: "sum"},
			{Function: "min", Column: "amount", As: "min"},
			{Function: "max", Column: "amount", As: "max"},
			{Function: "average", Column: "amount", As: "average"},
		},
	})
	if result.TotalRows != 1 || len(result.Rows) != 1 {
		t.Fatalf("aggregate page = total %d rows %d", result.TotalRows, len(result.Rows))
	}
	want := map[string]interface{}{"rows": float64(4), "amount_count": float64(4), "sum": float64(65), "min": float64(5), "max": float64(30), "average": float64(16.25)}
	for key, expected := range want {
		if result.Rows[0].Values[key] != expected {
			t.Errorf("aggregate %s = %#v, want %#v", key, result.Rows[0].Values[key], expected)
		}
	}

	result = queryDataset(t, db, "sales", DatasetQueryOptions{
		Schema:     datasetQuerySchema,
		GroupBy:    "status",
		Aggregates: []DatasetAggregate{{Function: "count", As: "rows"}},
		Limit:      1,
	})
	if result.TotalRows != 3 || len(result.Rows) != 1 || result.Rows[0].Values["status"] != "closed" || result.Rows[0].Values["rows"] != float64(1) {
		t.Fatalf("grouped aggregate = %#v total=%d", result.Rows, result.TotalRows)
	}

	many := make([]DatasetRow, 0, DatasetQueryMaxRows+1)
	for i := 0; i < DatasetQueryMaxRows+1; i++ {
		many = append(many, DatasetRow{DatasetSlug: "groups", RowKey: fmt.Sprintf("row-%04d", i), ValuesJSON: fmt.Sprintf(`{"category":"group-%04d"}`, i)})
	}
	if err := db.ReplaceDatasetRows("groups", many); err != nil {
		t.Fatal(err)
	}
	result = queryDataset(t, db, "groups", DatasetQueryOptions{
		Schema:  map[string]string{"category": "text"},
		GroupBy: "category",
		Limit:   DatasetQueryMaxRows + 10,
	})
	if result.TotalRows != DatasetQueryMaxRows+1 || result.Limit != DatasetQueryMaxRows || len(result.Rows) != DatasetQueryMaxRows {
		t.Fatalf("group cap = total %d limit %d rows %d", result.TotalRows, result.Limit, len(result.Rows))
	}
}

func TestQueryDatasetRejectsMalformedRowAndDatasetRowCountErrors(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.conn.Exec(`INSERT INTO dataset_rows(dataset_slug, row_key, values_json, source_path, row_number) VALUES (?, ?, ?, ?, ?)`, "broken", "bad", `{not-json`, "broken.csv", 2); err != nil {
		t.Fatal(err)
	}
	_, err := db.QueryDataset("broken", DatasetQueryOptions{Schema: map[string]string{"name": "text"}})
	if err == nil || err.Error() != `decode dataset row "bad": invalid character 'n' looking for beginning of object key string` {
		t.Fatalf("malformed row error = %v", err)
	}

	if count, err := db.DatasetRowCount("broken"); err != nil || count != 1 {
		t.Fatalf("DatasetRowCount = %d, %v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DatasetRowCount("broken"); err == nil {
		t.Fatal("DatasetRowCount on closed database returned nil error")
	}
}

func TestQueryDatasetSetFiltersHandleScalarsAndUnsafeContainers(t *testing.T) {
	db := setupTestDB(t)
	slug := "set-values"
	rows := []DatasetRow{
		{DatasetSlug: slug, RowKey: "a", ValuesJSON: `{"text":"alpha","number":10,"enabled":true,"tags":["red","blue"]}`},
		{DatasetSlug: slug, RowKey: "b", ValuesJSON: `{"text":"beta","number":20,"enabled":false,"tags":["green"]}`},
		{DatasetSlug: slug, RowKey: "c", ValuesJSON: `{"text":null,"number":null,"enabled":null,"tags":null}`},
		{DatasetSlug: slug, RowKey: "d", ValuesJSON: `{"text":"[malformed","number":"not-a-number","enabled":"not-a-bool","tags":"not-json"}`},
		{DatasetSlug: slug, RowKey: "e", ValuesJSON: `{"text":null,"enabled":true}`},
		{DatasetSlug: slug, RowKey: "f", ValuesJSON: `{}`},
	}
	if err := db.ReplaceDatasetRows(slug, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO dataset_rows(dataset_slug, row_key, values_json, source_path, row_number) VALUES(?,?,?,?,?)`, slug, "g", "not-json", "source.csv", 7); err != nil {
		t.Fatal(err)
	}

	schema := map[string]string{
		"text":    "text",
		"number":  "number",
		"enabled": "bool",
		"tags":    "text",
	}
	tests := []struct {
		name   string
		filter DatasetFilter
		want   string
	}{
		{name: "scalar text in", filter: DatasetFilter{Key: "text", Operator: "in", Value: "alpha"}, want: "a"},
		{name: "scalar text not in", filter: DatasetFilter{Key: "text", Operator: "not_in", Value: "alpha"}, want: "b d f"},
		{name: "scalar number in", filter: DatasetFilter{Key: "number", Operator: "in", Value: "20"}, want: "b"},
		{name: "scalar number not in", filter: DatasetFilter{Key: "number", Operator: "not_in", Value: "20"}, want: "a d e f"},
		{name: "scalar bool in", filter: DatasetFilter{Key: "enabled", Operator: "in", Value: "true"}, want: "a e"},
		{name: "scalar bool not in", filter: DatasetFilter{Key: "enabled", Operator: "not_in", Value: "true"}, want: "b d f"},
		{name: "array in", filter: DatasetFilter{Key: "tags", Operator: "in", Value: "red"}, want: "a"},
		{name: "array not in", filter: DatasetFilter{Key: "tags", Operator: "not_in", Value: "red"}, want: "b d e f"},
		{name: "array contains any", filter: DatasetFilter{Key: "tags", Operator: "contains_any", Value: "red green"}, want: "a b"},
		{name: "array contains none", filter: DatasetFilter{Key: "tags", Operator: "contains_none", Value: "red green"}, want: "c d e f"},
		{name: "array contains all", filter: DatasetFilter{Key: "tags", Operator: "contains_all", Value: "red blue"}, want: "a"},
		{name: "bound injection value", filter: DatasetFilter{Key: "text", Operator: "in", Value: `alpha'); DROP TABLE dataset_rows;--`}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := db.QueryDataset(slug, DatasetQueryOptions{Schema: schema, Filters: []DatasetFilter{tt.filter}})
			if err != nil {
				t.Fatalf("QueryDataset: %v", err)
			}
			got := fmt.Sprint(datasetQueryKeys(result.Rows))
			want := "[]"
			if tt.want != "" {
				want = "[" + tt.want + "]"
			}
			if got != want {
				t.Fatalf("rows = %s, want %s", got, want)
			}
		})
	}
	if count, err := db.DatasetRowCount(slug); err != nil || count != len(rows)+1 {
		t.Fatalf("dataset rows after bound value = %d, %v", count, err)
	}
}
