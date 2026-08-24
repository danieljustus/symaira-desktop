package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpen_CreatesWALDatabase(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "symrelate.db")

	db, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode error = %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys error = %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestMigrate_CleanDatabaseAppliesAllMigrations(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	defer db.Close()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("expected at least one embedded migration")
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("query schema_migrations error = %v", err)
	}
	if count != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", count, len(migrations))
	}
}

func TestMigrate_IsIdempotentOnUpgrade(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")

	db1, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if _, err := db1.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES ('probe', '1')"); err != nil {
		t.Fatalf("insert probe row error = %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close first handle error = %v", err)
	}

	// Reopening simulates an upgrade fixture: migrations must be a no-op
	// and prior data must survive untouched.
	db2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer db2.Close()

	var value string
	if err := db2.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'probe'").Scan(&value); err != nil {
		t.Fatalf("select probe row error = %v", err)
	}
	if value != "1" {
		t.Errorf("probe value = %q, want 1", value)
	}
}

func TestWithTx_CommitsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	defer db.Close()

	if err := WithTx(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES ('tx-commit', 'saved')")
		return err
	}); err != nil {
		t.Fatalf("committing transaction error = %v", err)
	}

	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'tx-commit'").Scan(&value); err != nil {
		t.Fatalf("committed row query error = %v", err)
	}
	if value != "saved" {
		t.Fatalf("committed value = %q, want saved", value)
	}

	rollbackErr := errors.New("abort transaction")
	if err := WithTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES ('tx-rollback', 'discarded')"); err != nil {
			return err
		}
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error = %v, want %v", err, rollbackErr)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE key = 'tx-rollback'").Scan(&count); err != nil {
		t.Fatalf("rolled-back row query error = %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back row count = %d, want 0", count)
	}
}

func TestParseMigrationFilename(t *testing.T) {
	version, name, err := parseMigrationFilename("0002_contacts.sql")
	if err != nil {
		t.Fatalf("parseMigrationFilename() error = %v", err)
	}
	if version != 2 || name != "contacts" {
		t.Errorf("got version=%d name=%q, want version=2 name=\"contacts\"", version, name)
	}

	if _, _, err := parseMigrationFilename("malformed.sql"); err == nil {
		t.Error("expected error for malformed filename, got nil")
	}
}

func TestSplitStatements_IgnoresSemicolonInsideLineComment(t *testing.T) {
	script := "-- a comment; that looks like a statement break\nCREATE TABLE t (id TEXT);\nCREATE TABLE u (id TEXT); -- trailing; comment"
	stmts := splitStatements(script)

	var nonEmpty []string
	for _, s := range stmts {
		if strings.TrimSpace(s) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(s))
		}
	}
	if len(nonEmpty) != 2 {
		t.Fatalf("got %d non-empty statements %q, want 2", len(nonEmpty), nonEmpty)
	}
	if nonEmpty[0] != "CREATE TABLE t (id TEXT)" {
		t.Errorf("statement 1 = %q, want \"CREATE TABLE t (id TEXT)\"", nonEmpty[0])
	}
	if nonEmpty[1] != "CREATE TABLE u (id TEXT)" {
		t.Errorf("statement 2 = %q, want \"CREATE TABLE u (id TEXT)\"", nonEmpty[1])
	}
}
