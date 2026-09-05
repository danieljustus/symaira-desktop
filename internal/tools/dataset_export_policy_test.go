package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func TestDeskExportCannotBypassDatasetPolicyOrCreateDeniedOutput(t *testing.T) {
	t.Setenv("SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY", dataset.SensitivityPublic)
	factory := testServiceFactory(t)
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DatasetSync(service.DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id", Sensitivity: dataset.SensitivityConfidential,
		Provenance: dataset.Provenance{ImportedAt: "2026-09-04T00:00:00Z", SourceName: "feed", SourceSHA256: "tool-policy"},
		Rows:       []service.DatasetSyncRow{{Identity: "o1", Values: map[string]interface{}{"id": "o1"}}},
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "orders", Name: "Orders", Type: "table", Source: "dataset:orders", Columns: []string{"id"}}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"html", "csv", "pdf"} {
		t.Run(format, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "denied."+format)
			input, err := json.Marshal(map[string]string{"view": "orders", "format": format, "output": output})
			if err != nil {
				t.Fatal(err)
			}
			_, err = newExportTool(factory).Handler(context.Background(), input)
			if err == nil || !strings.Contains(err.Error(), "dataset export denied") {
				t.Fatalf("desk_export %s error = %v", format, err)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("desk_export %s created denied output: %v", format, statErr)
			}
		})
	}

	noteOutput := filepath.Join(t.TempDir(), "denied-note.html")
	input, _ := json.Marshal(map[string]string{"note": "datasets/orders.md", "format": "html", "output": noteOutput})
	if _, err := newExportTool(factory).Handler(context.Background(), input); err == nil || !strings.Contains(err.Error(), "dataset export denied") {
		t.Fatalf("desk_export dataset handle error = %v", err)
	}
	if _, err := os.Stat(noteOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("desk_export created denied dataset-note output: %v", err)
	}
}

func TestDeskExportFailsClosedForMissingAndMalformedDatasetHandles(t *testing.T) {
	t.Setenv("SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY", dataset.SensitivityRestricted)
	for _, tc := range []struct {
		name   string
		source string
		handle string
	}{
		{name: "missing", source: "dataset:missing"},
		{name: "malformed", source: "dataset:malformed", handle: "---\ntype: note\ntitle: Malformed\n---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := testServiceFactory(t)
			svc, db, err := factory()
			if err != nil {
				t.Fatal(err)
			}
			if tc.handle != "" {
				path := filepath.Join(svc.VaultRoot, "datasets", tc.name+".md")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.handle), 0600); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
			}
			if err := svc.ViewsMgr.Save(dbviews.View{ID: tc.name, Name: tc.name, Type: "table", Source: tc.source}); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "denied.html")
			input, _ := json.Marshal(map[string]string{"view": tc.name, "format": "html", "output": output})
			if _, err := newExportTool(factory).Handler(context.Background(), input); err == nil || !strings.Contains(err.Error(), "dataset export policy") {
				t.Fatalf("desk_export %s error = %v", tc.name, err)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("desk_export created output for %s: %v", tc.name, err)
			}
		})
	}
}
