package notionimport

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/version"
)

const ReportSchemaVersion = 1

// MigrationReport is the machine-readable artifact of a Notion import or
// dry-run, suitable as a review surface. It never contains document content.
type MigrationReport struct {
	SchemaVersion int          `json:"schema_version"`
	RunID         string       `json:"run_id,omitempty"`
	ToolVersion   string       `json:"tool_version,omitempty"`
	Source        string       `json:"source,omitempty"`
	StartedAt     time.Time    `json:"started_at,omitempty"`
	FinishedAt    time.Time    `json:"finished_at,omitempty"`
	DurationSec   float64      `json:"duration_seconds,omitempty"`
	Mode          string       `json:"mode,omitempty"`
	DryRun        bool         `json:"dry_run"`
	Total         int          `json:"total"`
	Imported      int          `json:"imported"`
	Skipped       int          `json:"skipped"`
	Failed        int          `json:"failed"`
	Documents     []PageResult `json:"documents"`
	Warnings      []string     `json:"warnings,omitempty"`
}

// BuildMigrationReport assembles a MigrationReport from run statistics.
func (s *Stats) BuildMigrationReport(dryRun bool) *MigrationReport {
	r := &MigrationReport{
		SchemaVersion: ReportSchemaVersion,
		RunID:         s.RunID,
		ToolVersion:   version.Version,
		Source:        s.Source,
		StartedAt:     s.StartedAt,
		FinishedAt:    s.FinishedAt,
		DurationSec:   s.FinishedAt.Sub(s.StartedAt).Seconds(),
		Mode:          s.Mode,
		DryRun:        dryRun,
		Total:         s.Total,
		Imported:      s.Imported,
		Skipped:       s.Skipped,
		Failed:        s.Failed,
		Documents:     s.Results,
		Warnings:      s.Warnings,
	}
	if r.Documents == nil {
		r.Documents = []PageResult{}
	}
	return r
}

// WriteMigrationReport writes report to path as indented JSON.
func WriteMigrationReport(path string, report *MigrationReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal migration report: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write migration report: %w", err)
	}
	return nil
}
