package paperlessimport

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/version"
)

const ReportSchemaVersion = 1

// MigrationReport is the machine-readable artifact of an import or dry-run,
// suitable as a review surface for an operator or a later UI/bulk-review tool.
// It never contains document content or secrets — only counts, document IDs,
// generated paths, and warnings.
type MigrationReport struct {
	SchemaVersion   int              `json:"schema_version"`
	RunID           string           `json:"run_id,omitempty"`
	ToolVersion     string           `json:"tool_version,omitempty"`
	Source          string           `json:"source,omitempty"`
	SourceURL       string           `json:"source_url,omitempty"`
	StartedAt       time.Time        `json:"started_at,omitempty"`
	FinishedAt      time.Time        `json:"finished_at,omitempty"`
	DurationSeconds float64          `json:"duration_seconds,omitempty"`
	Mode            string           `json:"mode,omitempty"`
	DryRun          bool             `json:"dry_run"`
	Total           int              `json:"total"`
	Imported        int              `json:"imported"`
	Skipped         int              `json:"skipped"`
	Failed          int              `json:"failed"`
	Documents       []DocumentResult `json:"documents"`
	Warnings        []string         `json:"warnings,omitempty"`

	// These are populated from the dry-run audit when it is available.
	UnsupportedFileTypes       map[string]int `json:"unsupported_file_types,omitempty"`
	UnresolvedTagIDs           []int          `json:"unresolved_tag_ids,omitempty"`
	UnresolvedCorrespondentIDs []int          `json:"unresolved_correspondent_ids,omitempty"`
	UnresolvedDocumentTypeIDs  []int          `json:"unresolved_document_type_ids,omitempty"`
	UnresolvedStoragePathIDs   []int          `json:"unresolved_storage_path_ids,omitempty"`
	ByExpectedExtension        map[string]int `json:"by_expected_extension,omitempty"`
	RequiredTools              []string       `json:"required_tools,omitempty"`
}

// BuildMigrationReport assembles a MigrationReport from run statistics. Pass
// the same dryRun flag the run used so the report records whether documents
// were actually written.
func (s *Stats) BuildMigrationReport(dryRun bool) *MigrationReport {
	r := &MigrationReport{
		SchemaVersion:   ReportSchemaVersion,
		RunID:           s.RunID,
		ToolVersion:     version.Version,
		Source:          s.Source,
		SourceURL:       s.SourceURL,
		StartedAt:       s.StartedAt,
		FinishedAt:      s.FinishedAt,
		DurationSeconds: s.FinishedAt.Sub(s.StartedAt).Seconds(),
		Mode:            s.Mode,
		DryRun:          dryRun,
		Total:           s.Total,
		Imported:        s.Imported,
		Skipped:         s.Skipped,
		Failed:          s.Failed,
		Documents:       s.Results,
		Warnings:        s.Warnings,
	}
	if r.Documents == nil {
		r.Documents = []DocumentResult{}
	}
	if s.Audit != nil {
		r.UnsupportedFileTypes = s.Audit.UnsupportedFileTypes
		r.UnresolvedTagIDs = s.Audit.UnresolvedTagIDs
		r.UnresolvedCorrespondentIDs = s.Audit.UnresolvedCorrespondentIDs
		r.UnresolvedDocumentTypeIDs = s.Audit.UnresolvedDocumentTypeIDs
		r.UnresolvedStoragePathIDs = s.Audit.UnresolvedStoragePathIDs
		r.ByExpectedExtension = s.Audit.ByExpectedExtension
		r.RequiredTools = s.Audit.RequiredTools
	}
	return r
}

// WriteMigrationReport writes report to path as indented JSON with a trailing
// newline.
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
