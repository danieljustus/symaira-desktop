package contact

import (
	"context"
	"database/sql"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/errs"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

// CreatePerson inserts a new person. DisplayName is required; all other
// fields are optional.
func (s *Service) CreatePerson(ctx context.Context, in contact.PersonInput) (*contact.Person, error) {
	const op = "contact.CreatePerson"
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, errs.Invalid(op, "display name must not be empty", nil)
	}

	id, err := insertPerson(ctx, s.db, in)
	if err != nil {
		return nil, errs.Internal(op, "failed to insert person", err)
	}
	return s.GetPerson(ctx, id)
}

// insertPerson inserts a person row through x (a *sql.DB or *sql.Tx) and
// returns the new person id.
func insertPerson(ctx context.Context, x execer, in contact.PersonInput) (string, error) {
	id := newID()
	ts := formatTime(now())
	_, err := x.ExecContext(ctx, `
		INSERT INTO persons (id, display_name, given_name, family_name, notes, source, source_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.DisplayName, in.GivenName, in.FamilyName, in.Notes, in.Source, in.SourceRef, ts, ts)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetPerson loads a person with its aliases, tags, classifications and
// contact points.
func (s *Service) GetPerson(ctx context.Context, id string) (*contact.Person, error) {
	const op = "contact.GetPerson"

	var p contact.Person
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, display_name, COALESCE(given_name, ''), COALESCE(family_name, ''), COALESCE(notes, ''), COALESCE(source, ''), COALESCE(source_ref, ''), created_at, updated_at
		FROM persons WHERE id = ?`, id,
	).Scan(&p.ID, &p.DisplayName, &p.GivenName, &p.FamilyName, &p.Notes, &p.Source, &p.SourceRef, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, errs.NotFound(op, "person not found", nil)
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load person", err)
	}
	p.CreatedAt, _ = parseTime(createdAt)
	p.UpdatedAt, _ = parseTime(updatedAt)

	ref := personRef(id)
	if p.Aliases, err = listAliases(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load aliases", err)
	}
	if p.Tags, err = listTags(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load tags", err)
	}
	if p.Classifications, err = listClassifications(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load classifications", err)
	}
	if p.ContactPoints, err = listContactPoints(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load contact points", err)
	}
	return &p, nil
}

// ListPersonsOptions filters and orders ListPersons.
type ListPersonsOptions struct {
	Classification contact.Classification // zero value: no filter
	Query          string                 // zero value: no filter; matched against name fields, case-insensitive substring
	Page           page.Request
}

// ListPersons returns persons ordered by display name, optionally filtered
// by classification and/or a free-text name query. Contact points/aliases/
// tags are not preloaded — call GetPerson for the full record.
func (s *Service) ListPersons(ctx context.Context, opts ListPersonsOptions) (page.Result[contact.Person], error) {
	const op = "contact.ListPersons"
	req := page.NewRequest(opts.Page.Limit, opts.Page.Offset)

	query := `
		SELECT DISTINCT p.id, p.display_name, COALESCE(p.given_name, ''), COALESCE(p.family_name, ''), COALESCE(p.notes, ''), COALESCE(p.source, ''), COALESCE(p.source_ref, ''), p.created_at, p.updated_at
		FROM persons p`
	args := []any{}
	if opts.Classification != "" {
		query += ` JOIN entity_classifications ec ON ec.person_id = p.id AND ec.classification = ?`
		args = append(args, string(opts.Classification))
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		query += ` WHERE (p.display_name LIKE ? ESCAPE '\' COLLATE NOCASE OR p.given_name LIKE ? ESCAPE '\' COLLATE NOCASE OR p.family_name LIKE ? ESCAPE '\' COLLATE NOCASE)`
		like := likePattern(q)
		args = append(args, like, like, like)
	}
	query += ` ORDER BY p.display_name COLLATE NOCASE, p.id LIMIT ? OFFSET ?`
	args = append(args, req.Limit+1, req.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page.Result[contact.Person]{}, errs.Internal(op, "failed to list persons", err)
	}
	defer rows.Close()

	var out []contact.Person
	for rows.Next() {
		var p contact.Person
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.GivenName, &p.FamilyName, &p.Notes, &p.Source, &p.SourceRef, &createdAt, &updatedAt); err != nil {
			return page.Result[contact.Person]{}, errs.Internal(op, "failed to scan person", err)
		}
		p.CreatedAt, _ = parseTime(createdAt)
		p.UpdatedAt, _ = parseTime(updatedAt)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return page.Result[contact.Person]{}, errs.Internal(op, "failed to iterate persons", err)
	}

	return page.Trim(out, req), nil
}

