// Package importer implements the vCard/CSV import workflow's dedup
// planning and apply on top of the sqlite storage package and the
// contact service (import genuinely needs to create persons/
// organizations, so unlike relationship/security this package depends on
// contactsvc directly instead of re-issuing its own INSERTs).
package importer

import (
	"database/sql"
	"time"

	"github.com/google/uuid"

	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
)

type Service struct {
	db       *sql.DB
	contacts *contactsvc.Service
}

func New(db *sql.DB, contacts *contactsvc.Service) *Service {
	return &Service{db: db, contacts: contacts}
}

func newID() string {
	return uuid.NewString()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func now() time.Time {
	return time.Now().UTC()
}
