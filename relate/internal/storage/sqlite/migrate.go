package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"sort"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/errs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version  int
	name     string
	filename string
	sql      string
}

// Migrate applies every embedded migration that has not yet been recorded
// in schema_migrations, in ascending version order, each inside its own
// transaction. It is safe to call on every startup.
func Migrate(ctx context.Context, db *sql.DB) error {
	const op = "sqlite.Migrate"

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)`); err != nil {
		return errs.Internal(op, "failed to create schema_migrations", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return errs.Internal(op, "failed to load embedded migrations", err)
	}

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return errs.Internal(op, "failed to read schema_migrations", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return errs.Internal(op, "failed to scan schema_migrations", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return errs.Internal(op, "failed to iterate schema_migrations", err)
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return errs.Internal(op, "migration "+m.filename+" failed", err)
		}
	}

	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, stmt := range splitStatements(m.sql) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES (?, ?)", m.version, m.name); err != nil {
		return err
	}

	return tx.Commit()
}

// splitStatements strips "--" line comments (so a semicolon inside a
// comment can't be mistaken for a statement terminator) and splits the
// remainder on ";". Migration files must not rely on ";" inside string
// literals or triggers; this keeps the runner dependency-free and
// auditable.
func splitStatements(script string) []string {
	var stripped strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		stripped.WriteString(line)
		stripped.WriteByte('\n')
	}
	return strings.Split(stripped.String(), ";")
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}

	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationFilename(e.Name())
		if err != nil {
			return nil, err
		}
		content, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: version, name: name, filename: e.Name(), sql: string(content)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseMigrationFilename expects "%04d_name.sql".
func parseMigrationFilename(filename string) (version int, name string, err error) {
	base := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 {
		return 0, "", errs.Invalid("sqlite.parseMigrationFilename", "malformed migration filename: "+filename, nil)
	}
	version, convErr := strconv.Atoi(parts[0])
	if convErr != nil {
		return 0, "", errs.Invalid("sqlite.parseMigrationFilename", "non-numeric migration version: "+filename, convErr)
	}
	return version, parts[1], nil
}
