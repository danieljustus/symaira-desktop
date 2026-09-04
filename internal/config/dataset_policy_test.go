package config

import "testing"

func TestDatasetExportSensitivityDefaultsAndValidation(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DatasetExportMaxSensitivity != "internal" {
		t.Fatalf("default dataset export maximum = %q, want internal", cfg.DatasetExportMaxSensitivity)
	}
	cfg.DatasetExportMaxSensitivity = "restricted"
	for _, finding := range cfg.Validate() {
		if finding.Field == "dataset_export_max_sensitivity" {
			t.Fatalf("valid restricted export maximum produced finding: %#v", finding)
		}
	}
	cfg.DatasetExportMaxSensitivity = "secret"
	found := false
	for _, finding := range cfg.Validate() {
		if finding.Field == "dataset_export_max_sensitivity" {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid dataset export maximum was not reported")
	}
}
