package security

import (
	"context"
	"database/sql"

	"github.com/danieljustus/symaira-relate/internal/domain/security"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// recordAudit inserts an audit event using x (a *sql.DB or *sql.Tx, so
// callers can fold the audit write into the same transaction as the
// mutation it describes). detail must already be scrubbed of sensitive
// values by the caller — see EraseSummary for the shape used by erase.
func recordAudit(ctx context.Context, x interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, operation, entityType, entityID, detail string) error {
	_, err := x.ExecContext(ctx, `
		INSERT INTO audit_events (id, occurred_at, operation, actor, entity_type, entity_id, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID(), formatTime(now()), operation, localActor(), nullable(entityType), nullable(entityID), detail)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ListAuditEvents returns every audit event, most recent first.
func (s *Service) ListAuditEvents(ctx context.Context) ([]security.AuditEvent, error) {
	const op = "security.ListAuditEvents"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, occurred_at, operation, actor, entity_type, entity_id, detail
		FROM audit_events ORDER BY occurred_at DESC, id DESC`)
	if err != nil {
		return nil, errs.Internal(op, "failed to list audit events", err)
	}
	defer rows.Close()

	var out []security.AuditEvent
	for rows.Next() {
		var e security.AuditEvent
		var occurredAt string
		var entityType, entityID, detail sql.NullString
		if err := rows.Scan(&e.ID, &occurredAt, &e.Operation, &e.Actor, &entityType, &entityID, &detail); err != nil {
			return nil, errs.Internal(op, "failed to scan audit event", err)
		}
		e.OccurredAt, _ = parseTime(occurredAt)
		e.EntityType = entityType.String
		e.EntityID = entityID.String
		e.Detail = detail.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Internal(op, "failed to iterate audit events", err)
	}
	return out, nil
}
