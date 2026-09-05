package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
)

func legacyDatasetHandle(t *testing.T, svc *Service, name, policy string) (string, []byte) {
	t.Helper()
	path := filepath.Join(svc.VaultRoot, dataset.RawDir, name+".md")
	content := []byte("---\n" +
		"type: dataset\n" +
		"title: Orders\n" +
		"dataset_id: orders\n" +
		"source: datasets/orders/source.csv\n" +
		"custom_policy: keep-me\n" +
		policy +
		"---\n\n# Preserve this body\n")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func TestDatasetListMigratesLegacyPolicyAtomicallyAndIdempotently(t *testing.T) {
	svc := newTestService(t)
	path, original := legacyDatasetHandle(t, svc, "orders", "")

	list, err := svc.DatasetList()
	if err != nil {
		t.Fatalf("legacy DatasetList failed: %v", err)
	}
	if len(list) != 1 || list[0].Sensitivity != dataset.DefaultSensitivity || list[0].RetentionRule != dataset.DefaultRetentionRule {
		t.Fatalf("legacy dataset summary = %#v", list)
	}
	migrated, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migrated), "sensitivity: \"restricted\"\n") || !strings.Contains(string(migrated), "retention_rule: \"default\"\n") {
		t.Fatalf("migrated policy fields missing: %s", migrated)
	}
	if !strings.Contains(string(migrated), "custom_policy: keep-me\n") || !strings.Contains(string(migrated), "# Preserve this body\n") {
		t.Fatalf("migration did not preserve unrelated content: %s", migrated)
	}
	if bytes.Equal(migrated, original) {
		t.Fatal("legacy handle was not persisted")
	}

	beforeSecondRead := append([]byte(nil), migrated...)
	list, err = svc.DatasetList()
	if err != nil {
		t.Fatalf("second DatasetList failed: %v", err)
	}
	if len(list) != 1 || list[0].Sensitivity != dataset.DefaultSensitivity || list[0].RetentionRule != dataset.DefaultRetentionRule {
		t.Fatalf("migrated dataset summary = %#v", list)
	}
	afterSecondRead, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterSecondRead, beforeSecondRead) {
		t.Fatal("policy migration was not idempotent")
	}
}

func TestDatasetListSurfacesPartialPolicyDiagnostics(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.DatasetSync(validDatasetSyncOptions()); err != nil {
		t.Fatal(err)
	}
	_, _ = legacyDatasetHandle(t, svc, "partial", "sensitivity: internal\n")

	list, err := svc.DatasetList()
	if err == nil || !strings.Contains(err.Error(), "datasets/partial.md") || !strings.Contains(err.Error(), "retention_rule") {
		t.Fatalf("partial policy diagnostics = list %#v, err %v", list, err)
	}
	if len(list) != 1 || list[0].Slug != "events" {
		t.Fatalf("valid dataset disappeared with partial handle: %#v", list)
	}
}

func TestDatasetListSurfacesPolicyMigrationWriteFailure(t *testing.T) {
	svc := newTestService(t)
	path, original := legacyDatasetHandle(t, svc, "orders", "")
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil { //nolint:gosec // test-owned directory permissions simulate a write failure
		t.Skipf("cannot make migration directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) }) //nolint:gosec // restore test-owned directory permissions

	list, err := svc.DatasetList()
	if err == nil || !strings.Contains(err.Error(), "datasets/orders.md") || !strings.Contains(err.Error(), "migrat") {
		t.Fatalf("migration write failure = list %#v, err %v", list, err)
	}
	unchanged, readErr := os.ReadFile(path) //nolint:gosec // test-owned path
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(unchanged, original) {
		t.Fatal("failed migration changed the handle")
	}
}
