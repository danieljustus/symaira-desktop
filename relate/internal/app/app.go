// Package app wires the storage layer to the domain services and is the
// behavior boundary the CLI (and, later, an MCP server) is allowed to
// depend on: no entry point may import internal/storage or a
// internal/service/* package directly. Pure data types in internal/domain
// are shared vocabulary and may be imported directly by entry points.
package app

import (
	"context"
	"database/sql"
	"io"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/errs"
	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
	importersvc "github.com/danieljustus/symaira-relate/internal/service/importer"
	memorylinksvc "github.com/danieljustus/symaira-relate/internal/service/memorylink"
	relationshipsvc "github.com/danieljustus/symaira-relate/internal/service/relationship"
	securitysvc "github.com/danieljustus/symaira-relate/internal/service/security"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
	"github.com/danieljustus/symaira-relate/internal/xdg"
)

// App is the process-wide service container. Fields are populated
// incrementally as domain packages land; every field is safe to use
// concurrently.
type App struct {
	Paths xdg.Paths
	db    *sql.DB

	Contacts      *contactsvc.Service
	Relationships *relationshipsvc.Service
	Security      *securitysvc.Service
	Import        *importersvc.Service
	MemoryLinks   *memorylinksvc.Service
}

// Aliases for the domain services' option types, so CLI commands can name
// them without importing internal/service/* directly.
type (
	ListPersonsOptions       = contactsvc.ListPersonsOptions
	ListOrganizationsOptions = contactsvc.ListOrganizationsOptions
	FollowUpFilter           = relationshipsvc.FollowUpFilter
)

const (
	FollowUpFilterAll      = relationshipsvc.FollowUpFilterAll
	FollowUpFilterOpen     = relationshipsvc.FollowUpFilterOpen
	FollowUpFilterOverdue  = relationshipsvc.FollowUpFilterOverdue
	FollowUpFilterUpcoming = relationshipsvc.FollowUpFilterUpcoming
)

// Open resolves XDG paths, ensures they exist, and opens the migrated
// database. Callers must Close the returned App.
func Open(ctx context.Context) (*App, error) {
	const op = "app.Open"

	paths, err := xdg.Resolve()
	if err != nil {
		return nil, errs.Internal(op, "failed to resolve XDG paths", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		return nil, errs.Internal(op, "failed to create profile directories", err)
	}

	db, err := sqlite.Open(ctx, paths.DatabasePath())
	if err != nil {
		return nil, err
	}

	return wire(paths, db), nil
}

// OpenAt opens a database at an explicit path instead of the resolved XDG
// data directory (used for restoring a backup into a clean profile, or for
// tests).
func OpenAt(ctx context.Context, dbPath string, paths xdg.Paths) (*App, error) {
	db, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	return wire(paths, db), nil
}

// OpenMemory opens an in-memory database, wired with the same service
// container as production. Intended for tests.
func OpenMemory(ctx context.Context) (*App, error) {
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		return nil, err
	}
	return wire(xdg.Paths{}, db), nil
}

func (a *App) Close() error {
	if a.db == nil {
		return nil
	}
	return a.db.Close()
}

// Ping checks database connectivity.
func (a *App) Ping(ctx context.Context) error {
	return a.db.PingContext(ctx)
}

// RestoreBackup decrypts a backup into targetDBPath. It does not require
// (or touch) an already-open App — see securitysvc.Restore.
func RestoreBackup(ctx context.Context, passphrase []byte, r io.Reader, targetDBPath string) error {
	return securitysvc.Restore(ctx, passphrase, r, targetDBPath)
}

// CreatePersonWithContactPoints creates a person and, when non-empty, an
// email and/or phone contact point in the same call — the "quick add"
// shape every front end (CLI `contact add`, the MCP contact_create tool,
// the web console) offers so a caller does not have to duplicate the
// create-then-add-point sequence. It is the single implementation of that
// use case; front ends call through it instead of re-deriving it. The
// whole operation runs in one transaction in the contact service: any
// failure rolls back the person.
func (a *App) CreatePersonWithContactPoints(ctx context.Context, in contact.PersonInput, email, phone string) (*contact.Person, error) {
	return a.Contacts.CreatePersonWithContactPoints(ctx, in, email, phone)
}
