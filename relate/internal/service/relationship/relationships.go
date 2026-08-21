package relationship

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// AddRelationship creates a directed relationship from a person to either
// another person or an organization. Retrying the same
// from/to/type/valid_from write returns the existing row instead of a
// duplicate (see the migration's partial unique indexes).
func (s *Service) AddRelationship(ctx context.Context, in relationship.RelationshipInput) (*relationship.Relationship, error) {
	const op = "relationship.AddRelationship"

	if strings.TrimSpace(in.FromPersonID) == "" {
		return nil, errs.Invalid(op, "from person id must not be empty", nil)
	}
	if strings.TrimSpace(in.Type) == "" {
		return nil, errs.Invalid(op, "relationship type must not be empty", nil)
	}
	if (in.ToPersonID == "") == (in.ToOrganizationID == "") {
		return nil, errs.Invalid(op, "exactly one of to-person or to-organization must be set", nil)
	}

	id := newID()
	ts := formatTime(now())
	var toPerson, toOrg, validFrom, validTo any
	if in.ToPersonID != "" {
		toPerson = in.ToPersonID
	}
	if in.ToOrganizationID != "" {
		toOrg = in.ToOrganizationID
	}
	if in.ValidFrom != nil {
		validFrom = formatTime(*in.ValidFrom)
	}
	if in.ValidTo != nil {
		validTo = formatTime(*in.ValidTo)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO relationships (id, from_person_id, to_person_id, to_organization_id, relationship_type, valid_from, valid_to, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.FromPersonID, toPerson, toOrg, in.Type, validFrom, validTo, in.Notes, ts, ts)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return s.findExistingRelationship(ctx, in)
		}
		if isForeignKeyErr(err) {
			return nil, errs.Invalid(op, "from/to entity does not exist", err)
		}
		if isCheckConstraintErr(err) {
			return nil, errs.Invalid(op, "invalid relationship (self-reference or valid_to before valid_from)", err)
		}
		return nil, errs.Internal(op, "failed to insert relationship", err)
	}

	return s.getRelationship(ctx, id)
}

// findExistingRelationship re-reads the row that made AddRelationship's
// unique constraint fire, so a retried write is idempotent from the
// caller's point of view instead of surfacing a conflict error.
func (s *Service) findExistingRelationship(ctx context.Context, in relationship.RelationshipInput) (*relationship.Relationship, error) {
	const op = "relationship.findExistingRelationship"
	query := `
		SELECT id, from_person_id, to_person_id, to_organization_id, relationship_type, valid_from, valid_to, notes, created_at, updated_at
		FROM relationships
		WHERE from_person_id = ? AND relationship_type = ? AND COALESCE(valid_from, '') = COALESCE(?, '')`
	args := []any{in.FromPersonID, in.Type, nullableTime(in.ValidFrom)}
	if in.ToPersonID != "" {
		query += " AND to_person_id = ?"
		args = append(args, in.ToPersonID)
	} else {
		query += " AND to_organization_id = ?"
		args = append(args, in.ToOrganizationID)
	}

	r, err := scanRelationship(s.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, errs.Internal(op, "unique constraint fired but no matching row found", err)
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load existing relationship", err)
	}
	return r, nil
}

func nullableTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

func (s *Service) getRelationship(ctx context.Context, id string) (*relationship.Relationship, error) {
	const op = "relationship.getRelationship"
	r, err := scanRelationship(s.db.QueryRowContext(ctx, `
		SELECT id, from_person_id, to_person_id, to_organization_id, relationship_type, valid_from, valid_to, notes, created_at, updated_at
		FROM relationships WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, errs.NotFound(op, "relationship not found", nil)
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load relationship", err)
	}
	return r, nil
}

// ListOutgoingFromPerson returns every relationship where personID is the
// subject ("who does this person know"), most recently created first.
func (s *Service) ListOutgoingFromPerson(ctx context.Context, personID string) ([]relationship.Relationship, error) {
	const op = "relationship.ListOutgoingFromPerson"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_person_id, to_person_id, to_organization_id, relationship_type, valid_from, valid_to, notes, created_at, updated_at
		FROM relationships WHERE from_person_id = ? ORDER BY created_at, id`, personID)
	if err != nil {
		return nil, errs.Internal(op, "failed to list relationships", err)
	}
	defer rows.Close()
	return scanRelationships(rows)
}

// ListIncomingToPerson returns every relationship that targets personID
// ("who knows this person").
func (s *Service) ListIncomingToPerson(ctx context.Context, personID string) ([]relationship.Relationship, error) {
	const op = "relationship.ListIncomingToPerson"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_person_id, to_person_id, to_organization_id, relationship_type, valid_from, valid_to, notes, created_at, updated_at
		FROM relationships WHERE to_person_id = ? ORDER BY created_at, id`, personID)
	if err != nil {
		return nil, errs.Internal(op, "failed to list relationships", err)
	}
	defer rows.Close()
	return scanRelationships(rows)
}

// ListIncomingToOrganization returns every relationship that targets
// organizationID.
func (s *Service) ListIncomingToOrganization(ctx context.Context, organizationID string) ([]relationship.Relationship, error) {
	const op = "relationship.ListIncomingToOrganization"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_person_id, to_person_id, to_organization_id, relationship_type, valid_from, valid_to, notes, created_at, updated_at
		FROM relationships WHERE to_organization_id = ? ORDER BY created_at, id`, organizationID)
	if err != nil {
		return nil, errs.Internal(op, "failed to list relationships", err)
	}
	defer rows.Close()
	return scanRelationships(rows)
}

func scanRelationships(rows *sql.Rows) ([]relationship.Relationship, error) {
	var out []relationship.Relationship
	for rows.Next() {
		r, err := scanRelationship(rows)
		if err != nil {
			return nil, errs.Internal("relationship.scanRelationships", "failed to scan relationship", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Internal("relationship.scanRelationships", "failed to iterate relationships", err)
	}
	return out, nil
}

func scanRelationship(r rowScanner) (*relationship.Relationship, error) {
	var rel relationship.Relationship
	var toPerson, toOrg, validFrom, validTo sql.NullString
	var createdAt, updatedAt string
	if err := r.Scan(&rel.ID, &rel.FromPersonID, &toPerson, &toOrg, &rel.Type, &validFrom, &validTo, &rel.Notes, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	rel.ToPersonID = toPerson.String
	rel.ToOrganizationID = toOrg.String
	if validFrom.Valid {
		t, _ := parseTime(validFrom.String)
		rel.ValidFrom = &t
	}
	if validTo.Valid {
		t, _ := parseTime(validTo.String)
		rel.ValidTo = &t
	}
	rel.CreatedAt, _ = parseTime(createdAt)
	rel.UpdatedAt, _ = parseTime(updatedAt)
	return &rel, nil
}
