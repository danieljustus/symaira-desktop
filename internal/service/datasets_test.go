package service

import (
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestDatasetQueryFiltersAggregatesAndCaps(t *testing.T) {
	vaultRoot := t.TempDir()
	db, err := sidecar.Open(t.TempDir() + "/sidecar.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(vaultRoot, db)
	_, err = svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id",
		Provenance: dataset.Provenance{ImportedAt: "2026-01-04T00:00:00Z", SourceName: "provider", SourceSHA256: "sha-1"},
		Rows: []DatasetSyncRow{
			{Identity: "1", Values: map[string]interface{}{"id": "1", "status": "paid", "category": "a", "amount": 12.5}},
			{Identity: "2", Values: map[string]interface{}{"id": "2", "status": "paid", "category": "a", "amount": 7.5}},
			{Identity: "3", Values: map[string]interface{}{"id": "3", "status": "open", "category": "b", "amount": 20.0}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.DatasetQuery("orders", DatasetQueryOptions{
		Columns: []string{"id", "amount"},
		Filters: []dbviews.Filter{{Key: "status", Operator: "equals", Value: "paid"}},
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalRows != 2 || result.ReturnedRows != 1 || !result.Capped || result.Rows[0]["id"] != float64(1) {
		t.Fatalf("unexpected filtered result: %#v", result)
	}
	grouped, err := svc.DatasetQuery("orders", DatasetQueryOptions{
		GroupBy:    "category",
		Aggregates: []DatasetAggregate{{Column: "amount", Function: "sum", As: "total"}, {Function: "count", As: "n"}},
		Limit:      10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if grouped.TotalRows != 2 || grouped.Rows[0]["category"] != "a" || grouped.Rows[0]["total"] != float64(20) || grouped.Rows[0]["n"] != 2 {
		t.Fatalf("unexpected grouped result: %#v", grouped)
	}
}

func TestDatasetSyncRequiresProvenanceAndIsIdempotent(t *testing.T) {
	vaultRoot := t.TempDir()
	db, err := sidecar.Open(t.TempDir() + "/sidecar.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(vaultRoot, db)
	opts := DatasetSyncOptions{Slug: "events", IdentityField: "event_id", Provenance: dataset.Provenance{ImportedAt: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), SourceName: "feed", SourceSHA256: "sha-2"}, Rows: []DatasetSyncRow{{Identity: "evt-1", Values: map[string]interface{}{"event_id": "evt-1", "value": 4}}}}
	if _, err := svc.DatasetSync(DatasetSyncOptions{Slug: "events", IdentityField: "event_id", Rows: opts.Rows}); err == nil {
		t.Fatal("sync without provenance unexpectedly succeeded")
	}
	first, err := svc.DatasetSync(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.DatasetSync(opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Idempotent || !second.Idempotent || first.Rows != 1 || second.Rows != 1 {
		t.Fatalf("unexpected sync results: first=%#v second=%#v", first, second)
	}
	rows, err := db.DatasetRows("events")
	if err != nil || len(rows) != 1 {
		t.Fatalf("sync duplicated rows: %d %v", len(rows), err)
	}
}
