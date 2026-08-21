package contact

import (
	"context"
	"database/sql"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// AddMembership links personID to organizationID for the given role/title
// and optional validity period. Multiple memberships between the same
// person and organization are allowed (e.g. rejoining later), so history
// is preserved rather than overwritten.
func (s *Service) AddMembership(ctx context.Context, personID, organizationID string, in contact.MembershipInput) (*contact.Membership, error) {
	const op = "contact.AddMembership"

	id := newID()
	ts := formatTime(now())
	var validFrom, validTo any
	if in.ValidFrom != nil {
		validFrom = formatTime(*in.ValidFrom)
	}
	if in.ValidTo != nil {
		validTo = formatTime(*in.ValidTo)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO organization_memberships (id, person_id, organization_id, role, title, valid_from, valid_to, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, personID, organizationID, in.Role, in.Title, validFrom, validTo, ts, ts)
	if err != nil {
		if isForeignKeyErr(err) {
			return nil, errs.Invalid(op, "person or organization does not exist", err)
		}
		if isCheckConstraintErr(err) {
			return nil, errs.Invalid(op, "valid_to must not be before valid_from", err)
		}
		return nil, errs.Internal(op, "failed to insert membership", err)
	}

	return s.getMembership(ctx, id)
}

func (s *Service) getMembership(ctx context.Context, id string) (*contact.Membership, error) {
	const op = "contact.getMembership"
	m, err := scanMembership(s.db.QueryRowContext(ctx, `
		SELECT id, person_id, organization_id, COALESCE(role, ''), COALESCE(title, ''), valid_from, valid_to, created_at, updated_at
		FROM organization_memberships WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, errs.NotFound(op, "membership not found", nil)
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load membership", err)
	}
	return m, nil
}

// ListMembershipsByPerson returns every membership (past and present) for
// a person, most recent valid_from first.
func (s *Service) ListMembershipsByPerson(ctx context.Context, personID string) ([]contact.Membership, error) {
	return s.listMemberships(ctx, "person_id", personID)
}

// ListMembershipsByOrganization returns every membership for an
// organization.
func (s *Service) ListMembershipsByOrganization(ctx context.Context, organizationID string) ([]contact.Membership, error) {
	return s.listMemberships(ctx, "organization_id", organizationID)
}

func (s *Service) listMemberships(ctx context.Context, column, id string) ([]contact.Membership, error) {
	const op = "contact.listMemberships"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, person_id, organization_id, COALESCE(role, ''), COALESCE(title, ''), valid_from, valid_to, created_at, updated_at
		FROM organization_memberships WHERE `+column+` = ?
		ORDER BY COALESCE(valid_from, created_at) DESC, id`, id)
	if err != nil {
		return nil, errs.Internal(op, "failed to list memberships", err)
	}
	defer rows.Close()

	var out []contact.Membership
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, errs.Internal(op, "failed to scan membership", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// EndMembership sets valid_to on an existing membership (closing a role
// without deleting its history).
func (s *Service) EndMembership(ctx context.Context, membershipID string, validTo time.Time) (*contact.Membership, error) {
	const op = "contact.EndMembership"
	res, err := s.db.ExecContext(ctx,
		"UPDATE organization_memberships SET valid_to = ?, updated_at = ? WHERE id = ?",
		formatTime(validTo), formatTime(now()), membershipID)
	if err != nil {
		if isCheckConstraintErr(err) {
			return nil, errs.Invalid(op, "valid_to must not be before valid_from", err)
		}
		return nil, errs.Internal(op, "failed to end membership", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errs.NotFound(op, "membership not found", nil)
	}
	return s.getMembership(ctx, membershipID)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMembership(r rowScanner) (*contact.Membership, error) {
	var m contact.Membership
	var validFrom, validTo sql.NullString
	var createdAt, updatedAt string
	if err := r.Scan(&m.ID, &m.PersonID, &m.OrganizationID, &m.Role, &m.Title, &validFrom, &validTo, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if validFrom.Valid {
		t, _ := parseTime(validFrom.String)
		m.ValidFrom = &t
	}
	if validTo.Valid {
		t, _ := parseTime(validTo.String)
		m.ValidTo = &t
	}
	m.CreatedAt, _ = parseTime(createdAt)
	m.UpdatedAt, _ = parseTime(updatedAt)
	return &m, nil
}
