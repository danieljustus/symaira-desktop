package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestDatasetPurgeResumesAfterClosedSidecarWithNewService(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	dbPath := filepath.Join(svc.VaultRoot, "sidecar.db")
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed sidecar purge error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders.md")); err != nil {
		t.Fatalf("closed sidecar phase removed active handle: %v", err)
	}
	journal, err := loadDatasetPurgeJournal(mustPurgeRoot(t, svc.VaultRoot), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != datasetPurgePhaseSidecar || journal.Status != datasetPurgeStatusFailed {
		t.Fatalf("unexpected failed journal: %#v", journal)
	}

	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	resumed := New(svc.VaultRoot, db)
	if err := resumed.DatasetPurge("orders", dataset.DefaultRetentionRule); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resumed purge left handle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, datasetPurgeJournalPath("orders"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed purge left journal: %v", err)
	}
}

func TestDatasetPurgeRetriesFilesystemRemovalFailure(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	original := DatasetPurgeRemove
	calls := 0
	DatasetPurgeRemove = func(root *os.Root, relPath string) error {
		calls++
		if calls == 1 {
			return errors.New("injected removal failure")
		}
		return original(root, relPath)
	}
	t.Cleanup(func() { DatasetPurgeRemove = original })
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("injected purge error = %v", err)
	}
	journal, err := loadDatasetPurgeJournal(mustPurgeRoot(t, svc.VaultRoot), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != datasetPurgePhaseActive || journal.Status != datasetPurgeStatusFailed {
		t.Fatalf("unexpected removal-failure journal: %#v", journal)
	}
	DatasetPurgeRemove = original
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry left handle: %v", err)
	}
}

func TestDatasetPurgeRejectsTamperedJournalForeignPath(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil {
		t.Fatal("closed sidecar unexpectedly completed purge")
	}
	journalPath := filepath.Join(svc.VaultRoot, datasetPurgeJournalPath("orders"))
	data, err := os.ReadFile(journalPath) //nolint:gosec // test-owned vault path
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), `"path": "datasets/orders.md"`, `"path": "../foreign.md"`, 1)
	if updated == string(data) {
		t.Fatal("journal path fixture was not found")
	}
	if err := os.WriteFile(journalPath, []byte(updated), 0o600); err != nil { //nolint:gosec // test-owned vault path
		t.Fatal(err)
	}
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "outside dataset") {
		t.Fatalf("tampered journal error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders.md")); err != nil {
		t.Fatalf("tampered journal changed active handle: %v", err)
	}
}

func TestDatasetPurgeRejectsReplacementAtRecordedPath(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	original := DatasetPurgeRemove
	calls := 0
	DatasetPurgeRemove = func(root *os.Root, relPath string) error {
		calls++
		if calls == 2 {
			return errors.New("injected active removal failure")
		}
		return original(root, relPath)
	}
	t.Cleanup(func() { DatasetPurgeRemove = original })
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("injected purge error = %v", err)
	}
	journal, err := loadDatasetPurgeJournal(mustPurgeRoot(t, svc.VaultRoot), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != datasetPurgePhaseActive || journal.Status != datasetPurgeStatusFailed {
		t.Fatalf("unexpected removal-failure journal: %#v", journal)
	}
	// The handle was removed by the first successful call. Recreate a remaining
	// recorded source with different content and ensure retry fails closed.
	raw := filepath.Join(svc.VaultRoot, "datasets", "orders", "2026-01-04.csv")
	if err := os.Remove(raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(raw, []byte("replacement"), 0o600); err != nil { //nolint:gosec // test-owned vault path
		t.Fatal(err)
	}
	DatasetPurgeRemove = original
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("replacement path error = %v", err)
	}
	if data, err := os.ReadFile(raw); err != nil || string(data) != "replacement" { //nolint:gosec // test-owned vault path
		t.Fatalf("replacement was removed: %q %v", data, err)
	}
}

func TestDatasetPurgeCorruptRecoveryLeavesActiveStateUntouched(t *testing.T) {
	tests := []struct {
		name    string
		makeBad func(t *testing.T, root string)
	}{
		{name: "null history", makeBad: func(t *testing.T, root string) {
			path := filepath.Join(root, ".symdesk", "history", "manifest", "datasets", "orders.md.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("null"), 0o600); err != nil { //nolint:gosec // test-owned vault path
				t.Fatal(err)
			}
		}},
		{name: "orphan trash", makeBad: func(t *testing.T, root string) {
			path := filepath.Join(root, ".symdesk", "trash", "orphan")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("orphan"), 0o600); err != nil { //nolint:gosec // test-owned vault path
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
			rows, err := svc.DB.DatasetRows("orders")
			if err != nil || len(rows) != 1 {
				t.Fatalf("dataset setup rows = %d %v", len(rows), err)
			}
			tt.makeBad(t, svc.VaultRoot)
			if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "preflight") {
				t.Fatalf("corrupt recovery state error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders.md")); err != nil {
				t.Fatalf("corrupt recovery state removed handle: %v", err)
			}
			if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders", "2026-01-04.csv")); err != nil {
				t.Fatalf("corrupt recovery state removed raw file: %v", err)
			}
			if after, err := svc.DB.DatasetRows("orders"); err != nil || len(after) != 1 {
				t.Fatalf("corrupt recovery state changed rows: %d %v", len(after), err)
			}
			if _, err := os.Stat(filepath.Join(svc.VaultRoot, datasetPurgeJournalPath("orders"))); !os.IsNotExist(err) {
				t.Fatalf("failed preflight persisted a journal: %v", err)
			}
		})
	}
}

func TestDatasetPurgeRejectsReplacementTrashAfterJournal(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	rawRel := "datasets/orders/2026-01-04.csv"
	entry, err := svc.History.Trash(rawRel)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil {
		t.Fatal("closed sidecar unexpectedly completed purge")
	}
	trashPath := filepath.Join(svc.VaultRoot, ".symdesk", "trash", entry.Name)
	if err := os.WriteFile(trashPath, []byte("replacement"), 0o600); err != nil { //nolint:gosec // test-owned vault path
		t.Fatal(err)
	}
	db, err := sidecar.Open(filepath.Join(svc.VaultRoot, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	resumed := New(svc.VaultRoot, db)
	if err := resumed.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "content changed") {
		t.Fatalf("replacement trash error = %v", err)
	}
	if data, err := os.ReadFile(trashPath); err != nil || string(data) != "replacement" { //nolint:gosec // test-owned vault path
		t.Fatalf("replacement trash was removed: %q %v", data, err)
	}
	if entries, err := resumed.History.List(rawRel); err != nil || len(entries) == 0 {
		t.Fatalf("replacement trash failure removed recovery history: %#v %v", entries, err)
	}
}

func mustPurgeRoot(t *testing.T, vaultRoot string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}
