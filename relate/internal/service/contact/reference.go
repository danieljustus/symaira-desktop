package contact

import (
	"context"
	"database/sql"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// GetRef resolves any contact ID (person or organization) to its minimal,
// privacy-safe Ref — the reference-only shape documented in
// docs/integrations/CONTACT_REF.md. It never loads contact points, notes,
// aliases, tags, or classifications: both lookups are single primary-key
// reads of the id and display-name columns only, so the query itself
// cannot surface private fields.
//
// Unknown or erased IDs return errs.KindNotFound with a message that does
// not disclose which tables were probed, per docs/PRIVACY.md.
func (s *Service) GetRef(ctx context.Context, id string) (*contact.Ref, error) {
	const op = "contact.GetRef"

	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT display_name FROM persons WHERE id = ?`, id,
	).Scan(&name)
	switch {
	case err == nil:
		return &contact.Ref{
			Provider:      contact.RefProvider,
			SchemaVersion: contact.RefSchemaVersion,
			ID:            id,
			Kind:          contact.RefKindPerson,
			DisplayName:   name,
		}, nil
	case err != sql.ErrNoRows:
		return nil, errs.Internal(op, "failed to resolve contact reference", err)
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT name FROM organizations WHERE id = ?`, id,
	).Scan(&name)
	switch {
	case err == nil:
		return &contact.Ref{
			Provider:      contact.RefProvider,
			SchemaVersion: contact.RefSchemaVersion,
			ID:            id,
			Kind:          contact.RefKindOrganization,
			DisplayName:   name,
		}, nil
	case err != sql.ErrNoRows:
		return nil, errs.Internal(op, "failed to resolve contact reference", err)
	}

	return nil, errs.NotFound(op, "contact not found", nil)
}
