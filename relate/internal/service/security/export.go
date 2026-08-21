package security

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/errs"
)

// ExportPerson is a data-minimized export record: identity and contact
// points only. Notes and audit metadata are deliberately left out of
// exports by default (see docs/PRIVACY.md's data-minimization policy).
type ExportPerson struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	GivenName   string   `json:"given_name,omitempty"`
	FamilyName  string   `json:"family_name,omitempty"`
	Emails      []string `json:"emails,omitempty"`
	Phones      []string `json:"phones,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// ExportJSON writes every person as a JSON array to w.
func (s *Service) ExportJSON(ctx context.Context, w io.Writer) error {
	const op = "security.ExportJSON"
	people, err := s.exportPeople(ctx)
	if err != nil {
		return errs.Internal(op, "failed to load persons for export", err)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(people); err != nil {
		return errs.Internal(op, "failed to write JSON export", err)
	}
	return recordAudit(ctx, s.db, "export_json", "", "", fmt.Sprintf("persons=%d", len(people)))
}

// ExportCSV writes one row per person (id, names, joined emails/phones/
// tags) to w.
func (s *Service) ExportCSV(ctx context.Context, w io.Writer) error {
	const op = "security.ExportCSV"
	people, err := s.exportPeople(ctx)
	if err != nil {
		return errs.Internal(op, "failed to load persons for export", err)
	}

	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "display_name", "given_name", "family_name", "emails", "phones", "tags"}); err != nil {
		return errs.Internal(op, "failed to write CSV header", err)
	}
	for _, p := range people {
		row := []string{p.ID, p.DisplayName, p.GivenName, p.FamilyName, strings.Join(p.Emails, ";"), strings.Join(p.Phones, ";"), strings.Join(p.Tags, ";")}
		if err := cw.Write(row); err != nil {
			return errs.Internal(op, "failed to write CSV row", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return errs.Internal(op, "failed to flush CSV", err)
	}
	return recordAudit(ctx, s.db, "export_csv", "", "", fmt.Sprintf("persons=%d", len(people)))
}

func (s *Service) exportPeople(ctx context.Context) ([]ExportPerson, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, display_name, given_name, family_name FROM persons ORDER BY display_name COLLATE NOCASE, id")
	if err != nil {
		return nil, err
	}
	var out []ExportPerson
	for rows.Next() {
		var p ExportPerson
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.GivenName, &p.FamilyName); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	cpRows, err := s.db.QueryContext(ctx,
		"SELECT person_id, kind, raw_value FROM contact_points ORDER BY person_id, kind, is_preferred DESC, created_at")
	if err != nil {
		return nil, err
	}
	cpMap := make(map[string]map[string][]string)
	for cpRows.Next() {
		var personID, kind, value string
		if err := cpRows.Scan(&personID, &kind, &value); err != nil {
			cpRows.Close()
			return nil, err
		}
		if cpMap[personID] == nil {
			cpMap[personID] = make(map[string][]string)
		}
		cpMap[personID][kind] = append(cpMap[personID][kind], value)
	}
	if err := cpRows.Err(); err != nil {
		cpRows.Close()
		return nil, err
	}
	cpRows.Close()

	tagRows, err := s.db.QueryContext(ctx,
		"SELECT et.person_id, t.name FROM tags t JOIN entity_tags et ON et.tag_id = t.id ORDER BY et.person_id, t.name")
	if err != nil {
		return nil, err
	}
	tagMap := make(map[string][]string)
	for tagRows.Next() {
		var personID, name string
		if err := tagRows.Scan(&personID, &name); err != nil {
			tagRows.Close()
			return nil, err
		}
		tagMap[personID] = append(tagMap[personID], name)
	}
	if err := tagRows.Err(); err != nil {
		tagRows.Close()
		return nil, err
	}
	tagRows.Close()

	for i := range out {
		if cp := cpMap[out[i].ID]; cp != nil {
			out[i].Emails = cp["email"]
			out[i].Phones = cp["phone"]
		}
		out[i].Tags = tagMap[out[i].ID]
	}
	return out, nil
}
