package sidecar

import (
	"encoding/json"
	"testing"
)

func TestDatasetRowsReplaceIsAtomicAndScoped(t *testing.T) {
	db := setupTestDB(t)
	values, err := json.Marshal(map[string]interface{}{"amount": 12.5, "paid": true})
	if err != nil {
		t.Fatal(err)
	}
	rows := []DatasetRow{{DatasetSlug: "orders", RowKey: "hash:one", ValuesJSON: string(values), SourcePath: "datasets/orders/source.csv", RowNumber: 2}}
	if err := db.ReplaceDatasetRows("orders", rows); err != nil {
		t.Fatal(err)
	}
	got, err := db.DatasetRows("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RowKey != rows[0].RowKey || got[0].ValuesJSON != rows[0].ValuesJSON {
		t.Fatalf("unexpected rows: %#v", got)
	}
	if err := db.ReplaceDatasetRows("orders", []DatasetRow{{DatasetSlug: "orders", RowKey: "identity:id-1", Identity: "id-1", ValuesJSON: `{"amount":13}`, SourcePath: "datasets/orders/new.csv", RowNumber: 2}}); err != nil {
		t.Fatal(err)
	}
	got, err = db.DatasetRows("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Identity != "id-1" {
		t.Fatalf("replace did not remove old derived rows: %#v", got)
	}
	if other, err := db.DatasetRows("other"); err != nil || len(other) != 0 {
		t.Fatalf("rows leaked between datasets: %#v %v", other, err)
	}
}
