// Package memorylink implements Relate's own storage of confirmed
// SymMemory entity links (see internal/domain/memorylink). It does not
// talk to SymMemory itself — that is internal/integration/symmemory's
// job — this package only owns the local, reviewed mapping.
package memorylink

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/memorylink"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/google/uuid"
)

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

func newID() string { return uuid.NewString() }

func now() time.Time { return time.Now().UTC() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isForeignKeyErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// LinkPerson records a reviewed link from personID to a SymMemory entity.
// A person may have at most one link at a time — call UnlinkPerson first
// to replace it, so a link is never silently overwritten.
func (s *Service) LinkPerson(ctx context.Context, personID string, in memorylink.LinkInput) (*memorylink.Link, error) {
	return s.link(ctx, "person_id", personID, in)
}

// LinkOrganization is the organization equivalent of LinkPerson.
func (s *Service) LinkOrganization(ctx context.Context, organizationID string, in memorylink.LinkInput) (*memorylink.Link, error) {
	return s.link(ctx, "organization_id", organizationID, in)
}

func (s *Service) link(ctx context.Context, column, id string, in memorylink.LinkInput) (*memorylink.Link, error) {
	const op = "memorylink.link"
	if strings.TrimSpace(in.MemoryEntityID) == "" {
		return nil, errs.Invalid(op, "memory entity id must not be empty", nil)
	}
	if strings.TrimSpace(in.LinkedBy) == "" {
		return nil, errs.Invalid(op, "linked_by must not be empty", nil)
	}

	linkID := newID()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memory_links (id, `+column+`, memory_entity_id, memory_entity_type, linked_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		linkID, id, in.MemoryEntityID, in.MemoryEntityType, in.LinkedBy, formatTime(now()))
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, errs.Conflict(op, "already linked to a SymMemory entity; unlink first to replace it", err)
		}
		if isForeignKeyErr(err) {
			return nil, errs.Invalid(op, "entity does not exist", err)
		}
		return nil, errs.Internal(op, "failed to insert memory link", err)
	}
	return s.get(ctx, column, id)
}

// GetPersonLink returns the link for personID, or nil if unlinked.
func (s *Service) GetPersonLink(ctx context.Context, personID string) (*memorylink.Link, error) {
	return s.get(ctx, "person_id", personID)
}

// GetOrganizationLink returns the link for organizationID, or nil if unlinked.
func (s *Service) GetOrganizationLink(ctx context.Context, organizationID string) (*memorylink.Link, error) {
	return s.get(ctx, "organization_id", organizationID)
}

func (s *Service) get(ctx context.Context, column, id string) (*memorylink.Link, error) {
	const op = "memorylink.get"
	row := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(person_id, ''), COALESCE(organization_id, ''), memory_entity_id, memory_entity_type, linked_by, created_at
		FROM memory_links WHERE `+column+` = ?`, id)

	var l memorylink.Link
	var createdAt string
	err := row.Scan(&l.ID, &l.PersonID, &l.OrganizationID, &l.MemoryEntityID, &l.MemoryEntityType, &l.LinkedBy, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load memory link", err)
	}
	l.CreatedAt, _ = parseTime(createdAt)
	return &l, nil
}

// UnlinkPerson removes personID's SymMemory link, if any. Unlinking a
// person with no link is a no-op, not an error.
func (s *Service) UnlinkPerson(ctx context.Context, personID string) error {
	return s.unlink(ctx, "person_id", personID)
}

// UnlinkOrganization is the organization equivalent of UnlinkPerson.
func (s *Service) UnlinkOrganization(ctx context.Context, organizationID string) error {
	return s.unlink(ctx, "organization_id", organizationID)
}

func (s *Service) unlink(ctx context.Context, column, id string) error {
	const op = "memorylink.unlink"
	if _, err := s.db.ExecContext(ctx, "DELETE FROM memory_links WHERE "+column+" = ?", id); err != nil {
		return errs.Internal(op, "failed to remove memory link", err)
	}
	return nil
}
