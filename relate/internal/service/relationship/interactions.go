package relationship

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// AddPersonInteraction records an immutable interaction against a person.
func (s *Service) AddPersonInteraction(ctx context.Context, personID string, in relationship.InteractionInput) (*relationship.Interaction, error) {
	return addInteraction(ctx, s.db, personRef(personID), in)
}

// AddOrganizationInteraction records an immutable interaction against an
// organization.
func (s *Service) AddOrganizationInteraction(ctx context.Context, organizationID string, in relationship.InteractionInput) (*relationship.Interaction, error) {
	return addInteraction(ctx, s.db, organizationRef(organizationID), in)
}

func addInteraction(ctx context.Context, db *sql.DB, ref entityRef, in relationship.InteractionInput) (*relationship.Interaction, error) {
	const op = "relationship.addInteraction"
	if !in.Kind.Valid() {
		return nil, errs.Invalid(op, "unknown interaction kind: "+string(in.Kind), nil)
	}
	if strings.TrimSpace(in.Summary) == "" {
		return nil, errs.Invalid(op, "summary must not be empty", nil)
	}
	if in.Kind == relationship.InteractionExternalReference && strings.TrimSpace(in.ExternalRef) == "" {
		return nil, errs.Invalid(op, "external_reference interactions require an external_ref", nil)
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = now()
	}

	id := newID()
	createdAt := formatTime(now())
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO interactions (id, %s, kind, occurred_at, summary, external_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ref.column),
		id, ref.id, string(in.Kind), formatTime(in.OccurredAt), in.Summary, in.ExternalRef, createdAt)
	if err != nil {
		if isCheckConstraintErr(err) {
			return nil, errs.Invalid(op, "external_reference interactions require an external_ref", err)
		}
		if isForeignKeyErr(err) {
			return nil, errs.Invalid(op, "entity does not exist", err)
		}
		return nil, errs.Internal(op, "failed to insert interaction", err)
	}

	createdAtT, _ := parseTime(createdAt)
	return &relationship.Interaction{
		ID: id, Kind: in.Kind, OccurredAt: in.OccurredAt, Summary: in.Summary,
		ExternalRef: in.ExternalRef, CreatedAt: createdAtT,
	}, nil
}

// ListPersonInteractions returns a person's interactions, oldest first.
func (s *Service) ListPersonInteractions(ctx context.Context, personID string) ([]relationship.Interaction, error) {
	return listInteractions(ctx, s.db, personRef(personID))
}

// ListOrganizationInteractions returns an organization's interactions,
// oldest first.
func (s *Service) ListOrganizationInteractions(ctx context.Context, organizationID string) ([]relationship.Interaction, error) {
	return listInteractions(ctx, s.db, organizationRef(organizationID))
}

func listInteractions(ctx context.Context, db *sql.DB, ref entityRef) ([]relationship.Interaction, error) {
	const op = "relationship.listInteractions"
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, kind, occurred_at, summary, external_ref, created_at
		FROM interactions WHERE %s = ? ORDER BY occurred_at, id`, ref.column), ref.id)
	if err != nil {
		return nil, errs.Internal(op, "failed to list interactions", err)
	}
	defer rows.Close()

	var out []relationship.Interaction
	for rows.Next() {
		i, err := scanInteraction(rows)
		if err != nil {
			return nil, errs.Internal(op, "failed to scan interaction", err)
		}
		out = append(out, *i)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Internal(op, "failed to iterate interactions", err)
	}
	return out, nil
}

// LastPersonInteraction returns the most recent interaction for a person,
// or nil if none exists.
func (s *Service) LastPersonInteraction(ctx context.Context, personID string) (*relationship.Interaction, error) {
	return lastInteraction(ctx, s.db, personRef(personID))
}

// LastOrganizationInteraction returns the most recent interaction for an
// organization, or nil if none exists.
func (s *Service) LastOrganizationInteraction(ctx context.Context, organizationID string) (*relationship.Interaction, error) {
	return lastInteraction(ctx, s.db, organizationRef(organizationID))
}

func lastInteraction(ctx context.Context, db *sql.DB, ref entityRef) (*relationship.Interaction, error) {
	const op = "relationship.lastInteraction"
	i, err := scanInteraction(db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, kind, occurred_at, summary, external_ref, created_at
		FROM interactions WHERE %s = ? ORDER BY occurred_at DESC, id DESC LIMIT 1`, ref.column), ref.id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load last interaction", err)
	}
	return i, nil
}

func scanInteraction(r rowScanner) (*relationship.Interaction, error) {
	var i relationship.Interaction
	var kind, occurredAt, createdAt string
	var externalRef sql.NullString
	if err := r.Scan(&i.ID, &kind, &occurredAt, &i.Summary, &externalRef, &createdAt); err != nil {
		return nil, err
	}
	i.Kind = relationship.InteractionKind(kind)
	i.ExternalRef = externalRef.String
	i.OccurredAt, _ = parseTime(occurredAt)
	i.CreatedAt, _ = parseTime(createdAt)
	return &i, nil
}
