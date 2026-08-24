package contact

import (
	"context"
	"database/sql"
	"strings"

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

// FindRefsByName resolves a display name to the minimal, privacy-safe Refs
// that carry it — persons first, then organizations, each ordered by name.
//
// It exists so a consumer can turn a name it already holds (a document's
// correspondent, say) into a stable contact identity. Like GetRef, both
// queries read the id and display-name columns only, so no contact point,
// note, alias, tag or classification can leave through this path; and like
// GetRef it neither creates nor modifies anything.
//
// Matching is exact but case-insensitive: a fuzzy match would silently
// assert an identity the user never confirmed. An empty or blank name
// matches nothing rather than everything.
func (s *Service) FindRefsByName(ctx context.Context, name string) ([]contact.Ref, error) {
	const op = "contact.FindRefsByName"

	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, nil
	}

	refs := []contact.Ref{}
	queries := []struct {
		sql  string
		kind contact.RefKind
	}{
		{`SELECT id, display_name FROM persons WHERE display_name = ? COLLATE NOCASE ORDER BY display_name, id`, contact.RefKindPerson},
		{`SELECT id, name FROM organizations WHERE name = ? COLLATE NOCASE ORDER BY name, id`, contact.RefKindOrganization},
	}
	for _, q := range queries {
		rows, err := s.db.QueryContext(ctx, q.sql, trimmed)
		if err != nil {
			return nil, errs.Internal(op, "failed to resolve contact references", err)
		}
		for rows.Next() {
			var id, displayName string
			if err := rows.Scan(&id, &displayName); err != nil {
				_ = rows.Close()
				return nil, errs.Internal(op, "failed to resolve contact references", err)
			}
			refs = append(refs, contact.Ref{
				Provider:      contact.RefProvider,
				SchemaVersion: contact.RefSchemaVersion,
				ID:            id,
				Kind:          q.kind,
				DisplayName:   displayName,
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, errs.Internal(op, "failed to resolve contact references", err)
		}
		_ = rows.Close()
	}
	return refs, nil
}
