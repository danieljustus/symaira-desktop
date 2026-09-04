package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func TestDatasetToolHandlersRoundTrip(t *testing.T) {
	factory := testServiceFactory(t)
	syncTool := newDatasetSyncTool(factory)
	input := json.RawMessage(`{"dataset":"orders","identity_field":"id","provenance":{"imported_at":"2026-09-04T00:00:00Z","source_name":"feed","source_sha256":"sha-795"},"rows":[{"identity":"o1","values":{"id":"o1","status":"paid","amount":12.5}}]}`)
	if _, err := syncTool.Handler(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	list, err := newDatasetListTool(factory).Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := list.([]service.DatasetSummary); len(got) != 1 || got[0].Slug != "orders" {
		t.Fatalf("unexpected dataset list: %#v", list)
	}
	query, err := newDatasetQueryTool(factory).Handler(context.Background(), json.RawMessage(`{"dataset":"orders","columns":["id"],"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := query.(*service.DatasetQueryResult); got.ReturnedRows != 1 || got.Rows[0]["id"] != "o1" {
		t.Fatalf("unexpected dataset query: %#v", got)
	}
}

func TestDatasetQueryToolRejectsRawSQL(t *testing.T) {
	tool := newDatasetQueryTool(testServiceFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"dataset":"orders","sql":"DROP TABLE dataset_rows"}`)); err == nil {
		t.Fatal("dataset query accepted an unknown raw SQL field")
	}
}
