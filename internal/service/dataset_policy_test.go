package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func datasetForPolicyTest(t *testing.T, svc *Service, sensitivity string) {
	t.Helper()
	_, err := svc.DatasetSync(DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id", Sensitivity: sensitivity,
		Provenance: dataset.Provenance{ImportedAt: "2026-01-04T00:00:00Z", SourceName: "feed", SourceSHA256: "policy-sha"},
		Rows:       []DatasetSyncRow{{Identity: "o1", Values: map[string]interface{}{"id": "o1", "amount": 12.5}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "orders", Name: "Orders", Type: "table", Source: "dataset:orders", Columns: []string{"id", "amount"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDatasetExportSensitivityGateAllowsAndDeniesCSV(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityConfidential)

	if _, err := svc.ViewsExportCSV("orders"); err == nil || !strings.Contains(err.Error(), "dataset export denied") {
		t.Fatalf("default export did not deny confidential dataset: %v", err)
	}
	if _, err := svc.Export("", "orders", filepath.Join(t.TempDir(), "orders.html"), "html", ""); err == nil || !strings.Contains(err.Error(), "dataset export denied") {
		t.Fatalf("HTML export did not deny confidential dataset: %v", err)
	}

	svc.Config = &config.Config{DatasetExportMaxSensitivity: dataset.SensitivityConfidential}
	csvData, err := svc.ViewsExportCSV("orders")
	if err != nil {
		t.Fatalf("configured confidential export was denied: %v", err)
	}
	if !strings.Contains(string(csvData), "id,amount") || !strings.Contains(string(csvData), "o1") {
		t.Fatalf("allowed CSV omitted dataset rows: %q", csvData)
	}
}

func TestDatasetPurgeAfterExplicitRetentionAcceptanceLeavesNoResidue(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	handleRel := "datasets/orders.md"
	rawRel := "datasets/orders/2026-01-04.csv"
	rawAbs, err := vault.SecurePath(svc.VaultRoot, rawRel)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(rawAbs) //nolint:gosec // test-owned vault path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.History.CheckpointFile("retention-task", handleRel); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.History.Trash(rawRel); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rawAbs, raw, 0600); err != nil { //nolint:gosec // test-owned vault path
		t.Fatal(err)
	}

	if err := svc.DatasetPurge("orders"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{handleRel, rawRel} {
		abs, err := vault.SecurePath(svc.VaultRoot, rel)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(abs); !os.IsNotExist(err) { //nolint:gosec // test-owned vault path
			t.Fatalf("dataset path %s still exists: %v", rel, err)
		}
		entries, err := svc.History.List(rel)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("history residue for %s: %#v", rel, entries)
		}
	}
	if rows, err := svc.DB.DatasetRows("orders"); err != nil || len(rows) != 0 {
		t.Fatalf("derived dataset rows remain: %d %v", len(rows), err)
	}
	trash, err := svc.History.TrashList()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range trash {
		if entry.OriginalPath == rawRel || entry.OriginalPath == handleRel {
			t.Fatalf("matching trash residue remains: %#v", entry)
		}
	}
	checkpoints, err := svc.History.ListCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("matching checkpoint residue remains: %#v", checkpoints)
	}
	objects, err := os.ReadDir(filepath.Join(svc.VaultRoot, ".symdesk", "history", "objects"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("unreferenced history blobs remain: %#v", objects)
	}
}

func TestGenericSearchExcludesDatasetRowsWhileDatasetQuerySelectsThem(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityInternal)
	response, err := svc.SearchWithMeta("12.5")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("generic AI/search context exposed dataset rows: %#v", response.Results)
	}
	query, err := svc.DatasetQuery("orders", DatasetQueryOptions{Columns: []string{"amount"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if query.ReturnedRows != 1 || query.Rows[0]["amount"] != 12.5 {
		t.Fatalf("explicit dataset query did not expose selected row: %#v", query)
	}
}
