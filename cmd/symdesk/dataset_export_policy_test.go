package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/spf13/cobra"
)

func TestCLIExportPathsFailClosedForDeniedDatasetAndLeaveNoOutput(t *testing.T) {
	t.Setenv("SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY", dataset.SensitivityPublic)
	vaultRoot := t.TempDir()
	originalConfig := cfg
	cfg = &config.Config{Vault: vaultRoot}
	t.Cleanup(func() { cfg = originalConfig })

	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(vaultRoot, db)
	if _, err := svc.DatasetSync(service.DatasetSyncOptions{
		Slug: "orders", Title: "Orders", IdentityField: "id", Sensitivity: dataset.SensitivityConfidential,
		Provenance: dataset.Provenance{ImportedAt: "2026-09-04T00:00:00Z", SourceName: "feed", SourceSHA256: "cli-policy"},
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

	for _, tc := range []struct {
		name   string
		format string
		setup  func(*cobra.Command)
	}{
		{name: "view html", format: "html", setup: func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("view", "orders")
		}},
		{name: "view pdf", format: "pdf", setup: func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("view", "orders")
		}},
		{name: "dataset note html", format: "html", setup: func(cmd *cobra.Command) {
			_ = cmd.Flags().Set("note", "datasets/orders.md")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "denied."+tc.format)
			cmd := newExportCmd()
			tc.setup(cmd)
			_ = cmd.Flags().Set("format", tc.format)
			_ = cmd.Flags().Set("output", output)
			if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "dataset export denied") {
				t.Fatalf("CLI %s error = %v", tc.name, err)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("CLI %s created denied output: %v", tc.name, statErr)
			}
		})
	}

	viewsCmd := newViewsCmd()
	var csvCmd *cobra.Command
	for _, sub := range viewsCmd.Commands() {
		if sub.Name() == "export-csv" {
			csvCmd = sub
			break
		}
	}
	if csvCmd == nil {
		t.Fatal("views export-csv command not found")
	}
	csvOutput := filepath.Join(t.TempDir(), "denied.csv")
	_ = csvCmd.Flags().Set("output", csvOutput)
	if err := csvCmd.RunE(csvCmd, []string{"orders"}); err == nil || !strings.Contains(err.Error(), "dataset export denied") {
		t.Fatalf("CLI CSV export error = %v", err)
	}
	if _, statErr := os.Stat(csvOutput); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("CLI CSV export created denied output: %v", statErr)
	}
}
