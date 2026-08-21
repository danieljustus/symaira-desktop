package importer

import (
	"context"
	"sort"
	"strconv"
	"strings"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/importer"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// batchChunkSize is the maximum number of IN-clause parameters per
// query. SQLite's default limit is 999; we use a lower value for
// headroom.
const batchChunkSize = 500

// Plan computes duplicate candidates for rows already parsed by
// ParseVCard/ParseCSV. It only reads — a dry run is simply "call Plan
// and show the result," and Apply is a separate, explicit step, so
// there is no code path where planning alone can write data.
//
// Candidate lookup is batched: one constant number of queries (at most
// four) is issued regardless of row count, instead of one query per row
// per lookup type.
func (s *Service) Plan(ctx context.Context, source importer.SourceKind, rows []importer.ImportRow, issues []importer.ValidationIssue) (*importer.ImportPlan, error) {
	const op = "importer.Plan"

	plan := &importer.ImportPlan{Source: source, Rows: rows, Issues: issues}
	if len(rows) == 0 {
		return plan, nil
	}

	// Collect all unique lookup keys across every row so the batch
	// queries hit each value exactly once.
	sourceRefs := map[string]bool{}
	emails := map[string]bool{}
	phones := map[string]bool{}
	names := map[string]bool{}

	for _, row := range rows {
		if row.SourceRef != "" {
			sourceRefs[row.SourceRef] = true
		}
		for _, email := range row.Emails {
			if n := contactdomain.NormalizeEmail(email); n != "" {
				emails[n] = true
			}
		}
		for _, phone := range row.Phones {
			if n := contactdomain.NormalizePhone(phone); n != "" {
				phones[n] = true
			}
		}
		if row.DisplayName != "" {
			names[row.DisplayName] = true
		}
	}

	// Execute batch queries (at most four, independent of row count).
	sourceMap, err := s.batchFindImportSources(ctx, source, setKeys(sourceRefs))
	if err != nil {
		return nil, errs.Internal(op, "failed to query import sources", err)
	}
	emailMap, err := s.batchContactPointMatches(ctx, contactdomain.ContactPointEmail, setKeys(emails))
	if err != nil {
		return nil, errs.Internal(op, "failed to query email matches", err)
	}
	phoneMap, err := s.batchContactPointMatches(ctx, contactdomain.ContactPointPhone, setKeys(phones))
	if err != nil {
		return nil, errs.Internal(op, "failed to query phone matches", err)
	}
	nameMap, err := s.batchNameMatches(ctx, setKeys(names))
	if err != nil {
		return nil, errs.Internal(op, "failed to query name matches", err)
	}

	// Build candidates for each row from the pre-fetched maps.
	for _, row := range rows {
		candidates := candidatesFromMaps(row, source, sourceMap, emailMap, phoneMap, nameMap)
		plan.Duplicates = append(plan.Duplicates, candidates...)
	}

	return plan, nil
}

// setKeys returns the keys of a bool-set map in sorted order for
// deterministic query building.
func setKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// candidatesFromMaps builds duplicate candidates for a single row
// using pre-fetched lookup maps, producing identical output to the
// former per-row query path.
func candidatesFromMaps(
	row importer.ImportRow,
	source importer.SourceKind,
	sourceMap map[string]personMatch,
	emailMap map[string][]personMatch,
	phoneMap map[string][]personMatch,
	nameMap map[string][]personMatch,
) []importer.DuplicateCandidate {
	var out []importer.DuplicateCandidate
	seen := map[string]bool{} // personID → already emitted

	// 1. Exact idempotency match: this exact source was imported before.
	if row.SourceRef != "" {
		if m, ok := sourceMap[row.SourceRef]; ok {
			out = append(out, importer.DuplicateCandidate{
				RowNumber: row.RowNumber, ExistingPersonID: m.id, ExistingName: m.name,
				MatchedOn: "source:" + string(source) + ":" + row.SourceRef, ExactSourceRematch: true,
			})
			seen[m.id] = true
		}
	}

	// 2. Normalized contact-point matches.
	for _, email := range row.Emails {
		normalized := contactdomain.NormalizeEmail(email)
		if normalized == "" {
			continue
		}
		for _, m := range emailMap[normalized] {
			if seen[m.id] {
				continue
			}
			out = append(out, importer.DuplicateCandidate{RowNumber: row.RowNumber, ExistingPersonID: m.id, ExistingName: m.name, MatchedOn: "email:" + email})
		}
	}
	for _, phone := range row.Phones {
		normalized := contactdomain.NormalizePhone(phone)
		if normalized == "" {
			continue
		}
		for _, m := range phoneMap[normalized] {
			if seen[m.id] {
				continue
			}
			out = append(out, importer.DuplicateCandidate{RowNumber: row.RowNumber, ExistingPersonID: m.id, ExistingName: m.name, MatchedOn: "phone:" + phone})
		}
	}

	// 3. Normalized exact name match — a weaker signal, still
	// deterministic and explainable.
	if row.DisplayName != "" {
		for _, m := range nameMap[row.DisplayName] {
			if seen[m.id] {
				continue
			}
			out = append(out, importer.DuplicateCandidate{RowNumber: row.RowNumber, ExistingPersonID: m.id, ExistingName: m.name, MatchedOn: "name:" + row.DisplayName})
		}
	}

	return dedupeCandidates(out)
}

// dedupeCandidates collapses multiple candidates for the same row+person
// down to the strongest match: an exact source rematch wins over a
// contact-point/name match, and only one entry per (row, person) survives.
func dedupeCandidates(in []importer.DuplicateCandidate) []importer.DuplicateCandidate {
	best := map[[2]string]importer.DuplicateCandidate{}
	var order [][2]string
	for _, c := range in {
		key := [2]string{strconv.Itoa(c.RowNumber), c.ExistingPersonID}
		existing, ok := best[key]
		if !ok {
			best[key] = c
			order = append(order, key)
			continue
		}
		if c.ExactSourceRematch && !existing.ExactSourceRematch {
			best[key] = c
		}
	}
	out := make([]importer.DuplicateCandidate, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	return out
}

type personMatch struct{ id, name string }

// --- batch query helpers ---------------------------------------------------

func (s *Service) batchFindImportSources(ctx context.Context, source importer.SourceKind, refs []string) (map[string]personMatch, error) {
	result := make(map[string]personMatch, len(refs))
	if len(refs) == 0 {
		return result, nil
	}
	for i := 0; i < len(refs); i += batchChunkSize {
		chunk := refs[i:min(i+batchChunkSize, len(refs))]
		ph := placeholders(len(chunk))
		args := make([]any, 0, 1+len(chunk))
		args = append(args, string(source))
		for _, ref := range chunk {
			args = append(args, ref)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT src.source_ref, p.id, p.display_name FROM import_sources src
			JOIN persons p ON p.id = src.person_id
			WHERE src.source_kind = ? AND src.source_ref IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ref, id, name string
			if err := rows.Scan(&ref, &id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			result[ref] = personMatch{id: id, name: name}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func (s *Service) batchContactPointMatches(ctx context.Context, kind contactdomain.ContactPointKind, normalizedValues []string) (map[string][]personMatch, error) {
	result := make(map[string][]personMatch, len(normalizedValues))
	if len(normalizedValues) == 0 {
		return result, nil
	}
	for i := 0; i < len(normalizedValues); i += batchChunkSize {
		chunk := normalizedValues[i:min(i+batchChunkSize, len(normalizedValues))]
		ph := placeholders(len(chunk))
		args := make([]any, 0, 1+len(chunk))
		args = append(args, string(kind))
		for _, v := range chunk {
			args = append(args, v)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT cp.normalized_value, p.id, p.display_name FROM contact_points cp
			JOIN persons p ON p.id = cp.person_id
			WHERE cp.kind = ? AND cp.normalized_value IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var nv, id, name string
			if err := rows.Scan(&nv, &id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			result[nv] = append(result[nv], personMatch{id: id, name: name})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func (s *Service) batchNameMatches(ctx context.Context, names []string) (map[string][]personMatch, error) {
	result := make(map[string][]personMatch, len(names))
	if len(names) == 0 {
		return result, nil
	}
	for i := 0; i < len(names); i += batchChunkSize {
		chunk := names[i:min(i+batchChunkSize, len(names))]
		ph := placeholders(len(chunk))
		args := make([]any, 0, len(chunk))
		for _, name := range chunk {
			args = append(args, name)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, display_name FROM persons
			WHERE display_name COLLATE NOCASE IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, displayName string
			if err := rows.Scan(&id, &displayName); err != nil {
				rows.Close()
				return nil, err
			}
			// Map the result back to every queried name that matches
			// case-insensitively, preserving per-row iteration order.
			for _, name := range chunk {
				if strings.EqualFold(name, displayName) {
					result[name] = append(result[name], personMatch{id: id, name: displayName})
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('?')
	for i := 1; i < n; i++ {
		b.WriteString(", ?")
	}
	return b.String()
}
