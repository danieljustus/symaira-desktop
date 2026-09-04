package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestDatasetImportIsTypedIdempotentAndRebuildable(t *testing.T) {
	vaultRoot := t.TempDir()
	source := filepath.Join(t.TempDir(), "orders.csv")
	csvData := "id,date,amount\norder-1,2026-01-02,12.50\norder-2,2026-01-03,8\n"
	if err := os.WriteFile(source, []byte(csvData), 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(vaultRoot, db)
	now := time.Date(2026, 1, 4, 5, 6, 7, 0, time.UTC)
	first, err := svc.DatasetImport(source, DatasetImportOptions{Title: "Orders", IdentityField: "id", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if first.Rows != 2 || first.RawPath != "datasets/orders/2026-01-04.csv" || first.HandlePath != "datasets/orders.md" {
		t.Fatalf("unexpected first import: %#v", first)
	}
	rawAbs, err := vault.SecurePath(vaultRoot, first.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rawAbs); err != nil { //nolint:gosec // path was confined by vault.SecurePath above
		t.Fatal(err)
	}
	if err := db.RefreshIndex(vaultRoot); err != nil {
		t.Fatal(err)
	}
	searchResults, err := db.Search("12.50")
	if err != nil {
		t.Fatal(err)
	}
	if len(searchResults) != 0 {
		t.Fatalf("raw dataset content was added to full-text index: %#v", searchResults)
	}
	rows, err := db.DatasetRows("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two materialized rows, got %d", len(rows))
	}
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(rows[0].ValuesJSON), &values); err != nil {
		t.Fatal(err)
	}
	if _, ok := values["amount"].(float64); !ok {
		t.Fatalf("amount was not retained as JSON number: %#v", values)
	}

	second, err := svc.DatasetImport(source, DatasetImportOptions{Title: "Orders", IdentityField: "id", Now: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if second.Rows != 2 {
		t.Fatalf("repeated import duplicated rows: %#v", second)
	}
	rowsAfterRepeat, err := db.DatasetRows("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAfterRepeat) != 2 {
		t.Fatalf("expected two rows after repeat, got %d", len(rowsAfterRepeat))
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	rebuiltDB, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	rebuiltSvc := New(vaultRoot, rebuiltDB)
	if err := rebuiltSvc.RebuildDatasets(); err != nil {
		t.Fatal(err)
	}
	rebuiltRows, err := rebuiltDB.DatasetRows("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuiltRows) != len(rowsAfterRepeat) {
		t.Fatalf("rebuild changed row count: before=%d after=%d", len(rowsAfterRepeat), len(rebuiltRows))
	}
	for i := range rowsAfterRepeat {
		if rowsAfterRepeat[i].RowKey != rebuiltRows[i].RowKey || rowsAfterRepeat[i].ValuesJSON != rebuiltRows[i].ValuesJSON {
			t.Fatalf("rebuild changed row %d: before=%#v after=%#v", i, rowsAfterRepeat[i], rebuiltRows[i])
		}
	}
}
