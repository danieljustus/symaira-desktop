// Package contact implements the beta contact, organization and
// contact-point use cases on top of the sqlite storage package. It is the
// only place SQL for these tables is written; callers (CLI, tests) go
// through Service.
package contact

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service provides person, organization, contact-point, alias, tag,
// classification and membership operations backed by a single SQLite
// database handle.
type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

func newID() string {
	return uuid.NewString()
}

// likePattern turns a free-text query into a substring LIKE pattern,
// escaping the two SQL wildcard characters so a literal "%" or "_" in a
// name search cannot be mistaken for a wildcard.
func likePattern(q string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + escaped + "%"
}

func now() time.Time {
	return time.Now().UTC()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// entityRef identifies which parent table/column a child row (contact
// point, alias, tag link, classification, ...) belongs to, so the same
// child-table logic serves both persons and organizations without
// duplicating every method.
type entityRef struct {
	column string // "person_id" or "organization_id"
	id     string
}

func personRef(id string) entityRef       { return entityRef{column: "person_id", id: id} }
func organizationRef(id string) entityRef { return entityRef{column: "organization_id", id: id} }

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
