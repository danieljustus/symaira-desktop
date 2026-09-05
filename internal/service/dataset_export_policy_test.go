package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

func TestDatasetExportPolicyFullMatrixAcrossViewAndNote(t *testing.T) {
	sensitivities := []string{
		dataset.SensitivityPublic,
		dataset.SensitivityInternal,
		dataset.SensitivityConfidential,
		dataset.SensitivityRestricted,
	}
	for _, sensitivity := range sensitivities {
		for _, maximum := range sensitivities {
			t.Run(sensitivity+"_through_"+maximum, func(t *testing.T) {
				svc := newTestService(t)
				datasetForPolicyTest(t, svc, sensitivity)
				svc.Config = &config.Config{DatasetExportMaxSensitivity: maximum}
				allowed := datasetSensitivityRank(sensitivity) <= datasetSensitivityRank(maximum)

				viewHTML := filepath.Join(t.TempDir(), "view.html")
				_, viewErr := svc.Export("", "orders", viewHTML, "html", "")
				assertDatasetExportResult(t, allowed, viewErr, viewHTML)

				noteHTML := filepath.Join(t.TempDir(), "note.html")
				_, noteErr := svc.Export("datasets/orders.md", "", noteHTML, "html", "")
				assertDatasetExportResult(t, allowed, noteErr, noteHTML)

				csvData, csvErr := svc.ViewsExportCSV("orders")
				if allowed {
					if csvErr != nil || !strings.Contains(string(csvData), "o1") {
						t.Fatalf("allowed CSV export = %q, %v", csvData, csvErr)
					}
				} else if csvErr == nil || !strings.Contains(csvErr.Error(), "dataset export denied") || csvData != nil {
					t.Fatalf("denied CSV export = %q, %v", csvData, csvErr)
				}

				if !allowed {
					pdfOutput := filepath.Join(t.TempDir(), "denied.pdf")
					_, pdfErr := svc.Export("", "orders", pdfOutput, "pdf", "")
					assertDatasetExportResult(t, false, pdfErr, pdfOutput)
				}
			})
		}
	}
}

func TestDatasetExportPolicyFailsClosedForMissingMalformedAndRawHandles(t *testing.T) {
	tests := []struct {
		name       string
		handlePath string
		handle     string
		source     string
		notePath   string
	}{
		{name: "missing", source: "dataset:missing", notePath: "datasets/missing.md"},
		{name: "malformed", handlePath: "datasets/malformed.md", handle: "---\ntype: note\ntitle: Malformed\n---\n", source: "dataset:malformed", notePath: "datasets/malformed.md"},
		{name: "handle slug mismatch", handlePath: "datasets/mismatch.md", handle: "---\ntype: dataset\ntitle: Mismatch\ndataset_id: other\nsource: feed\nsensitivity: internal\nretention_rule: default\n---\n", source: "dataset:mismatch", notePath: "datasets/mismatch.md"},
		{name: "missing raw handle", source: "dataset:missing/raw.csv", notePath: "datasets/missing/raw.csv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			if tt.handlePath != "" {
				path := filepath.Join(svc.VaultRoot, tt.handlePath)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tt.handle), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.ViewsMgr.Save(dbviews.View{ID: tt.name, Name: tt.name, Type: "table", Source: tt.source}); err != nil {
				t.Fatal(err)
			}
			svc.Config = &config.Config{DatasetExportMaxSensitivity: dataset.SensitivityRestricted}

			for _, format := range []string{"html", "pdf"} {
				output := filepath.Join(t.TempDir(), tt.name+"."+format)
				_, err := svc.Export("", tt.name, output, format, "")
				if err == nil || !strings.Contains(err.Error(), "dataset export policy") {
					t.Fatalf("%s view export error = %v", format, err)
				}
				assertNoDatasetOutput(t, output)
			}

			csvOutput := filepath.Join(t.TempDir(), tt.name+".csv")
			_, err := svc.Export("", tt.name, csvOutput, "csv", "")
			if err == nil || !strings.Contains(err.Error(), "dataset export policy") {
				t.Fatalf("CSV view export error = %v", err)
			}
			assertNoDatasetOutput(t, csvOutput)

			noteOutput := filepath.Join(t.TempDir(), tt.name+"-note.html")
			_, err = svc.Export(tt.notePath, "", noteOutput, "html", "")
			if err == nil || !strings.Contains(err.Error(), "dataset export policy") {
				t.Fatalf("note export error = %v", err)
			}
			assertNoDatasetOutput(t, noteOutput)
		})
	}
}

func TestDatasetExportPathRejectsUnsafeDatasetSlugs(t *testing.T) {
	svc := newTestService(t)
	for _, relPath := range []string{"datasets/", "datasets/.md", "datasets/../orders.md", "datasets/orders/raw.csv", "datasets/orders.md/extra"} {
		err := svc.checkDatasetExportPath(relPath)
		if err == nil || (!strings.Contains(err.Error(), "requires a dataset slug") && !strings.Contains(err.Error(), "filesystem-safe") && !strings.Contains(err.Error(), "dataset export policy")) {
			t.Errorf("unsafe dataset path %q error = %v", relPath, err)
		}
	}
	for _, source := range []string{"dataset:", "dataset:../orders", "dataset:orders.md", "dataset:orders/raw"} {
		err := svc.checkDatasetExport(&dbviews.View{Source: source})
		if err == nil || !strings.Contains(err.Error(), "dataset export") {
			t.Errorf("unsafe dataset source %q error = %v", source, err)
		}
	}
}

func assertDatasetExportResult(t *testing.T, allowed bool, err error, output string) {
	t.Helper()
	if allowed {
		if err != nil {
			t.Fatalf("allowed export failed: %v", err)
		}
		if _, statErr := os.Stat(output); statErr != nil {
			t.Fatalf("allowed export did not create %s: %v", output, statErr)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "dataset export denied") {
		t.Fatalf("denied export error = %v", err)
	}
	assertNoDatasetOutput(t, output)
}

func assertNoDatasetOutput(t *testing.T, output string) {
	t.Helper()
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied dataset export created %s: %v", output, err)
	}
}
