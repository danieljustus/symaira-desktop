package contact

import (
	"context"
	"database/sql"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// CreatePersonWithContactPoints creates a person and, when non-empty, an
// email and/or phone contact point in the same transaction — the "quick
// add" shape every front end (CLI `contact add`, the MCP contact_create
// tool, the web console) offers. Any failure rolls the whole call back:
// a duplicate or malformed contact point never leaves a half-created
// person behind, and a retry cannot produce a second person.
func (s *Service) CreatePersonWithContactPoints(ctx context.Context, in contact.PersonInput, email, phone string) (*contact.Person, error) {
	const op = "contact.CreatePersonWithContactPoints"
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, errs.Invalid(op, "display name must not be empty", nil)
	}

	var id string
	err := sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		id, err = insertPerson(ctx, tx, in)
		if err != nil {
			return err
		}
		ref := personRef(id)
		if email != "" {
			if _, err := addContactPoint(ctx, tx, ref, contact.ContactPointInput{Kind: contact.ContactPointEmail, RawValue: email}); err != nil {
				return err
			}
		}
		if phone != "" {
			if _, err := addContactPoint(ctx, tx, ref, contact.ContactPointInput{Kind: contact.ContactPointPhone, RawValue: phone}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetPerson(ctx, id)
}
