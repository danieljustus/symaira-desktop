package db

import (
	"database/sql"
	"embed"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies pending SQL migrations to the database.
func RunMigrations(conn *sql.DB) error {
	return sqlitekit.Migrate(conn, migrationsFS)
}
