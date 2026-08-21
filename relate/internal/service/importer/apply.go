package importer

import (
	"context"
	"database/sql"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/importer"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// Apply writes plan.Rows to the database. Rows with no duplicate
// candidate are always created. Rows with a duplicate candidate require
// an explicit resolution in resolutions — an unresolved duplicate row is
// skipped, never silently created or merged. Everything runs in one
// transaction: an unexpected per-row failure is recorded in
// RunResult.Errors and that row is skipped, but does not touch rows that
// already succeeded within the same Apply call.
func (s *Service) Apply(ctx context.Context, plan *importer.ImportPlan, resolutions []importer.RowResolution) (*importer.RunResult, error) {
	const op = "importer.Apply"

	resByRow := make(map[int]importer.RowResolution, len(resolutions))
	for _, r := range resolutions {
		resByRow[r.RowNumber] = r
	}
	dupByRow := make(map[int]bool, len(plan.Duplicates))
	for _, d := range plan.Duplicates {
		dupByRow[d.RowNumber] = true
	}

	result := &importer.RunResult{Source: plan.Source}

	err := sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		for _, row := range plan.Rows {
			res, hasRes := resByRow[row.RowNumber]

			if !dupByRow[row.RowNumber] {
				// No duplicate candidate: nothing to choose between,
				// unless the caller explicitly asked to skip it anyway.
				if hasRes && res.Resolution == importer.ResolutionSkip {
					result.Skipped++
					continue
				}
				s.applyCreate(ctx, tx, plan.Source, row, result)
				continue
			}

			if !hasRes || res.Resolution == importer.ResolutionSkip {
				result.Skipped++
				continue
			}

			switch res.Resolution {
			case importer.ResolutionCreate:
				s.applyCreate(ctx, tx, plan.Source, row, result)
			case importer.ResolutionMerge:
				s.applyMerge(ctx, tx, plan.Source, row, res.MergePersonID, result)
			default:
				result.Failed++
				result.Errors = append(result.Errors, importer.ValidationIssue{
					RowNumber: row.RowNumber, Field: "resolution", Message: "unknown resolution: " + string(res.Resolution),
				})
			}
		}
		return recordRun(ctx, tx, result)
	})
	if err != nil {
		return nil, errs.Internal(op, "import apply failed", err)
	}
	return result, nil
}

func (s *Service) applyCreate(ctx context.Context, tx *sql.Tx, source importer.SourceKind, row importer.ImportRow, result *importer.RunResult) {
	personID, err := s.createFromRow(ctx, tx, row)
	if err != nil {
		result.Failed++
		result.Errors = append(result.Errors, importer.ValidationIssue{RowNumber: row.RowNumber, Message: err.Error()})
		return
	}
	if err := linkSource(ctx, tx, source, row.SourceRef, personID); err != nil {
		result.Failed++
		result.Errors = append(result.Errors, importer.ValidationIssue{RowNumber: row.RowNumber, Message: err.Error()})
		return
	}
	result.Created++
}

func (s *Service) applyMerge(ctx context.Context, tx *sql.Tx, source importer.SourceKind, row importer.ImportRow, targetPersonID string, result *importer.RunResult) {
	if targetPersonID == "" {
		result.Failed++
		result.Errors = append(result.Errors, importer.ValidationIssue{RowNumber: row.RowNumber, Field: "merge_person_id", Message: "merge resolution requires a target person id"})
		return
	}
	if err := s.mergeIntoRow(ctx, tx, row, targetPersonID); err != nil {
		result.Failed++
		result.Errors = append(result.Errors, importer.ValidationIssue{RowNumber: row.RowNumber, Message: err.Error()})
		return
	}
	if err := linkSource(ctx, tx, source, row.SourceRef, targetPersonID); err != nil {
		result.Failed++
		result.Errors = append(result.Errors, importer.ValidationIssue{RowNumber: row.RowNumber, Message: err.Error()})
		return
	}
	result.Merged++
}

// createFromRow inserts a new person (and organization/membership, if the
// row names one) inside tx.
func (s *Service) createFromRow(ctx context.Context, tx *sql.Tx, row importer.ImportRow) (string, error) {
	personID := newID()
	ts := formatTime(now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO persons (id, display_name, given_name, family_name, notes, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		personID, row.DisplayName, row.GivenName, row.FamilyName, "", "import", ts, ts); err != nil {
		return "", err
	}

	if err := insertContactPoints(ctx, tx, personID, row); err != nil {
		return "", err
	}

	if row.OrganizationName != "" {
		orgID, err := findOrCreateOrganization(ctx, tx, row.OrganizationName)
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO organization_memberships (id, person_id, organization_id, role, title, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, newID(), personID, orgID, "", row.Title, ts, ts); err != nil {
			return "", err
		}
	}

	return personID, nil
}

// mergeIntoRow adds any contact points from row that targetPersonID does
// not already have. It never overwrites an existing field — merge only
// ever adds, consistent with the beta's "preserve verified data" import
// goal. A contact point already present (same kind+normalized value) is
// silently skipped rather than erroring, since that is simply not new
// information.
func (s *Service) mergeIntoRow(ctx context.Context, tx *sql.Tx, row importer.ImportRow, targetPersonID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons WHERE id = ?", targetPersonID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errs.Invalid("importer.mergeIntoRow", "merge target person does not exist", nil)
	}
	return insertContactPoints(ctx, tx, targetPersonID, row)
}

func insertContactPoints(ctx context.Context, tx *sql.Tx, personID string, row importer.ImportRow) error {
	insert := func(kind contactdomain.ContactPointKind, value string) error {
		normalized := contactdomain.Normalize(kind, value)
		ts := formatTime(now())
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO contact_points (id, person_id, kind, raw_value, normalized_value, label, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, newID(), personID, string(kind), value, normalized, "", ts, ts)
		return err
	}
	for _, e := range row.Emails {
		if err := insert(contactdomain.ContactPointEmail, e); err != nil {
			return err
		}
	}
	for _, p := range row.Phones {
		if err := insert(contactdomain.ContactPointPhone, p); err != nil {
			return err
		}
	}
	for _, u := range row.URLs {
		if err := insert(contactdomain.ContactPointURL, u); err != nil {
			return err
		}
	}
	return nil
}

func findOrCreateOrganization(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM organizations WHERE name = ? COLLATE NOCASE", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	id = newID()
	ts := formatTime(now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO organizations (id, name, notes, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, "", "import", ts, ts); err != nil {
		return "", err
	}
	return id, nil
}

func linkSource(ctx context.Context, tx *sql.Tx, source importer.SourceKind, sourceRef, personID string) error {
	if sourceRef == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO import_sources (id, source_kind, source_ref, person_id, imported_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (source_kind, source_ref) DO UPDATE SET person_id = excluded.person_id, imported_at = excluded.imported_at`,
		newID(), string(source), sourceRef, personID, formatTime(now()))
	return err
}

func recordRun(ctx context.Context, tx *sql.Tx, result *importer.RunResult) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO import_runs (id, source_kind, created, merged, skipped, failed)
		VALUES (?, ?, ?, ?, ?, ?)`,
		newID(), string(result.Source), result.Created, result.Merged, result.Skipped, result.Failed)
	return err
}
