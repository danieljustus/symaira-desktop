package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestPortDatasetQueryCLIContract(t *testing.T) {
	fixtureBytes, err := os.ReadFile("../../testdata/port/dataset/cli.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			ID      string   `json:"id"`
			Stage   string   `json:"stage"`
			Args    []string `json:"args"`
			Prepare []string `json:"prepare_args"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	wantCases := map[string]bool{
		"query-cli-projected-key-ordered-first-page":    false,
		"query-cli-default-columns-and-limit":           false,
		"query-cli-console-output":                      false,
		"query-cli-unknown-column":                      false,
		"query-cli-missing-dataset":                     false,
		"query-cli-filter-text-case-insensitive-equals": false,
		"query-cli-filter-numeric-coercion":             false,
		"query-cli-filter-not-equals-missing-null":      false,
		"query-cli-filter-is-empty-missing-null-empty":  false,
		"query-cli-filter-unknown-column":               false,
		"query-cli-nested-group-all-any":                false,
	}
	for _, testCase := range fixture.Cases {
		if _, wanted := wantCases[testCase.ID]; !wanted {
			continue
		}
		if testCase.Stage != "dataset-cli" || !containsArgument(testCase.Args, "query") {
			t.Fatalf("fixture case %q is not a dataset query process case", testCase.ID)
		}
		if strings.Contains(testCase.ID, "filter-") && !containsArgument(testCase.Args, "--filters") {
			t.Fatalf("fixture case %q does not exercise --filters", testCase.ID)
		}
		if testCase.ID == "query-cli-nested-group-all-any" && !containsArgument(testCase.Args, "--filter-group") {
			t.Fatalf("fixture case %q does not exercise --filter-group", testCase.ID)
		}
		if len(testCase.Prepare) > 0 && !containsArgument(testCase.Prepare, "sync") {
			t.Fatalf("fixture case %q does not seed its dataset through the CLI", testCase.ID)
		}
		wantCases[testCase.ID] = true
	}
	for id, found := range wantCases {
		if !found {
			t.Fatalf("dataset query fixture is missing case %q", id)
		}
	}

	root := t.TempDir()
	db, err := sidecar.Open(t.TempDir() + "/sidecar.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(root, db)
	if _, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id",
		Provenance: dataset.Provenance{ImportedAt: "2026-04-03T10:00:00Z", SourceName: "fixture", SourceSHA256: "sha"},
		Rows: []DatasetSyncRow{
			{Identity: "b", Values: map[string]interface{}{"id": "b", "amount": 20, "status": nil}},
			{Identity: "a", Values: map[string]interface{}{"id": "a", "amount": 10, "status": "open"}},
			{Identity: "c", Values: map[string]interface{}{"id": "c", "amount": 30}},
			{Identity: "d", Values: map[string]interface{}{"id": "d", "amount": 40, "status": ""}},
			{Identity: "e", Values: map[string]interface{}{"id": "e", "amount": 50, "status": "paid"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.DatasetQuery("orders", DatasetQueryOptions{Columns: []string{"_key", "identity", "id"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalRows != 5 || first.ReturnedRows != 1 || !first.Capped || first.Rows[0]["_key"] != "identity:a" || first.Rows[0]["id"] != "a" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	defaultPage, err := svc.DatasetQuery("orders", DatasetQueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if defaultPage.Limit != 10 || defaultPage.TotalRows != 5 || defaultPage.ReturnedRows != 5 || defaultPage.Capped || defaultPage.Columns[0] != "amount" || defaultPage.Rows[0]["id"] != "a" {
		t.Fatalf("unexpected default page: %#v", defaultPage)
	}
	filterCases := []struct {
		name   string
		filter dbviews.Filter
		want   []string
	}{
		{name: "case-insensitive text equality", filter: dbviews.Filter{Key: "status", Operator: "equals", Value: "OPEN"}, want: []string{"a"}},
		{name: "numeric coercion", filter: dbviews.Filter{Key: "amount", Operator: "equals", Value: "10.0"}, want: []string{"a"}},
		{name: "not equals includes missing and null after CSV normalization", filter: dbviews.Filter{Key: "status", Operator: "not_equals", Value: "open"}, want: []string{"b", "c", "d", "e"}},
		{name: "is empty includes missing null and empty", filter: dbviews.Filter{Key: "status", Operator: "is_empty"}, want: []string{"b", "c", "d"}},
	}
	for _, testCase := range filterCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := svc.DatasetQuery("orders", DatasetQueryOptions{Filters: []dbviews.Filter{testCase.filter}})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(result.Rows))
			for _, row := range result.Rows {
				got = append(got, row["id"].(string))
			}
			if strings.Join(got, ",") != strings.Join(testCase.want, ",") || result.TotalRows != len(testCase.want) {
				t.Fatalf("rows = %v total=%d, want %v", got, result.TotalRows, testCase.want)
			}
		})
	}
	if _, err := svc.DatasetQuery("orders", DatasetQueryOptions{Filters: []dbviews.Filter{{Key: "absent", Operator: "equals", Value: "x"}}}); err == nil || err.Error() != `dataset column "absent" not found` {
		t.Fatalf("unknown filter-column error = %v", err)
	}
	combined, err := svc.DatasetQuery("orders", DatasetQueryOptions{Filters: []dbviews.Filter{
		{Key: "status", Operator: "is_empty"},
		{Key: "amount", Operator: "not_equals", Value: "20"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if combined.TotalRows != 2 || combined.Rows[0]["id"] != "c" || combined.Rows[1]["id"] != "d" {
		t.Fatalf("combined filters = %#v", combined)
	}
	nested, err := svc.DatasetQuery("orders", DatasetQueryOptions{FilterGroup: &dbviews.FilterGroup{
		Operator: "any",
		Filters:  []dbviews.Filter{{Key: "status", Operator: "equals", Value: "paid"}},
		Groups: []dbviews.FilterGroup{{
			Operator: "all",
			Filters:  []dbviews.Filter{{Key: "amount", Operator: "greater_than", Value: "20"}},
			Groups: []dbviews.FilterGroup{{
				Operator: "any",
				Filters: []dbviews.Filter{
					{Key: "status", Operator: "is_empty"},
					{Key: "status", Operator: "equals", Value: "OPEN"},
				},
			}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if nested.TotalRows != 3 || nested.Rows[0]["id"] != "c" || nested.Rows[1]["id"] != "d" || nested.Rows[2]["id"] != "e" {
		t.Fatalf("nested all/any filter group = %#v", nested)
	}
	if _, err := svc.DatasetQuery("orders", DatasetQueryOptions{Columns: []string{"absent"}}); err == nil || err.Error() != `dataset column "absent" not found` {
		t.Fatalf("unknown-column error = %v", err)
	}
	if _, err := svc.DatasetQuery("missing", DatasetQueryOptions{}); err == nil || err.Error() == "" {
		t.Fatalf("missing-dataset query error = %v", err)
	}
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want || strings.HasPrefix(arg, want+" ") {
			return true
		}
	}
	return false
}
