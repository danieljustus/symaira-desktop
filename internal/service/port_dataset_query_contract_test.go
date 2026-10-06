package service

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestPortDatasetQueryCLIContract(t *testing.T) {
	// Case definitions are generator inputs, not observations copied from Q.
	fixtureBytes, err := json.MarshalIndent(struct {
		SchemaVersion int              `json:"schema_version"`
		Oracle        inventory.Oracle `json:"oracle"`
		Cases         json.RawMessage  `json:"cases"`
	}{1, fixtureoracle.Current(), json.RawMessage(datasetCLICaseDefinitions)}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fixtureBytes = append(fixtureBytes, '\n')
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
		"query-cli-filter-is-not-empty-aliases":         false,
		"query-cli-filter-text-pattern-operators":       false,
		"query-cli-filter-not-contains-missing-null":    false,
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
		{name: "is not empty", filter: dbviews.Filter{Key: "status", Operator: "is_not_empty"}, want: []string{"a", "e"}},
		{name: "not empty alias", filter: dbviews.Filter{Key: "status", Operator: "not_empty"}, want: []string{"a", "e"}},
		{name: "contains pattern wildcard", filter: dbviews.Filter{Key: "status", Operator: "contains", Value: "p_n"}, want: []string{"a"}},
		{name: "not contains includes missing null and empty", filter: dbviews.Filter{Key: "status", Operator: "not_contains", Value: "open"}, want: []string{"b", "c", "d", "e"}},
		{name: "starts with", filter: dbviews.Filter{Key: "status", Operator: "starts_with", Value: "O"}, want: []string{"a"}},
		{name: "prefix alias", filter: dbviews.Filter{Key: "status", Operator: "prefix", Value: "o"}, want: []string{"a"}},
		{name: "ends with", filter: dbviews.Filter{Key: "status", Operator: "ends_with", Value: "D"}, want: []string{"e"}},
		{name: "suffix alias", filter: dbviews.Filter{Key: "status", Operator: "suffix", Value: "id"}, want: []string{"e"}},
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
	const path = "../../testdata/port/dataset/cli.json"
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, fixtureBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, fixtureBytes) {
		t.Fatal("dataset CLI input corpus differs from current Go definitions/source identity; regenerate deliberately")
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

// The sixteen existing differential cases are preserved as Go-owned input definitions.
const datasetCLICaseDefinitions = `[
  {
    "id": "sync-cli-inline-provenance-large-integer-json",
    "stage": "dataset-cli",
    "args": [
      "dataset",
      "sync",
      "rounded",
      "--rows",
      "[{\"identity\":\"one\",\"values\":{\"id\":\"one\",\"amount\":9007199254740993}}]",
      "--provenance",
      "{\"source_name\":\"feed\",\"source_sha256\":\"sha-1\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "sync-cli-file-inputs-flag-overrides-text",
    "stage": "dataset-cli",
    "args": [
      "dataset",
      "sync",
      "file-ledger",
      "--rows",
      "${WORKSPACE}/rows.json",
      "--provenance",
      "${WORKSPACE}/provenance.json",
      "--source-name",
      "override-feed",
      "--source-sha256",
      "override-sha",
      "--imported-at",
      "2026-04-05T08:09:10Z",
      "--title",
      "File Ledger",
      "--identity-field",
      "id",
      "--schema",
      "${WORKSPACE}/schema.json",
      "--sensitivity",
      "confidential",
      "--retention-rule",
      "finance-7y",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      },
      {
        "path": "rows.json",
        "content": "[{\"identity\":\"one\",\"values\":{\"id\":\"one\",\"amount\":12.5}}]\n",
        "mode": 420
      },
      {
        "path": "provenance.json",
        "content": "{\"source_name\":\"feed\",\"source_sha256\":\"source-sha\",\"imported_at\":\"2026-04-05T08:09:10Z\"}\n",
        "mode": 420
      },
      {
        "path": "schema.json",
        "content": "{\"amount\":{\"type\":\"number\",\"label\":\"Amount\"}}\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-projected-key-ordered-first-page",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":2}},{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":1}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--columns",
      "_key,identity,id",
      "--limit",
      "1",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-default-columns-and-limit",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":2}},{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":1}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-console-output",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":2}},{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":1}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-unknown-column",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--columns",
      "absent",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-missing-dataset",
    "stage": "dataset-cli",
    "stdout_mode": "ignore",
    "args": [
      "dataset",
      "query",
      "missing",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-text-case-insensitive-equals",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"status\",\"operator\":\"equals\",\"value\":\"OPEN\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-numeric-coercion",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"amount\",\"operator\":\"equals\",\"value\":\"10.0\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-not-equals-missing-null",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"status\",\"operator\":\"not_equals\",\"value\":\"open\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-is-empty-missing-null-empty",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"status\",\"operator\":\"is_empty\",\"value\":\"\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-unknown-column",
    "stage": "dataset-cli",
    "stdout_mode": "ignore",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"absent\",\"operator\":\"equals\",\"value\":\"x\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-nested-group-all-any",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filter-group",
      "{\"operator\":\"any\",\"filters\":[{\"key\":\"status\",\"operator\":\"equals\",\"value\":\"paid\"}],\"groups\":[{\"operator\":\"all\",\"filters\":[{\"key\":\"amount\",\"operator\":\"greater_than\",\"value\":\"20\"}],\"groups\":[{\"operator\":\"any\",\"filters\":[{\"key\":\"status\",\"operator\":\"is_empty\",\"value\":\"\"},{\"key\":\"status\",\"operator\":\"equals\",\"value\":\"OPEN\"}]}]}]}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-is-not-empty-aliases",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"status\",\"operator\":\"is_not_empty\",\"value\":\"\"},{\"key\":\"status\",\"operator\":\"not_empty\",\"value\":\"\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-text-pattern-operators",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "labels",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"label\":\"AlPhX_beta\",\"starts\":\"ALPHA start\",\"starts_alias\":\"alpine\",\"ends\":\"the OMEGA\",\"ends_alias\":\"subomega\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"label\":\"nothing\",\"starts\":\"no\",\"starts_alias\":\"no\",\"ends\":\"no\",\"ends_alias\":\"no\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"label\":{\"type\":\"text\"},\"starts\":{\"type\":\"text\"},\"starts_alias\":{\"type\":\"text\"},\"ends\":{\"type\":\"text\"},\"ends_alias\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "labels",
      "--filters",
      "[{\"key\":\"label\",\"operator\":\"contains\",\"value\":\"ph%_\"},{\"key\":\"starts\",\"operator\":\"starts_with\",\"value\":\"al\"},{\"key\":\"starts_alias\",\"operator\":\"prefix\",\"value\":\"al\"},{\"key\":\"ends\",\"operator\":\"ends_with\",\"value\":\"ga\"},{\"key\":\"ends_alias\",\"operator\":\"suffix\",\"value\":\"ga\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  },
  {
    "id": "query-cli-filter-not-contains-missing-null",
    "stage": "dataset-cli",
    "prepare_args": [
      "dataset",
      "sync",
      "orders",
      "--rows",
      "[{\"identity\":\"a\",\"values\":{\"id\":\"a\",\"amount\":10,\"status\":\"open\"}},{\"identity\":\"b\",\"values\":{\"id\":\"b\",\"amount\":20,\"status\":null}},{\"identity\":\"c\",\"values\":{\"id\":\"c\",\"amount\":30}},{\"identity\":\"d\",\"values\":{\"id\":\"d\",\"amount\":40,\"status\":\"\"}},{\"identity\":\"e\",\"values\":{\"id\":\"e\",\"amount\":50,\"status\":\"paid\"}}]",
      "--provenance",
      "{\"source_name\":\"fixture\",\"source_sha256\":\"sha\",\"imported_at\":\"2026-04-03T10:00:00Z\"}",
      "--identity-field",
      "id",
      "--schema",
      "{\"amount\":{\"type\":\"number\"},\"status\":{\"type\":\"text\"}}",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "args": [
      "dataset",
      "query",
      "orders",
      "--filters",
      "[{\"key\":\"status\",\"operator\":\"not_contains\",\"value\":\"open\"}]",
      "--json",
      "--vault",
      "${WORKSPACE}/vault"
    ],
    "compare_sidecar_layout": true,
    "setup": [
      {
        "path": "vault/.seed",
        "content": "seed\n",
        "mode": 420
      }
    ]
  }
]`
