package importer

import (
	"context"

	"github.com/danieljustus/symaira-relate/internal/errs"
)

// Run is one recorded import_runs row — a report of what a past Apply
// call did.
type Run struct {
	ID         string
	SourceKind string
	StartedAt  string
	Created    int
	Merged     int
	Skipped    int
	Failed     int
}

// ListRuns returns every recorded import run, most recent first.
func (s *Service) ListRuns(ctx context.Context) ([]Run, error) {
	const op = "importer.ListRuns"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_kind, started_at, created, merged, skipped, failed
		FROM import_runs ORDER BY started_at DESC, id DESC`)
	if err != nil {
		return nil, errs.Internal(op, "failed to list import runs", err)
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.SourceKind, &r.StartedAt, &r.Created, &r.Merged, &r.Skipped, &r.Failed); err != nil {
			return nil, errs.Internal(op, "failed to scan import run", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Internal(op, "failed to iterate import runs", err)
	}
	return out, nil
}
