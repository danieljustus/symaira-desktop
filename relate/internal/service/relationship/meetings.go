package relationship

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// MeetingImportInput is a reviewed, manually-confirmed record of a
// SymMeet meeting — see internal/integration/symmeet and
// docs/integrations/SYMMEET.md. MeetingID is the only required field and
// becomes the interaction's ExternalRef: an opaque reference, never a
// transcript, recording, or participant list. Contact association is the
// caller's job (one call per person/organization to attach), matching
// the "manual/reviewed" acceptance criterion — nothing here guesses who
// attended.
type MeetingImportInput struct {
	MeetingID  string
	Title      string
	Summary    string
	OccurredAt time.Time
}

// ImportPersonMeeting records (or, if this MeetingID was already imported
// for this person, returns) a "meeting" interaction. The second return
// value is true only when a new interaction was created — false means an
// idempotent re-import found the existing one instead.
func (s *Service) ImportPersonMeeting(ctx context.Context, personID string, in MeetingImportInput) (*relationship.Interaction, bool, error) {
	return importMeeting(ctx, s.db, personRef(personID), in)
}

// ImportOrganizationMeeting is the organization equivalent of
// ImportPersonMeeting.
func (s *Service) ImportOrganizationMeeting(ctx context.Context, organizationID string, in MeetingImportInput) (*relationship.Interaction, bool, error) {
	return importMeeting(ctx, s.db, organizationRef(organizationID), in)
}

func importMeeting(ctx context.Context, db *sql.DB, ref entityRef, in MeetingImportInput) (*relationship.Interaction, bool, error) {
	const op = "relationship.importMeeting"
	if strings.TrimSpace(in.MeetingID) == "" {
		return nil, false, errs.Invalid(op, "meeting id must not be empty", nil)
	}

	existing, err := findInteractionByExternalRef(ctx, db, ref, relationship.InteractionMeeting, in.MeetingID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}

	summary := in.Summary
	if summary == "" {
		summary = in.Title
	}
	if summary == "" {
		summary = "Meeting"
	}

	created, err := addInteraction(ctx, db, ref, relationship.InteractionInput{
		Kind: relationship.InteractionMeeting, OccurredAt: in.OccurredAt, Summary: summary, ExternalRef: in.MeetingID,
	})
	if err != nil {
		if isUniqueConstraintErr(err) {
			// Lost a race with a concurrent import of the same meeting —
			// the database-level unique index (migration
			// 0007_meeting_idempotency.sql) is the authoritative
			// idempotency guarantee; fall back to it exactly like the
			// importer service does for import_sources.
			existing, findErr := findInteractionByExternalRef(ctx, db, ref, relationship.InteractionMeeting, in.MeetingID)
			if findErr != nil {
				return nil, false, findErr
			}
			if existing != nil {
				return existing, false, nil
			}
		}
		return nil, false, err
	}
	return created, true, nil
}

func findInteractionByExternalRef(ctx context.Context, db *sql.DB, ref entityRef, kind relationship.InteractionKind, externalRef string) (*relationship.Interaction, error) {
	const op = "relationship.findInteractionByExternalRef"
	i, err := scanInteraction(db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, kind, occurred_at, summary, external_ref, created_at
		FROM interactions WHERE %s = ? AND kind = ? AND external_ref = ? LIMIT 1`, ref.column),
		ref.id, string(kind), externalRef))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to query interaction by external ref", err)
	}
	return i, nil
}
