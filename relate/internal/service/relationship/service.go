// Package relationship implements the relationship, interaction and
// follow-up use cases on top of the sqlite storage package.
package relationship

import (
	"database/sql"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

func newID() string {
	return uuid.NewString()
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

// entityRef identifies whether a child row (interaction, follow-up)
// belongs to a person or an organization.
type entityRef struct {
	column string // "person_id" or "organization_id"
	id     string
}

func personRef(id string) entityRef       { return entityRef{column: "person_id", id: id} }
func organizationRef(id string) entityRef { return entityRef{column: "organization_id", id: id} }

type rowScanner interface {
	Scan(dest ...any) error
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
