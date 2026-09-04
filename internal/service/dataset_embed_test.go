package service

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestDatasetBaseEmbedPushesAndKeepsCap(t *testing.T) {
	vaultRoot := t.TempDir()
	db, err := sidecar.Open(t.TempDir() + "/sidecar.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(vaultRoot, db)
	rows := make([]DatasetSyncRow, 20)
	for i := range rows {
		rows[i] = DatasetSyncRow{Identity: string(rune('a' + i)), Values: map[string]interface{}{"name": string(rune('a' + i)), "amount": i}}
	}
	if _, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "items", Title: "Items", IdentityField: "name",
		Schema:     map[string]dbviews.PropertyConfig{"name": {Type: "text"}, "amount": {Type: "number"}},
		Provenance: dataset.Provenance{ImportedAt: "2026-01-04T00:00:00Z", SourceName: "provider", SourceSHA256: "embed-test"}, Rows: rows,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.BaseSave(&dbviews.Base{ID: "items-base", Title: "Items Base", Views: []dbviews.View{{ID: "items-view", Name: "Items", Source: "dataset:items", Columns: []string{"name", "amount"}}}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ExecuteBaseEmbed("base: items-base\nview: items-view\nlimit: 3\ncolumns: [name, amount]")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 3 || result.TotalRows != 20 || !result.Capped || result.RowCap != 3 {
		t.Fatalf("unexpected embed result: %#v", result)
	}
}
