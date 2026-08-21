package contact

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// -- aliases -----------------------------------------------------------

func addAlias(ctx context.Context, x execer, ref entityRef, alias string) error {
	_, err := x.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO aliases (id, %s, alias, created_at) VALUES (?, ?, ?, ?)", ref.column),
		newID(), ref.id, alias, formatTime(now()))
	return err
}

func listAliases(ctx context.Context, x execer, ref entityRef) ([]string, error) {
	rows, err := x.QueryContext(ctx,
		fmt.Sprintf("SELECT alias FROM aliases WHERE %s = ? ORDER BY created_at, id", ref.column), ref.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// -- tags ----------------------------------------------------------------

func addTag(ctx context.Context, tx *sql.Tx, ref entityRef, name string) error {
	tagID, err := findOrCreateTag(ctx, tx, name)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		fmt.Sprintf("INSERT OR IGNORE INTO entity_tags (id, tag_id, %s) VALUES (?, ?, ?)", ref.column),
		newID(), tagID, ref.id)
	return err
}

func findOrCreateTag(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM tags WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	id = newID()
	if _, err := tx.ExecContext(ctx, "INSERT INTO tags (id, name) VALUES (?, ?)", id, name); err != nil {
		return "", err
	}
	return id, nil
}

func listTags(ctx context.Context, x execer, ref entityRef) ([]string, error) {
	rows, err := x.QueryContext(ctx, fmt.Sprintf(`
		SELECT t.name FROM tags t
		JOIN entity_tags et ON et.tag_id = t.id
		WHERE et.%s = ?
		ORDER BY t.name`, ref.column), ref.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// -- classifications -------------------------------------------------------

func setClassification(ctx context.Context, x execer, ref entityRef, c contact.Classification) error {
	if !c.Valid() {
		return errs.Invalid("contact.setClassification", "unknown classification: "+string(c), nil)
	}
	_, err := x.ExecContext(ctx,
		fmt.Sprintf("INSERT OR IGNORE INTO entity_classifications (id, %s, classification, created_at) VALUES (?, ?, ?, ?)", ref.column),
		newID(), ref.id, string(c), formatTime(now()))
	return err
}

func removeClassification(ctx context.Context, x execer, ref entityRef, c contact.Classification) error {
	_, err := x.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM entity_classifications WHERE %s = ? AND classification = ?", ref.column),
		ref.id, string(c))
	return err
}

func listClassifications(ctx context.Context, x execer, ref entityRef) ([]contact.Classification, error) {
	rows, err := x.QueryContext(ctx,
		fmt.Sprintf("SELECT classification FROM entity_classifications WHERE %s = ? ORDER BY classification", ref.column), ref.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contact.Classification
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, contact.Classification(c))
	}
	return out, rows.Err()
}

// -- contact points --------------------------------------------------------

func addContactPoint(ctx context.Context, x execer, ref entityRef, in contact.ContactPointInput) (*contact.ContactPoint, error) {
	const op = "contact.addContactPoint"
	if !in.Kind.Valid() {
		return nil, errs.Invalid(op, "unknown contact point kind: "+string(in.Kind), nil)
	}
	if in.RawValue == "" {
		return nil, errs.Invalid(op, "contact point value must not be empty", nil)
	}

	id := newID()
	ts := formatTime(now())
	normalized := contact.Normalize(in.Kind, in.RawValue)

	_, err := x.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO contact_points (id, %s, kind, raw_value, normalized_value, label, is_preferred, is_verified, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ref.column),
		id, ref.id, string(in.Kind), in.RawValue, normalized, in.Label, boolToInt(in.IsPreferred), boolToInt(in.IsVerified), ts, ts)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, errs.Conflict(op, "an identical contact point already exists on this entity", err)
		}
		return nil, err
	}

	createdAt, _ := parseTime(ts)
	return &contact.ContactPoint{
		ID: id, Kind: in.Kind, RawValue: in.RawValue, NormalizedValue: normalized,
		Label: in.Label, IsPreferred: in.IsPreferred, IsVerified: in.IsVerified,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}, nil
}

func listContactPoints(ctx context.Context, x execer, ref entityRef) ([]contact.ContactPoint, error) {
	rows, err := x.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, kind, raw_value, normalized_value, COALESCE(label, ''), is_preferred, is_verified, created_at, updated_at
		FROM contact_points WHERE %s = ? ORDER BY is_preferred DESC, created_at, id`, ref.column), ref.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contact.ContactPoint
	for rows.Next() {
		var cp contact.ContactPoint
		var kind, createdAt, updatedAt string
		var isPreferred, isVerified int
		if err := rows.Scan(&cp.ID, &kind, &cp.RawValue, &cp.NormalizedValue, &cp.Label, &isPreferred, &isVerified, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		cp.Kind = contact.ContactPointKind(kind)
		cp.IsPreferred = isPreferred == 1
		cp.IsVerified = isVerified == 1
		cp.CreatedAt, _ = parseTime(createdAt)
		cp.UpdatedAt, _ = parseTime(updatedAt)
		out = append(out, cp)
	}
	return out, rows.Err()
}

func removeContactPoint(ctx context.Context, x execer, ref entityRef, contactPointID string) error {
	res, err := x.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM contact_points WHERE id = ? AND %s = ?", ref.column), contactPointID, ref.id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errs.NotFound("contact.removeContactPoint", "contact point not found", nil)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isForeignKeyErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

func isCheckConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "CHECK constraint failed")
}
