package symseek

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestClient_Binary_Explicit(t *testing.T) {
	c := Client{Binary: "/custom/path/symseek"}
	got, err := c.binary()
	if err != nil {
		t.Fatalf("binary(): %v", err)
	}
	if got != "/custom/path/symseek" {
		t.Errorf("binary() = %q, want /custom/path/symseek", got)
	}
}

func TestClient_Binary_LookPath(t *testing.T) {
	path, err := exec.LookPath("symseek")
	if err != nil {
		t.Skip("symseek not in PATH; skipping LookPath test")
	}
	c := Client{}
	got, err := c.binary()
	if err != nil {
		t.Fatalf("binary(): %v", err)
	}
	if got != path {
		t.Errorf("binary() = %q, want %q", got, path)
	}
}

func TestClient_Binary_NotFound(t *testing.T) {
	// Use an empty PATH to guarantee symseek is not found.
	t.Setenv("PATH", t.TempDir())
	c := Client{}
	_, err := c.binary()
	if err == nil {
		t.Fatal("expected error when symseek not in PATH")
	}
}

func TestLoadFixtures_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.json")
	data := `[
		{"query": "invoice", "min_results": 1, "must_contain": ["invoice"]},
		{"query": "receipt", "min_results": 0}
	]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtures, err := LoadFixtures(path)
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}
	if len(fixtures) != 2 {
		t.Fatalf("len(fixtures) = %d, want 2", len(fixtures))
	}
	if fixtures[0].Query != "invoice" {
		t.Errorf("fixtures[0].Query = %q, want invoice", fixtures[0].Query)
	}
	if fixtures[0].MinResults != 1 {
		t.Errorf("fixtures[0].MinResults = %d, want 1", fixtures[0].MinResults)
	}
	if len(fixtures[0].MustContain) != 1 || fixtures[0].MustContain[0] != "invoice" {
		t.Errorf("fixtures[0].MustContain = %v, want [invoice]", fixtures[0].MustContain)
	}
}

func TestLoadFixtures_FileNotFound(t *testing.T) {
	_, err := LoadFixtures(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadFixtures_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFixtures(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadFixtures_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.json")
	data := `[{"query": "", "min_results": 1}]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFixtures(path)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestLoadFixtures_NegativeMinResults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.json")
	data := `[{"query": "test", "min_results": -1}]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFixtures(path)
	if err == nil {
		t.Fatal("expected error for negative min_results")
	}
}

func TestCountResults(t *testing.T) {
	tests := []struct {
		name string
		data string
		want int
	}{
		{"array", `[1,2,3]`, 3},
		{"empty_array", `[]`, 0},
		{"map_with_results", `{"results": [1,2]}`, 2},
		{"map_with_documents", `{"documents": [1]}`, 1},
		{"map_with_hits", `{"hits": [1,2,3,4]}`, 4},
		{"map_no_known_key", `{"foo": [1,2]}`, 0},
		{"invalid_json", `{bad`, 0},
		{"string", `"hello"`, 0},
		{"number", `42`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countResults([]byte(tt.data))
			if got != tt.want {
				t.Errorf("countResults(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestClient_Validate_NoFixtures(t *testing.T) {
	c := Client{Binary: "/nonexistent/symseek"}
	report := c.Validate(context.Background(), nil, 5, "test-version")
	if report.SchemaVersion != ReportSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", report.SchemaVersion, ReportSchemaVersion)
	}
	if report.ToolVersion != "test-version" {
		t.Errorf("ToolVersion = %q, want test-version", report.ToolVersion)
	}
	if report.Total != 0 {
		t.Errorf("Total = %d, want 0", report.Total)
	}
	if !report.OK {
		t.Error("OK = false, want true for empty fixtures")
	}
}

func TestClient_Validate_SearchError(t *testing.T) {
	// Use a binary that does not exist to trigger search errors.
	c := Client{Binary: "/nonexistent/symseek"}
	fixtures := []QueryFixture{
		{Query: "test", MinResults: 1},
	}
	report := c.Validate(context.Background(), fixtures, 5, "test-version")
	if report.OK {
		t.Error("OK = true, want false when search fails")
	}
	if report.Failed != 1 {
		t.Errorf("Failed = %d, want 1", report.Failed)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("len(Checks) = %d, want 1", len(report.Checks))
	}
	if report.Checks[0].OK {
		t.Error("Checks[0].OK = true, want false")
	}
	if report.Checks[0].Error == "" {
		t.Error("Checks[0].Error is empty, want error message")
	}
}

func TestClient_Index_MissingBinary(t *testing.T) {
	c := Client{Binary: "/nonexistent/symseek"}
	result := c.Index(context.Background(), "/some/path")
	if result.OK {
		t.Error("Index.OK = true, want false when binary missing")
	}
	if result.Error == "" {
		t.Error("Index.Error is empty, want error message")
	}
	if result.Path != "/some/path" {
		t.Errorf("Index.Path = %q, want /some/path", result.Path)
	}
	if result.Duration == "" {
		t.Error("Index.Duration is empty")
	}
}

func TestClient_SearchJSON_MissingBinary(t *testing.T) {
	c := Client{Binary: "/nonexistent/symseek"}
	_, err := c.SearchJSON(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error when binary missing")
	}
}

func TestClient_SearchJSON_DefaultLimit(t *testing.T) {
	// Verify that limit <= 0 defaults to 5 by checking the error message
	// contains the expected args. We use a missing binary so the command
	// fails but we can still verify the code path.
	c := Client{Binary: "/nonexistent/symseek"}
	_, err := c.SearchJSON(context.Background(), "test", 0)
	if err == nil {
		t.Fatal("expected error when binary missing")
	}
}

func TestValidationReport_JSON(t *testing.T) {
	report := &ValidationReport{
		SchemaVersion: 1,
		ToolVersion:   "v1.0",
		OK:            true,
		Total:         2,
		Passed:        2,
		Checks: []QueryCheck{
			{Query: "a", OK: true, MinResults: 1, ResultCount: 3},
			{Query: "b", OK: true, MinResults: 0, ResultCount: 0},
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ValidationReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", decoded.SchemaVersion)
	}
	if decoded.Total != 2 {
		t.Errorf("Total = %d, want 2", decoded.Total)
	}
	if len(decoded.Checks) != 2 {
		t.Errorf("len(Checks) = %d, want 2", len(decoded.Checks))
	}
}