// UpdatePerson patches only the fields set in upd and bumps updated_at.
func (s *Service) UpdatePerson(ctx context.Context, id string, upd contact.PersonUpdate) (*contact.Person, error) {
	const op = "contact.UpdatePerson"

	sets := []string{"updated_at = ?"}
	args := []any{formatTime(now())}
	if upd.DisplayName != nil {
		if strings.TrimSpace(*upd.DisplayName) == "" {
			return nil, errs.Invalid(op, "display name must not be empty", nil)
		}
		sets = append(sets, "display_name = ?")
		args = append(args, *upd.DisplayName)
	}
	if upd.GivenName != nil {
		sets = append(sets, "given_name = ?")
		args = append(args, *upd.GivenName)
	}
	if upd.FamilyName != nil {
		sets = append(sets, "family_name = ?")
		args = append(args, *upd.FamilyName)
	}
	if upd.Notes != nil {
		sets = append(sets, "notes = ?")
		args = append(args, *upd.Notes)
	}
	args = append(args, id)

	res, err := s.db.ExecContext(ctx, "UPDATE persons SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return nil, errs.Internal(op, "failed to update person", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errs.NotFound(op, "person not found", nil)
	}
	return s.GetPerson(ctx, id)
}

// DeletePerson removes a person and cascades to its aliases, tags,
// classifications, contact points and memberships (ON DELETE CASCADE).
// Relationships and interactions (added in a later issue) apply their own
// deletion policy on top of this.
func (s *Service) DeletePerson(ctx context.Context, id string) error {
	const op = "contact.DeletePerson"
	res, err := s.db.ExecContext(ctx, "DELETE FROM persons WHERE id = ?", id)
	if err != nil {
		return errs.Internal(op, "failed to delete person", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.NotFound(op, "person not found", nil)
	}
	return nil
}

func (s *Service) AddPersonAlias(ctx context.Context, personID, alias string) error {
	if strings.TrimSpace(alias) == "" {
		return errs.Invalid("contact.AddPersonAlias", "alias must not be empty", nil)
	}
	if err := addAlias(ctx, s.db, personRef(personID), alias); err != nil {
		return errs.Internal("contact.AddPersonAlias", "failed to add alias", err)
	}
	return nil
}

func (s *Service) AddPersonTag(ctx context.Context, personID, tag string) error {
	if strings.TrimSpace(tag) == "" {
		return errs.Invalid("contact.AddPersonTag", "tag must not be empty", nil)
	}
	return sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return addTag(ctx, tx, personRef(personID), tag)
	})
}

func (s *Service) SetPersonClassification(ctx context.Context, personID string, c contact.Classification) error {
	return setClassification(ctx, s.db, personRef(personID), c)
}

func (s *Service) RemovePersonClassification(ctx context.Context, personID string, c contact.Classification) error {
	return removeClassification(ctx, s.db, personRef(personID), c)
}

func (s *Service) AddPersonContactPoint(ctx context.Context, personID string, in contact.ContactPointInput) (*contact.ContactPoint, error) {
	return addContactPoint(ctx, s.db, personRef(personID), in)
}

func (s *Service) RemovePersonContactPoint(ctx context.Context, personID, contactPointID string) error {
	return removeContactPoint(ctx, s.db, personRef(personID), contactPointID)
}
