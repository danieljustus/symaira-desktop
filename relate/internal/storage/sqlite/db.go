// Package sqlite owns the CGO-free SQLite connection and the embedded
// migration set shared by every symrelate domain package. No other package
// may open the database directly.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver registered as "sqlite"

	"github.com/danieljustus/symaira-relate/internal/errs"
)

// Open opens (creating if necessary) the SQLite database at path, puts it in
// WAL mode, enables foreign keys, and applies any pending migrations. The
// returned *sql.DB is safe for concurrent use.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	const op = "sqlite.Open"

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errs.Internal(op, "failed to open database", err)
	}

	// SQLite has no real connection pool; a single writer avoids
	// "database is locked" churn under WAL.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, errs.Internal(op, "failed to apply pragma: "+pragma, err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// WithTx runs fn inside a transaction. It begins a transaction, defers
// a rollback (which is a no-op after a successful commit), calls fn,
// and commits on success. Any error from fn causes the transaction to
// roll back and the error to be returned.
func WithTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// OpenMemory opens an in-process, private SQLite database for tests. Each
// call returns an independent database.
func OpenMemory(ctx context.Context) (*sql.DB, error) {
	const op = "sqlite.OpenMemory"

	db, err := sql.Open("sqlite", "file::memory:?cache=private")
	if err != nil {
		return nil, errs.Internal(op, "failed to open in-memory database", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, errs.Internal(op, "failed to enable foreign keys", err)
	}

	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
