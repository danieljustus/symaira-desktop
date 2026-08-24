// Package importer defines the pure domain types for the vCard/CSV import
// workflow: parsed rows, validation issues, duplicate candidates and a
// resolved apply plan. Parsing lives in vcard.go / csv.go; writing the
// results to storage is internal/service/importer's job.
package importer

// SourceKind identifies where an import came from.
type SourceKind string

const (
	SourceVCard SourceKind = "vcard"
	SourceCSV   SourceKind = "csv"
)

// ImportRow is one parsed record before any write. RowNumber is 1-based:
// the card index for vCard, the data row index (header excluded) for CSV.
// SourceRef is a stable identifier used for idempotent re-import — the
// vCard UID when present, or a content hash of the mapped fields for CSV.
type ImportRow struct {
	RowNumber        int
	DisplayName      string
	GivenName        string
	FamilyName       string
	OrganizationName string
	Title            string
	Emails           []string
	Phones           []string
	URLs             []string
	SourceRef        string
}

// ValidationIssue reports a row that failed validation and was excluded
// from the plan's importable rows, with enough context to fix the source
// file.
type ValidationIssue struct {
	RowNumber int
	Field     string
	Message   string
}

// DuplicateCandidate explains why an ImportRow might already exist in the
// database: either an exact idempotency match (this exact source was
// imported before) or a normalized contact-point/name match.
type DuplicateCandidate struct {
	RowNumber          int
	ExistingPersonID   string
	ExistingName       string
	MatchedOn          string // e.g. "email:ada@example.com", "source:vcard:UID-1", "name:ada lovelace"
	ExactSourceRematch bool   // true when this is the same source re-imported (idempotency match)
}

// Resolution is the user's explicit choice for a row that has at least
// one duplicate candidate. Rows with no candidates are always created —
// there is nothing to choose between.
type Resolution string

const (
	ResolutionCreate Resolution = "create"
	ResolutionSkip   Resolution = "skip"
	ResolutionMerge  Resolution = "merge"
)

// RowResolution pairs a row with its resolution. MergePersonID is required
// when Resolution is ResolutionMerge.
type RowResolution struct {
	RowNumber     int
	Resolution    Resolution
	MergePersonID string
}

// ImportPlan is the dry-run output: every parsed row, validation issues
// for rows that won't be imported, and duplicate candidates for rows that
// need an explicit resolution before Apply.
type ImportPlan struct {
	Source     SourceKind
	Rows       []ImportRow
	Issues     []ValidationIssue
	Duplicates []DuplicateCandidate
}

// RunResult reports what Apply actually did.
type RunResult struct {
	Source  SourceKind
	Created int
	Merged  int
	Skipped int
	Failed  int
	Errors  []ValidationIssue
}
