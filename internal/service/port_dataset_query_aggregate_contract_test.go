package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

const portDatasetQueryAggregateFixture = "../../testdata/port/dataset/query-aggregate.json"

const (
	portDatasetQueryAggregateOracleCommit  = "38891d35eb8ceb6c348eca9a78b3fb2873677e3d"
	portDatasetQueryAggregateOracleRelease = "post-v0.12.2-security-880"
)

type portDatasetQueryAggregateFixtureData struct {
	SchemaVersion int                             `json:"schema_version"`
	Oracle        portDatasetQueryAggregateOracle `json:"oracle"`
	Dataset       string                          `json:"dataset"`
	Rows          []DatasetSyncRow                `json:"rows"`
	Query         portDatasetQueryAggregate       `json:"query"`
	Result        *DatasetQueryResult             `json:"result"`
}

type portDatasetQueryAggregateOracle struct {
	Commit    string `json:"commit"`
	Release   string `json:"release"`
	ModuleGo  string `json:"module_go"`
	Toolchain string `json:"toolchain"`
}

type portDatasetQueryAggregate struct {
	GroupBy    string             `json:"group_by"`
	Aggregates []DatasetAggregate `json:"aggregates"`
	Limit      int                `json:"limit"`
}

// Regenerate the fixed Go oracle with:
// PORT_GENERATE=1 go test ./internal/service -run '^TestPortDatasetQueryAggregateContract$'
func TestPortDatasetQueryAggregateContract(t *testing.T) {
	fixture, encoded := buildPortDatasetQueryAggregateFixture(t)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(portDatasetQueryAggregateFixture), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(portDatasetQueryAggregateFixture, encoded, 0o644); err != nil { //nolint:gosec // fixed fixture path
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", portDatasetQueryAggregateFixture, len(encoded))
		return
	}
	want, err := os.ReadFile(portDatasetQueryAggregateFixture) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatalf("read dataset aggregate fixture (generate with PORT_GENERATE=1): %v", err)
	}
	if !bytes.Equal(want, encoded) {
		t.Fatalf("dataset aggregate fixture is stale (regenerate with PORT_GENERATE=1); result: %#v", fixture.Result)
	}
}

func buildPortDatasetQueryAggregateFixture(t *testing.T) (portDatasetQueryAggregateFixtureData, []byte) {
	t.Helper()
	rows := []DatasetSyncRow{
		{Identity: "a", Values: map[string]interface{}{"id": "a", "status": "open"}},
		{Identity: "b", Values: map[string]interface{}{"id": "b", "status": "paid"}},
		{Identity: "c", Values: map[string]interface{}{"id": "c", "status": "open"}},
		{Identity: "d", Values: map[string]interface{}{"id": "d", "status": "closed"}},
	}
	query := portDatasetQueryAggregate{
		GroupBy:    "status",
		Aggregates: []DatasetAggregate{{Function: "count"}},
		Limit:      1,
	}
	root := t.TempDir()
	db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	svc := New(root, db)
	if _, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id",
		Schema: map[string]dbviews.PropertyConfig{
			"id": {Type: "text"}, "status": {Type: "text"},
		},
		Provenance: dataset.Provenance{
			ImportedAt: "2026-09-25T00:00:00Z", SourceName: "fixture",
			SourceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Rows: rows,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.DatasetQuery("orders", DatasetQueryOptions{
		GroupBy: query.GroupBy, Aggregates: query.Aggregates, Limit: query.Limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalRows != 3 || result.ReturnedRows != 1 || result.Limit != 1 || !result.Capped ||
		len(result.Columns) != 2 || result.Columns[0] != "status" || result.Columns[1] != "count" ||
		len(result.Rows) != 1 || result.Rows[0]["status"] != "closed" || result.Rows[0]["count"] != 1 {
		t.Fatalf("grouped count projection = %#v", result)
	}
	fixture := portDatasetQueryAggregateFixtureData{
		SchemaVersion: 1,
		Oracle: portDatasetQueryAggregateOracle{
			Commit: portDatasetQueryAggregateOracleCommit, Release: portDatasetQueryAggregateOracleRelease,
			ModuleGo: "1.26.6", Toolchain: "go1.26.6",
		},
		Dataset: "orders", Rows: rows, Query: query, Result: result,
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return fixture, append(encoded, '\n')
}
