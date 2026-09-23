package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
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
		"query-cli-projected-key-ordered-first-page": false,
		"query-cli-default-columns-and-limit":        false,
		"query-cli-console-output":                   false,
		"query-cli-unknown-column":                   false,
		"query-cli-missing-dataset":                  false,
	}
	for _, testCase := range fixture.Cases {
		if _, wanted := wantCases[testCase.ID]; !wanted {
			continue
		}
		if testCase.Stage != "dataset-cli" || !containsArgument(testCase.Args, "query") {
			t.Fatalf("fixture case %q is not a dataset query process case", testCase.ID)
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
			{Identity: "b", Values: map[string]interface{}{"id": "b", "amount": 2}},
			{Identity: "a", Values: map[string]interface{}{"id": "a", "amount": 1}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.DatasetQuery("orders", DatasetQueryOptions{Columns: []string{"_key", "identity", "id"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalRows != 2 || first.ReturnedRows != 1 || !first.Capped || first.Rows[0]["_key"] != "identity:a" || first.Rows[0]["id"] != "a" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	defaultPage, err := svc.DatasetQuery("orders", DatasetQueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if defaultPage.Limit != 10 || defaultPage.TotalRows != 2 || defaultPage.ReturnedRows != 2 || defaultPage.Capped || defaultPage.Columns[0] != "amount" || defaultPage.Rows[0]["id"] != "a" {
		t.Fatalf("unexpected default page: %#v", defaultPage)
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
