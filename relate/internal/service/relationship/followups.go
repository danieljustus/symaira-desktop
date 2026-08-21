package relationship

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// AddPersonFollowUp creates an open follow-up for a person.
func (s *Service) AddPersonFollowUp(ctx context.Context, personID string, in relationship.FollowUpInput) (*relationship.FollowUp, error) {
	return addFollowUp(ctx, s.db, personRef(personID), in)
}

// AddOrganizationFollowUp creates an open follow-up for an organization.
func (s *Service) AddOrganizationFollowUp(ctx context.Context, organizationID string, in relationship.FollowUpInput) (*relationship.FollowUp, error) {
	return addFollowUp(ctx, s.db, organizationRef(organizationID), in)
}

func addFollowUp(ctx context.Context, db *sql.DB, ref entityRef, in relationship.FollowUpInput) (*relationship.FollowUp, error) {
	const op = "relationship.addFollowUp"
	if in.DueAt.IsZero() {
		return nil, errs.Invalid(op, "due date must be set", nil)
	}

	id := newID()
	ts := formatTime(now())
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO follow_ups (id, %s, due_at, notes, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'open', ?, ?)`, ref.column),
		id, ref.id, formatTime(in.DueAt), in.Notes, ts, ts)
	if err != nil {
		if isForeignKeyErr(err) {
			return nil, errs.Invalid(op, "entity does not exist", err)
		}
		return nil, errs.Internal(op, "failed to insert follow-up", err)
	}

	return getFollowUp(ctx, db, id)
}

func (s *Service) getFollowUp(ctx context.Context, id string) (*relationship.FollowUp, error) {
	return getFollowUp(ctx, s.db, id)
}

func getFollowUp(ctx context.Context, db *sql.DB, id string) (*relationship.FollowUp, error) {
	const op = "relationship.getFollowUp"
	f, err := scanFollowUp(db.QueryRowContext(ctx, `
		SELECT id, due_at, notes, status, completed_at, cancelled_at, created_at, updated_at
		FROM follow_ups WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, errs.NotFound(op, "follow-up not found", nil)
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load follow-up", err)
	}
	return f, nil
}

// CompleteFollowUp marks an open follow-up completed. Completing an
// already-completed or cancelled follow-up is rejected rather than
// silently accepted, so callers cannot lose track of a state transition.
func (s *Service) CompleteFollowUp(ctx context.Context, id string) (*relationship.FollowUp, error) {
	return s.transitionFollowUp(ctx, id, relationship.FollowUpCompleted, "completed_at")
}

// CancelFollowUp marks an open follow-up cancelled.
func (s *Service) CancelFollowUp(ctx context.Context, id string) (*relationship.FollowUp, error) {
	return s.transitionFollowUp(ctx, id, relationship.FollowUpCancelled, "cancelled_at")
}

func (s *Service) transitionFollowUp(ctx context.Context, id string, newStatus relationship.FollowUpStatus, timestampColumn string) (*relationship.FollowUp, error) {
	const op = "relationship.transitionFollowUp"
	ts := formatTime(now())
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE follow_ups SET status = ?, %s = ?, updated_at = ?
		WHERE id = ? AND status = 'open'`, timestampColumn),
		string(newStatus), ts, ts, id)
	if err != nil {
		return nil, errs.Internal(op, "failed to update follow-up", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		existing, getErr := s.getFollowUp(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		return nil, errs.Conflict(op, "follow-up is already "+string(existing.Status), nil)
	}
	return s.getFollowUp(ctx, id)
}

// FollowUpFilter narrows ListFollowUps.
type FollowUpFilter string

const (
	FollowUpFilterAll      FollowUpFilter = "all"
	FollowUpFilterOpen     FollowUpFilter = "open"
	FollowUpFilterOverdue  FollowUpFilter = "overdue"
	FollowUpFilterUpcoming FollowUpFilter = "upcoming"
)

// ListPersonFollowUps lists a person's follow-ups under the given filter.
func (s *Service) ListPersonFollowUps(ctx context.Context, personID string, filter FollowUpFilter) ([]relationship.FollowUp, error) {
	return listFollowUps(ctx, s.db, personRef(personID), filter)
}

// ListOrganizationFollowUps lists an organization's follow-ups under the
// given filter.
func (s *Service) ListOrganizationFollowUps(ctx context.Context, organizationID string, filter FollowUpFilter) ([]relationship.FollowUp, error) {
	return listFollowUps(ctx, s.db, organizationRef(organizationID), filter)
}

func listFollowUps(ctx context.Context, db *sql.DB, ref entityRef, filter FollowUpFilter) ([]relationship.FollowUp, error) {
	const op = "relationship.listFollowUps"

	query := fmt.Sprintf("SELECT id, due_at, notes, status, completed_at, cancelled_at, created_at, updated_at FROM follow_ups WHERE %s = ?", ref.column)
	args := []any{ref.id}

	switch filter {
	case FollowUpFilterOpen:
		query += " AND status = 'open'"
	case FollowUpFilterOverdue:
		query += " AND status = 'open' AND due_at < ?"
		args = append(args, formatTime(now()))
	case FollowUpFilterUpcoming:
		query += " AND status = 'open' AND due_at >= ?"
		args = append(args, formatTime(now()))
	case FollowUpFilterAll, "":
		// no additional filter
	default:
		return nil, errs.Invalid(op, "unknown follow-up filter: "+string(filter), nil)
	}
	query += " ORDER BY due_at, id"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errs.Internal(op, "failed to list follow-ups", err)
	}
	defer rows.Close()

	var out []relationship.FollowUp
	for rows.Next() {
		f, err := scanFollowUp(rows)
		if err != nil {
			return nil, errs.Internal(op, "failed to scan follow-up", err)
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Internal(op, "failed to iterate follow-ups", err)
	}
	return out, nil
}

func scanFollowUp(r rowScanner) (*relationship.FollowUp, error) {
	var f relationship.FollowUp
	var dueAt, createdAt, updatedAt string
	var status string
	var completedAt, cancelledAt sql.NullString
	if err := r.Scan(&f.ID, &dueAt, &f.Notes, &status, &completedAt, &cancelledAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	f.Status = relationship.FollowUpStatus(status)
	f.DueAt, _ = parseTime(dueAt)
	f.CreatedAt, _ = parseTime(createdAt)
	f.UpdatedAt, _ = parseTime(updatedAt)
	if completedAt.Valid {
		t, _ := parseTime(completedAt.String)
		f.CompletedAt = &t
	}
	if cancelledAt.Valid {
		t, _ := parseTime(cancelledAt.String)
		f.CancelledAt = &t
	}
	return &f, nil
}
