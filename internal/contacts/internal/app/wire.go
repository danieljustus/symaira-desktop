package app

import (
	"database/sql"

	contactsvc "github.com/danieljustus/symaira-desktop/internal/contacts/internal/service/contact"
	importersvc "github.com/danieljustus/symaira-desktop/internal/contacts/internal/service/importer"
	memorylinksvc "github.com/danieljustus/symaira-desktop/internal/contacts/internal/service/memorylink"
	relationshipsvc "github.com/danieljustus/symaira-desktop/internal/contacts/internal/service/relationship"
	securitysvc "github.com/danieljustus/symaira-desktop/internal/contacts/internal/service/security"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/xdg"
)

// wire constructs the App and its service container. Later issues extend
// this function as new domain services land; it stays the single place
// that assembles the dependency graph.
func wire(paths xdg.Paths, db *sql.DB) *App {
	contacts := contactsvc.New(db)
	return &App{
		Paths:         paths,
		db:            db,
		Contacts:      contacts,
		Relationships: relationshipsvc.New(db),
		Security:      securitysvc.New(db),
		Import:        importersvc.New(db, contacts),
		MemoryLinks:   memorylinksvc.New(db),
	}
}
