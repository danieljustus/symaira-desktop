package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/domain/security"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// EraseContact permanently removes a person and every row that names them
// directly: contact points, aliases, tag/classification links,
// memberships, relationships (incoming and outgoing) and interactions/
// follow-ups. This is a hard delete, not a soft tombstone — see
// docs/PRIVACY.md for why the beta's erase policy chose full removal plus
// an audit trail over marking rows deleted in place. The cascade is
// enforced by ON DELETE CASCADE foreign keys declared on the child
// tables; this method only needs to count what will disappear and delete
// the parent row inside one transaction, then record an audit event that
// names counts, never contact values.
func (s *Service) EraseContact(ctx context.Context, personID string) (*security.EraseSummary, error) {
	const op = "security.EraseContact"

	var summary *security.EraseSummary
	err := sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		summary, err = countPersonData(ctx, tx, personID)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, "DELETE FROM persons WHERE id = ?", personID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errs.NotFound(op, "person not found", nil)
		}

		return recordAudit(ctx, tx, "erase_contact", "person", personID, eraseDetail(*summary))
	})
	if err != nil {
		return nil, wrapEraseErr(op, "failed to erase person", err)
	}
	return summary, nil
}

// EraseOrganization is the organization equivalent of EraseContact.
func (s *Service) EraseOrganization(ctx context.Context, organizationID string) (*security.EraseSummary, error) {
	const op = "security.EraseOrganization"

	var summary *security.EraseSummary
	err := sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		summary, err = countOrganizationData(ctx, tx, organizationID)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, "DELETE FROM organizations WHERE id = ?", organizationID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errs.NotFound(op, "organization not found", nil)
		}

		return recordAudit(ctx, tx, "erase_contact", "organization", organizationID, eraseDetail(*summary))
	})
	if err != nil {
		return nil, wrapEraseErr(op, "failed to erase organization", err)
	}
	return summary, nil
}

// wrapEraseErr preserves a structured *errs.Error raised inside the
// transaction (e.g. NotFound) instead of flattening it to Internal.
func wrapEraseErr(op, message string, err error) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	return errs.Internal(op, message, err)
}

type countQuery struct {
	query string
	args  []any
	dest  *int
}

func countPersonData(ctx context.Context, tx *sql.Tx, personID string) (*security.EraseSummary, error) {
	s := &security.EraseSummary{EntityType: "person", EntityID: personID}
	counts := []countQuery{
		{"SELECT COUNT(*) FROM contact_points WHERE person_id = ?", []any{personID}, &s.ContactPointsRemoved},
		{"SELECT COUNT(*) FROM aliases WHERE person_id = ?", []any{personID}, &s.AliasesRemoved},
		{"SELECT COUNT(*) FROM organization_memberships WHERE person_id = ?", []any{personID}, &s.MembershipsRemoved},
		{"SELECT COUNT(*) FROM relationships WHERE from_person_id = ? OR to_person_id = ?", []any{personID, personID}, &s.RelationshipsRemoved},
		{"SELECT COUNT(*) FROM interactions WHERE person_id = ?", []any{personID}, &s.InteractionsRemoved},
		{"SELECT COUNT(*) FROM follow_ups WHERE person_id = ?", []any{personID}, &s.FollowUpsRemoved},
	}
	if err := runCounts(ctx, tx, counts); err != nil {
		return nil, err
	}
	return s, nil
}

func countOrganizationData(ctx context.Context, tx *sql.Tx, organizationID string) (*security.EraseSummary, error) {
	s := &security.EraseSummary{EntityType: "organization", EntityID: organizationID}
	counts := []countQuery{
		{"SELECT COUNT(*) FROM contact_points WHERE organization_id = ?", []any{organizationID}, &s.ContactPointsRemoved},
		{"SELECT COUNT(*) FROM aliases WHERE organization_id = ?", []any{organizationID}, &s.AliasesRemoved},
		{"SELECT COUNT(*) FROM organization_memberships WHERE organization_id = ?", []any{organizationID}, &s.MembershipsRemoved},
		{"SELECT COUNT(*) FROM relationships WHERE to_organization_id = ?", []any{organizationID}, &s.RelationshipsRemoved},
		{"SELECT COUNT(*) FROM interactions WHERE organization_id = ?", []any{organizationID}, &s.InteractionsRemoved},
		{"SELECT COUNT(*) FROM follow_ups WHERE organization_id = ?", []any{organizationID}, &s.FollowUpsRemoved},
	}
	if err := runCounts(ctx, tx, counts); err != nil {
		return nil, err
	}
	return s, nil
}

func runCounts(ctx context.Context, tx *sql.Tx, counts []countQuery) error {
	for _, c := range counts {
		if err := tx.QueryRowContext(ctx, c.query, c.args...).Scan(c.dest); err != nil {
			return err
		}
	}
	return nil
}

// eraseDetail formats a summary as an audit detail string. It only ever
// contains counts and the entity kind — never a name, email or phone
// number.
func eraseDetail(s security.EraseSummary) string {
	return fmt.Sprintf(
		"contact_points=%d aliases=%d memberships=%d relationships=%d interactions=%d follow_ups=%d",
		s.ContactPointsRemoved, s.AliasesRemoved, s.MembershipsRemoved, s.RelationshipsRemoved, s.InteractionsRemoved, s.FollowUpsRemoved,
	)
}
