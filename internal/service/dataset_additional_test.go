package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func validDatasetSyncOptions() DatasetSyncOptions {
	return DatasetSyncOptions{
		Title: "Events", Slug: "events", IdentityField: "event_id",
		Provenance: dataset.Provenance{ImportedAt: "2026-02-03T04:05:06Z", SourceName: "provider", SourceSHA256: "sha-1"},
		Rows:       []DatasetSyncRow{{Identity: "evt-1", Values: map[string]interface{}{"event_id": "evt-1", "amount": 2.5}}},
	}
}

func TestDatasetSyncRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	base := validDatasetSyncOptions()
	tests := []struct {
		name string
		make func() (*Service, DatasetSyncOptions)
		want string
	}{
		{"nil service", func() (*Service, DatasetSyncOptions) { return nil, base }, "requires a vault"},
		{"missing vault", func() (*Service, DatasetSyncOptions) { return &Service{DB: &sidecar.DB{}}, base }, "requires a vault"},
		{"missing sidecar", func() (*Service, DatasetSyncOptions) { return &Service{VaultRoot: t.TempDir()}, base }, "requires a vault and sidecar"},
		{"invalid sensitivity", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Sensitivity = "secret"; return o }()
		}, "invalid dataset sensitivity"},
		{"missing identity field", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.IdentityField = " "; return o }()
		}, "identity field"},
		{"missing source name", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Provenance.SourceName = ""; return o }()
		}, "explicit provenance"},
		{"missing source hash", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Provenance.SourceSHA256 = ""; return o }()
		}, "explicit provenance"},
		{"missing imported at", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Provenance.ImportedAt = ""; return o }()
		}, "explicit provenance"},
		{"invalid imported at", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Provenance.ImportedAt = "yesterday"; return o }()
		}, "invalid imported_at"},
		{"unsafe slug", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Slug = "Events 2026"; return o }()
		}, "filesystem-safe"},
		{"no rows", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Rows = nil; return o }()
		}, "at least one row"},
		{"blank row identity", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions {
				o := base
				o.Rows = []DatasetSyncRow{{Identity: " ", Values: map[string]interface{}{}}}
				return o
			}()
		}, "identity for every row"},
		{"duplicate row identity", func() (*Service, DatasetSyncOptions) {
			return newTestService(t), func() DatasetSyncOptions { o := base; o.Rows = append(o.Rows, o.Rows[0]); return o }()
		}, "duplicate dataset row identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, opts := tt.make()
			_, err := svc.DatasetSync(opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DatasetSync error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSyncValueStringSupportsProducerTypesAndReportsMarshalErrors(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{"nil", nil, ""}, {"string", "hello", "hello"}, {"bool", true, "true"},
		{"float64", float64(1.25), "1.25"}, {"float32", float32(2.5), "2.5"},
		{"int", 7, "7"}, {"int64", int64(8), "8"}, {"json", []string{"a", "b"}, `["a","b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := syncValueString(tt.value)
			if err != nil || got != tt.want {
				t.Fatalf("syncValueString(%#v) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
	if _, err := syncValueString(make(chan int)); err == nil {
		t.Fatal("unsupported channel value was marshaled successfully")
	}
	if _, _, err := syncCSV([]DatasetSyncRow{{Identity: "x", Values: map[string]interface{}{"bad": make(chan int)}}}, "id", nil); err == nil || !strings.Contains(err.Error(), `row "x" column "bad"`) {
		t.Fatalf("syncCSV unsupported value error = %v", err)
	}
}

func TestDatasetSyncPreservesExistingMetadataWhenRefreshing(t *testing.T) {
	svc := newTestService(t)
	firstOpts := validDatasetSyncOptions()
	firstOpts.Slug = "events"
	first, err := svc.DatasetSync(firstOpts)
	if err != nil {
		t.Fatal(err)
	}
	handleAbs, err := vault.SecurePath(svc.VaultRoot, first.HandlePath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := dataset.ParseHandle(first.HandlePath, mustReadFile(t, handleAbs))
	if err != nil {
		t.Fatal(err)
	}
	original.Created = "2025-01-01T00:00:00Z"
	original.Coverage = dataset.Coverage{From: "2025-01-01", To: "2025-01-31"}
	original.RefreshCommand = "refresh-events"
	encoded, err := original.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handleAbs, encoded, 0600); err != nil {
		t.Fatal(err)
	}

	refresh := firstOpts
	refresh.Provenance.SourceSHA256 = "sha-2"
	refresh.Title = ""
	refresh.Rows = []DatasetSyncRow{{Identity: "evt-2", Values: map[string]interface{}{"event_id": "evt-2", "amount": 4}}}
	result, err := svc.DatasetSync(refresh)
	if err != nil {
		t.Fatal(err)
	}
	if result.Idempotent || result.Rows != 2 {
		t.Fatalf("unexpected refresh result: %#v", result)
	}
	updated, err := dataset.ParseHandle(first.HandlePath, mustReadFile(t, handleAbs))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Events" || updated.Created != original.Created || updated.Coverage != original.Coverage || updated.RefreshCommand != original.RefreshCommand {
		t.Fatalf("existing metadata was not preserved: %#v", updated)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDatasetImportValidatesSourceAndSlugAndUsesExplicitPolicy(t *testing.T) {
	root := t.TempDir()
	db, err := sidecar.Open(filepath.Join(root, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(root, db)
	missing := filepath.Join(root, "missing.csv")
	if _, err := svc.DatasetImport(missing, DatasetImportOptions{}); err == nil || !strings.Contains(err.Error(), "stat dataset source") {
		t.Fatalf("missing source error = %v", err)
	}
	dir := filepath.Join(root, "input")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DatasetImport(dir, DatasetImportOptions{}); err == nil || !strings.Contains(err.Error(), "CSV files only") {
		t.Fatalf("directory source error = %v", err)
	}
	txt := filepath.Join(root, "input.txt")
	if err := os.WriteFile(txt, []byte("id\n1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DatasetImport(txt, DatasetImportOptions{}); err == nil || !strings.Contains(err.Error(), "CSV files only") {
		t.Fatalf("non-CSV source error = %v", err)
	}
	bad := filepath.Join(root, "bad.csv")
	if err := os.WriteFile(bad, []byte("id,amount\n1,nope\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DatasetImport(bad, DatasetImportOptions{Schema: map[string]dbviews.PropertyConfig{"amount": {Type: "number"}}}); err == nil || !strings.Contains(err.Error(), "invalid number") {
		t.Fatalf("invalid CSV error = %v", err)
	}
	good := filepath.Join(root, "feed.csv")
	if err := os.WriteFile(good, []byte("id,amount\n1,3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DatasetImport(good, DatasetImportOptions{Slug: "Bad Slug"}); err == nil || !strings.Contains(err.Error(), "filesystem-safe") {
		t.Fatalf("unsafe import slug error = %v", err)
	}
	result, err := svc.DatasetImport(good, DatasetImportOptions{Sensitivity: " CONFIDENTIAL ", RetentionRule: "  financial-7y  ", Now: time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Slug != "feed" || result.Sensitivity != dataset.SensitivityConfidential || result.RetentionRule != "financial-7y" || result.Rows != 1 {
		t.Fatalf("unexpected explicit-policy import: %#v", result)
	}
}

func TestDatasetPolicyExportChecksNilViewsNonDatasetAndConfiguredVocabulary(t *testing.T) {
	svc := newTestService(t)
	if err := svc.checkDatasetExport(nil); err == nil || !strings.Contains(err.Error(), "requires a view") {
		t.Fatalf("nil view error = %v", err)
	}
	if err := svc.checkDatasetExport(&dbviews.View{Source: "notes"}); err != nil {
		t.Fatalf("non-dataset view rejected: %v", err)
	}
	if err := svc.checkDatasetExport(&dbviews.View{Source: "dataset:"}); err == nil || !strings.Contains(err.Error(), "requires a dataset slug") {
		t.Fatalf("empty dataset slug error = %v", err)
	}
	if err := svc.checkDatasetExport(&dbviews.View{Source: "dataset:missing"}); err == nil || !strings.Contains(err.Error(), "dataset export policy") {
		t.Fatalf("missing handle error = %v", err)
	}
	if datasetSensitivityRank(dataset.SensitivityPublic) != 0 || datasetSensitivityRank(dataset.SensitivityInternal) != 1 || datasetSensitivityRank(dataset.SensitivityConfidential) != 2 || datasetSensitivityRank(dataset.SensitivityRestricted) != 3 || datasetSensitivityRank("bad") != -1 {
		t.Fatal("sensitivity rank ordering changed")
	}

	for _, sensitivity := range []string{dataset.SensitivityPublic, dataset.SensitivityInternal, dataset.SensitivityConfidential, dataset.SensitivityRestricted} {
		handle := &dataset.Handle{Slug: "matrix", Title: "Matrix", Source: "datasets/matrix/source.csv", Sensitivity: sensitivity, RetentionRule: dataset.DefaultRetentionRule}
		encoded, err := handle.Render()
		if err != nil {
			t.Fatal(err)
		}
		path, err := vault.SecurePath(svc.VaultRoot, "datasets/matrix.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		for _, max := range []string{dataset.SensitivityPublic, dataset.SensitivityInternal, dataset.SensitivityConfidential, dataset.SensitivityRestricted} {
			svc.Config = &config.Config{DatasetExportMaxSensitivity: max}
			err := svc.checkDatasetExport(&dbviews.View{Source: "dataset:matrix"})
			allowed := datasetSensitivityRank(sensitivity) <= datasetSensitivityRank(max)
			if allowed && err != nil || !allowed && (err == nil || !strings.Contains(err.Error(), "dataset export denied")) {
				t.Errorf("sensitivity=%s max=%s allowed=%v err=%v", sensitivity, max, allowed, err)
			}
		}
	}
	svc.Config = &config.Config{DatasetExportMaxSensitivity: "invalid"}
	if err := svc.checkDatasetExport(&dbviews.View{Source: "dataset:matrix"}); err == nil || !strings.Contains(err.Error(), "invalid dataset sensitivity") {
		t.Fatalf("invalid configured export maximum error = %v", err)
	}
}

func TestDatasetPurgeRejectsUnsafeAndNonRegularPaths(t *testing.T) {
	svc := newTestService(t)
	for _, slug := range []string{"", "../orders", "Bad Slug", "orders/other"} {
		if err := svc.DatasetPurge(slug, dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "filesystem-safe") {
			t.Errorf("unsafe purge slug %q error = %v", slug, err)
		}
	}
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil {
		t.Fatal("purge without datasets directory succeeded")
	}

	fixture := func(t *testing.T) *Service {
		t.Helper()
		s := newTestService(t)
		datasetForPolicyTest(t, s, dataset.SensitivityRestricted)
		return s
	}
	t.Run("missing handle", func(t *testing.T) {
		s := newTestService(t)
		dir := filepath.Join(s.VaultRoot, "datasets")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := s.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "dataset purge") {
			t.Fatalf("missing handle error = %v", err)
		}
	})
	t.Run("directory handle", func(t *testing.T) {
		s := newTestService(t)
		dir := filepath.Join(s.VaultRoot, "datasets")
		if err := os.MkdirAll(filepath.Join(dir, "orders.md"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := s.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("directory handle error = %v", err)
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("symlink handle", func(t *testing.T) {
			s := fixture(t)
			handle := filepath.Join(s.VaultRoot, "datasets", "orders.md")
			backup := filepath.Join(s.VaultRoot, "handle-backup.md")
			if err := os.Rename(handle, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(backup, handle); err != nil {
				t.Fatal(err)
			}
			if err := s.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlink handle error = %v", err)
			}
		})
	}
	t.Run("regular raw file", func(t *testing.T) {
		s := fixture(t)
		rawDir := filepath.Join(s.VaultRoot, "datasets", "orders")
		if err := os.RemoveAll(rawDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rawDir, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "must be a directory") {
			t.Fatalf("regular raw path error = %v", err)
		}
	})
	t.Run("nonregular raw entry", func(t *testing.T) {
		s := fixture(t)
		if err := os.Mkdir(filepath.Join(s.VaultRoot, "datasets", "orders", "nested"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := s.DatasetPurge("orders", dataset.DefaultRetentionRule); err == nil || !strings.Contains(err.Error(), "non-regular entry") {
			t.Fatalf("nonregular raw entry error = %v", err)
		}
	})
}

func TestDatasetPurgeAllowsMissingRawDirectory(t *testing.T) {
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	if err := os.RemoveAll(filepath.Join(svc.VaultRoot, "datasets", "orders")); err != nil {
		t.Fatal(err)
	}
	if err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "datasets", "orders.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("handle still exists after purge: %v", err)
	}
}
