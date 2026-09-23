package service

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestPortDatasetQueryCLIContract(t *testing.T) {
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
