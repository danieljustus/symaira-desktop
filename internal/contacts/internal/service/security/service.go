// Package security implements the audit log, per-contact erase workflow,
// and encrypted backup/export use cases on top of the sqlite storage
// package.
package security

import (
	"database/sql"
	"os/user"
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

// localActor identifies who performed a sensitive mutation for the audit
// log. It is the local OS username — never contact data — and falls back
// to "unknown" rather than failing the operation.
func localActor() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}
