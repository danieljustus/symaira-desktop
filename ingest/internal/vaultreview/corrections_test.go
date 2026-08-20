package vaultreview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyCorrectionsFile(t *testing.T) {
	vault := t.TempDir()
	dir := t.TempDir()
	correctionsPath := filepath.Join(dir, "corrections.yaml")

	// Write a corrections file with no corrections.
	content := "schema_version: 1\ncorrections: []\n"
	if err := os.WriteFile(correctionsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := ApplyCorrectionsFile(vault, correctionsPath, false)
	if err != nil {
		t.Fatalf("ApplyCorrectionsFile: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestApplyCorrectionsFile_DryRun(t *testing.T) {
	vault := t.TempDir()
	dir := t.TempDir()
	correctionsPath := filepath.Join(dir, "corrections.yaml")

	content := "schema_version: 1\ncorrections: []\n"
	if err := os.WriteFile(correctionsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := ApplyCorrectionsFile(vault, correctionsPath, true)
	if err != nil {
		t.Fatalf("ApplyCorrectionsFile: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestApplyCorrectionsFile_MissingFile(t *testing.T) {
	vault := t.TempDir()
	_, err := ApplyCorrectionsFile(vault, filepath.Join(t.TempDir(), "nonexistent.yaml"), false)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestApplyCorrectionsFile_InvalidYAML(t *testing.T) {
	vault := t.TempDir()
	dir := t.TempDir()
	correctionsPath := filepath.Join(dir, "bad.yaml")

	if err := os.WriteFile(correctionsPath, []byte("{invalid yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyCorrectionsFile(vault, correctionsPath, false)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestApplyCorrectionsFileWithOptions_RequireCount(t *testing.T) {
	vault := t.TempDir()
	dir := t.TempDir()
	correctionsPath := filepath.Join(dir, "corrections.yaml")

	content := "schema_version: 1\ncorrections: []\n"
	if err := os.WriteFile(correctionsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Require 1 correction but have 0.
	_, err := ApplyCorrectionsFileWithOptions(vault, correctionsPath, ApplyOptions{RequireCount: 1})
	if err == nil {
		t.Fatal("expected error for count mismatch")
	}
}

func TestApplyCorrectionsFileWithOptions_Max(t *testing.T) {
	vault := t.TempDir()
	dir := t.TempDir()
	correctionsPath := filepath.Join(dir, "corrections.yaml")

	content := `schema_version: 1
corrections:
  - paperless_id: 1
  - paperless_id: 2
`
	if err := os.WriteFile(correctionsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Max 1 correction but have 2.
	_, err := ApplyCorrectionsFileWithOptions(vault, correctionsPath, ApplyOptions{Max: 1})
	if err == nil {
		t.Fatal("expected error for exceeding max")
	}
}

func TestBulkUpdateByTag(t *testing.T) {
	vault := t.TempDir()

	// Empty vault should return no results.
	results, err := BulkUpdateByTag(vault, "test", Correction{}, false)
	if err != nil {
		t.Fatalf("BulkUpdateByTag: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestBulkUpdateByTag_DryRun(t *testing.T) {
	vault := t.TempDir()

	results, err := BulkUpdateByTag(vault, "test", Correction{}, true)
	if err != nil {
		t.Fatalf("BulkUpdateByTag: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestBulkUpdateByTagWithOptions(t *testing.T) {
	vault := t.TempDir()

	results, err := BulkUpdateByTagWithOptions(vault, "test", Correction{}, BulkUpdateOptions{DryRun: true})
	if err != nil {
		t.Fatalf("BulkUpdateByTagWithOptions: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestAddIDFindings(t *testing.T) {
	report := &ReviewReport{}
	ids := []int{1, 2, 3}

	addIDFindings(report, "test_kind", ids)

	if len(report.Findings) != 3 {
		t.Fatalf("len(Findings) = %d, want 3", len(report.Findings))
	}

	for i, id := range ids {
		if report.Findings[i].ID != id {
			t.Errorf("Findings[%d].ID = %d, want %d", i, report.Findings[i].ID, id)
		}
		if report.Findings[i].Kind != "test_kind" {
			t.Errorf("Findings[%d].Kind = %q, want test_kind", i, report.Findings[i].Kind)
		}
	}
}

func TestAddIDFindings_Empty(t *testing.T) {
	report := &ReviewReport{}
	addIDFindings(report, "test_kind", nil)

	if len(report.Findings) != 0 {
		t.Errorf("len(Findings) = %d, want 0", len(report.Findings))
	}
}

func TestAddDocFindings(t *testing.T) {
	report := &ReviewReport{}
	ids := []int{1, 2}

	addDocFindings(report, "missing", ids)

	if len(report.Findings) != 2 {
		t.Fatalf("len(Findings) = %d, want 2", len(report.Findings))
	}
	if len(report.Documents) != 2 {
		t.Fatalf("len(Documents) = %d, want 2", len(report.Documents))
	}

	for i, id := range ids {
		if report.Findings[i].ID != id {
			t.Errorf("Findings[%d].ID = %d, want %d", i, report.Findings[i].ID, id)
		}
		if report.Documents[i].ID != id {
			t.Errorf("Documents[%d].ID = %d, want %d", i, report.Documents[i].ID, id)
		}
		if report.Documents[i].Status != "missing" {
			t.Errorf("Documents[%d].Status = %q, want missing", i, report.Documents[i].Status)
		}
	}
}

func TestNumericID(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  int
		ok    bool
	}{
		{"int", 42, 42, true},
		{"int64", int64(100), 100, true},
		{"uint64", uint64(200), 200, true},
		{"float64_whole", float64(300), 300, true},
		{"float64_fractional", float64(3.14), 0, false},
		{"string_valid", "42", 42, true},
		{"string_invalid", "abc", 0, false},
		{"string_empty", "", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := numericID(tt.input)
			if ok != tt.ok {
				t.Errorf("numericID(%v) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("numericID(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
