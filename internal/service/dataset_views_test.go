package service

import (
	"fmt"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestViewsExecDatasetUsesTypedQueryAndPageCap(t *testing.T) {
	vaultRoot := t.TempDir()
	db, err := sidecar.Open(t.TempDir() + "/sidecar.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(vaultRoot, db)
	rows := make([]DatasetSyncRow, 10025)
	for i := range rows {
		rows[i] = DatasetSyncRow{Identity: fmt.Sprintf("%05d", i), Values: map[string]interface{}{
			"id": fmt.Sprintf("%05d", i), "amount": i, "status": map[bool]string{true: "paid", false: "open"}[i%2 == 0],
		}}
	}
	if _, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id",
		Provenance: dataset.Provenance{ImportedAt: "2026-01-04T00:00:00Z", SourceName: "provider", SourceSHA256: "dataset-test"},
		Schema:     map[string]dbviews.PropertyConfig{"id": {Type: "text"}, "amount": {Type: "number"}, "status": {Type: "select"}},
		Rows:       rows,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "paid", Name: "Paid", Source: "dataset:orders", Filters: []dbviews.Filter{{Key: "amount", Operator: ">=", Value: "0"}}, Sorts: []dbviews.Sort{{Key: "amount", Ascending: false}}, Columns: []string{"id", "amount"}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ViewsExec("paid")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != DatasetMaxRowCap {
		t.Fatalf("got %d rows, want bounded page of %d", len(got), DatasetMaxRowCap)
	}
	if got[0]["amount"] != float64(10024) || got[len(got)-1]["amount"] != float64(9025) {
		t.Fatalf("dataset sort/filter was not typed: first=%v last=%v", got[0]["amount"], got[len(got)-1]["amount"])
	}
}

func TestViewsExecDatasetNestedGroupComputedAndMissing(t *testing.T) {
	vaultRoot := t.TempDir()
	db, err := sidecar.Open(t.TempDir() + "/sidecar.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(vaultRoot, db)
	if _, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id",
		Provenance: dataset.Provenance{ImportedAt: "2026-01-04T00:00:00Z", SourceName: "provider", SourceSHA256: "dataset-test-2"},
		Schema:     map[string]dbviews.PropertyConfig{"id": {Type: "text"}, "amount": {Type: "number"}, "status": {Type: "select"}, "when": {Type: "date"}},
		Rows: []DatasetSyncRow{
			{Identity: "1", Values: map[string]interface{}{"id": "1", "amount": 12.5, "status": "paid", "when": "2026-01-02"}},
			{Identity: "2", Values: map[string]interface{}{"id": "2", "amount": 7.5, "status": "open", "when": "2026-01-03"}},
			{Identity: "3", Values: map[string]interface{}{"id": "3", "amount": 20.0, "status": "cancelled", "when": "2026-01-04"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "nested", Name: "Nested", Source: "dataset:orders", FilterGroup: &dbviews.FilterGroup{Operator: "any", Filters: []dbviews.Filter{{Key: "status", Value: "paid"}, {Key: "amount", Operator: ">", Value: "19"}}}, Computed: map[string]dbviews.ComputedColumn{"label": {Formula: "{status}:{amount}"}}, Sorts: []dbviews.Sort{{Key: "amount", Ascending: true}}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ViewsExec("nested")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["id"] != "1" || got[1]["id"] != "3" || got[0]["label"] != "paid:12.5" {
		t.Fatalf("unexpected dataset view rows: %#v", got)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "missing", Name: "Missing", Source: "dataset:deleted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ViewsExec("missing"); err == nil || !containsError(err, "dataset \"deleted\" not found") {
		t.Fatalf("missing dataset error = %v", err)
	}
}

func containsError(err error, want string) bool {
	return err != nil && (err.Error() == want || len(err.Error()) > len(want) && containsErrorString(err.Error(), want))
}
func containsErrorString(got, want string) bool {
	for i := 0; i+len(want) <= len(got); i++ {
		if got[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
