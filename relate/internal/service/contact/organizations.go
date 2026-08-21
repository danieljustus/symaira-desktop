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

func (s *Service) CreateOrganization(ctx context.Context, in contact.OrganizationInput) (*contact.Organization, error) {
	const op = "contact.CreateOrganization"
	if strings.TrimSpace(in.Name) == "" {
		return nil, errs.Invalid(op, "name must not be empty", nil)
	}

	id := newID()
	ts := formatTime(now())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO organizations (id, name, notes, source, source_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, in.Notes, in.Source, in.SourceRef, ts, ts)
	if err != nil {
		return nil, errs.Internal(op, "failed to insert organization", err)
	}
	return s.GetOrganization(ctx, id)
}

func (s *Service) GetOrganization(ctx context.Context, id string) (*contact.Organization, error) {
	const op = "contact.GetOrganization"

	var o contact.Organization
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, COALESCE(notes, ''), COALESCE(source, ''), COALESCE(source_ref, ''), created_at, updated_at
		FROM organizations WHERE id = ?`, id,
	).Scan(&o.ID, &o.Name, &o.Notes, &o.Source, &o.SourceRef, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, errs.NotFound(op, "organization not found", nil)
	}
	if err != nil {
		return nil, errs.Internal(op, "failed to load organization", err)
	}
	o.CreatedAt, _ = parseTime(createdAt)
	o.UpdatedAt, _ = parseTime(updatedAt)

	ref := organizationRef(id)
	if o.Aliases, err = listAliases(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load aliases", err)
	}
	if o.Tags, err = listTags(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load tags", err)
	}
	if o.Classifications, err = listClassifications(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load classifications", err)
	}
	if o.ContactPoints, err = listContactPoints(ctx, s.db, ref); err != nil {
		return nil, errs.Internal(op, "failed to load contact points", err)
	}
	return &o, nil
}

type ListOrganizationsOptions struct {
	Classification contact.Classification
	Query          string // zero value: no filter; matched against name, case-insensitive substring
	Page           page.Request
}

func (s *Service) ListOrganizations(ctx context.Context, opts ListOrganizationsOptions) (page.Result[contact.Organization], error) {
	const op = "contact.ListOrganizations"
	req := page.NewRequest(opts.Page.Limit, opts.Page.Offset)

	query := `
		SELECT DISTINCT o.id, o.name, COALESCE(o.notes, ''), COALESCE(o.source, ''), COALESCE(o.source_ref, ''), o.created_at, o.updated_at
		FROM organizations o`
	args := []any{}
	if opts.Classification != "" {
		query += ` JOIN entity_classifications ec ON ec.organization_id = o.id AND ec.classification = ?`
		args = append(args, string(opts.Classification))
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		query += ` WHERE o.name LIKE ? ESCAPE '\' COLLATE NOCASE`
		args = append(args, likePattern(q))
	}
	query += ` ORDER BY o.name COLLATE NOCASE, o.id LIMIT ? OFFSET ?`
	args = append(args, req.Limit+1, req.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page.Result[contact.Organization]{}, errs.Internal(op, "failed to list organizations", err)
	}
	defer rows.Close()

	var out []contact.Organization
	for rows.Next() {
		var o contact.Organization
		var createdAt, updatedAt string
		if err := rows.Scan(&o.ID, &o.Name, &o.Notes, &o.Source, &o.SourceRef, &createdAt, &updatedAt); err != nil {
			return page.Result[contact.Organization]{}, errs.Internal(op, "failed to scan organization", err)
		}
		o.CreatedAt, _ = parseTime(createdAt)
		o.UpdatedAt, _ = parseTime(updatedAt)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return page.Result[contact.Organization]{}, errs.Internal(op, "failed to iterate organizations", err)
	}

	return page.Trim(out, req), nil
}

func (s *Service) UpdateOrganization(ctx context.Context, id string, upd contact.OrganizationUpdate) (*contact.Organization, error) {
	const op = "contact.UpdateOrganization"

	sets := []string{"updated_at = ?"}
	args := []any{formatTime(now())}
	if upd.Name != nil {
		if strings.TrimSpace(*upd.Name) == "" {
			return nil, errs.Invalid(op, "name must not be empty", nil)
		}
		sets = append(sets, "name = ?")
		args = append(args, *upd.Name)
	}
	if upd.Notes != nil {
		sets = append(sets, "notes = ?")
		args = append(args, *upd.Notes)
	}
	args = append(args, id)

	res, err := s.db.ExecContext(ctx, "UPDATE organizations SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return nil, errs.Internal(op, "failed to update organization", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errs.NotFound(op, "organization not found", nil)
	}
	return s.GetOrganization(ctx, id)
}

// DeleteOrganization removes an organization and cascades to its aliases,
// tags, classifications, contact points and memberships.
func (s *Service) DeleteOrganization(ctx context.Context, id string) error {
	const op = "contact.DeleteOrganization"
	res, err := s.db.ExecContext(ctx, "DELETE FROM organizations WHERE id = ?", id)
	if err != nil {
		return errs.Internal(op, "failed to delete organization", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.NotFound(op, "organization not found", nil)
	}
	return nil
}

func (s *Service) AddOrganizationAlias(ctx context.Context, orgID, alias string) error {
	if strings.TrimSpace(alias) == "" {
		return errs.Invalid("contact.AddOrganizationAlias", "alias must not be empty", nil)
	}
	if err := addAlias(ctx, s.db, organizationRef(orgID), alias); err != nil {
		return errs.Internal("contact.AddOrganizationAlias", "failed to add alias", err)
	}
	return nil
}

func (s *Service) AddOrganizationTag(ctx context.Context, orgID, tag string) error {
	if strings.TrimSpace(tag) == "" {
		return errs.Invalid("contact.AddOrganizationTag", "tag must not be empty", nil)
	}
	return sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return addTag(ctx, tx, organizationRef(orgID), tag)
	})
}

func (s *Service) SetOrganizationClassification(ctx context.Context, orgID string, c contact.Classification) error {
	return setClassification(ctx, s.db, organizationRef(orgID), c)
}

func (s *Service) RemoveOrganizationClassification(ctx context.Context, orgID string, c contact.Classification) error {
	return removeClassification(ctx, s.db, organizationRef(orgID), c)
}

func (s *Service) AddOrganizationContactPoint(ctx context.Context, orgID string, in contact.ContactPointInput) (*contact.ContactPoint, error) {
	return addContactPoint(ctx, s.db, organizationRef(orgID), in)
}

func (s *Service) RemoveOrganizationContactPoint(ctx context.Context, orgID, contactPointID string) error {
	return removeContactPoint(ctx, s.db, organizationRef(orgID), contactPointID)
}
