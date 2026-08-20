package notionimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/version"
)

func TestBuildMigrationReport(t *testing.T) {
	started := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(5 * time.Second)

	stats := &Stats{
		Imported:   3,
		Skipped:    1,
		Failed:     0,
		Total:      4,
		Warnings:   []string{"missing tag"},
		RunID:      "notion-20260701T100000Z",
		Source:     "/tmp/notion-export",
		Mode:       "full",
		StartedAt:  started,
		FinishedAt: finished,
		Results: []PageResult{
			{SourcePath: "a.md", VaultPath: "/vault/a.md", SHA256: "aaa", Status: "imported"},
			{SourcePath: "b.md", Status: "skipped", Reason: "duplicate"},
		},
	}

	r := stats.BuildMigrationReport(false)

	if r.SchemaVersion != ReportSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", r.SchemaVersion, ReportSchemaVersion)
	}
	if r.ToolVersion != version.Version {
		t.Fatalf("tool_version = %q, want %q", r.ToolVersion, version.Version)
	}
	if r.RunID != stats.RunID {
		t.Fatalf("run_id = %q, want %q", r.RunID, stats.RunID)
	}
	if r.Source != stats.Source {
		t.Fatalf("source = %q, want %q", r.Source, stats.Source)
	}
	if r.Mode != stats.Mode {
		t.Fatalf("mode = %q, want %q", r.Mode, stats.Mode)
	}
	if r.DryRun {
		t.Fatal("dry_run should be false")
	}
	if r.Total != 4 {
		t.Fatalf("total = %d, want 4", r.Total)
	}
	if r.Imported != 3 {
		t.Fatalf("imported = %d, want 3", r.Imported)
	}
	if r.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", r.Skipped)
	}
	if r.Failed != 0 {
		t.Fatalf("failed = %d, want 0", r.Failed)
	}
	if r.DurationSec != 5.0 {
		t.Fatalf("duration_seconds = %f, want 5.0", r.DurationSec)
	}
	if len(r.Documents) != 2 {
		t.Fatalf("documents len = %d, want 2", len(r.Documents))
	}
	if len(r.Warnings) != 1 || r.Warnings[0] != "missing tag" {
		t.Fatalf("warnings = %v, want [missing tag]", r.Warnings)
	}
}

func TestBuildMigrationReport_NilResultsBecomesEmptySlice(t *testing.T) {
	stats := &Stats{
		StartedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}
	r := stats.BuildMigrationReport(true)
	if r.Documents == nil {
		t.Fatal("expected non-nil documents slice")
	}
	if len(r.Documents) != 0 {
		t.Fatalf("documents len = %d, want 0", len(r.Documents))
	}
	if !r.DryRun {
		t.Fatal("dry_run should be true")
	}
}

func TestWriteMigrationReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")

	report := &MigrationReport{
		SchemaVersion: ReportSchemaVersion,
		ToolVersion:   version.Version,
		Total:         2,
		Imported:      1,
		Skipped:       1,
		Documents:     []PageResult{},
	}

	if err := WriteMigrationReport(path, report); err != nil {
		t.Fatalf("WriteMigrationReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var decoded MigrationReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.SchemaVersion != ReportSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", decoded.SchemaVersion, ReportSchemaVersion)
	}
	if decoded.Total != 2 {
		t.Fatalf("total = %d, want 2", decoded.Total)
	}

	// Verify file ends with newline.
	if data[len(data)-1] != '\n' {
		t.Fatal("expected trailing newline")
	}
}

func TestWriteMigrationReport_InvalidPath(t *testing.T) {
	report := &MigrationReport{SchemaVersion: ReportSchemaVersion}
	err := WriteMigrationReport("/nonexistent/dir/report.json", report)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}
