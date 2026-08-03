package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	_ "modernc.org/sqlite"
)

// SQLiteBackend implements StorageBackend with the repository's pure-Go
// SQLite driver. It owns the database connection passed in by OpenSQLite.
// Its storage_entries table is independent from the rebuildable sidecar
// schema.
type SQLiteBackend struct {
	db *sql.DB
}

var _ StorageBackend = (*SQLiteBackend)(nil)

// OpenSQLite opens a persistent SQLite storage database at path and creates
// the key/value table if needed. The path must identify a separate storage
// database; it must not be the sidecar database path.
func OpenSQLite(path string) (*SQLiteBackend, error) {
	if path == "" {
		return nil, errors.New("storage: SQLite path must not be empty")
	}

	db, err := sqlitekit.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open storage SQLite database: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS storage_entries (
			storage_key TEXT PRIMARY KEY NOT NULL COLLATE BINARY,
			value BLOB NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize storage SQLite database: %w", err)
	}

	return &SQLiteBackend{db: db}, nil
}

// Close closes the SQLite database connection. It is safe to call repeatedly.
func (b *SQLiteBackend) Close() error {
	return b.db.Close()
}

// Get returns the value stored under key, or an error wrapping ErrNotFound
// when key is absent.
func (b *SQLiteBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if err := validateKey(ctx, key); err != nil {
		return nil, err
	}

	var value []byte
	err := b.db.QueryRowContext(ctx,
		"SELECT value FROM storage_entries WHERE storage_key = ?", key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("get storage key %q: %w", key, err)
	}
	return cloneBytes(value), nil
}

// Set stores value under key, replacing any existing value. A nil value is
// stored as a non-nil empty value.
func (b *SQLiteBackend) Set(ctx context.Context, key string, value []byte) error {
	if err := validateKey(ctx, key); err != nil {
		return err
	}

	_, err := b.db.ExecContext(ctx, `
		INSERT INTO storage_entries (storage_key, value) VALUES (?, ?)
		ON CONFLICT(storage_key) DO UPDATE SET value = excluded.value`, key, cloneBytes(value))
	if err != nil {
		return fmt.Errorf("set storage key %q: %w", key, err)
	}
	return nil
}

// Remove deletes key. Removing an absent key succeeds.
func (b *SQLiteBackend) Remove(ctx context.Context, key string) error {
	if err := validateKey(ctx, key); err != nil {
		return err
	}

	if _, err := b.db.ExecContext(ctx,
		"DELETE FROM storage_entries WHERE storage_key = ?", key,
	); err != nil {
		return fmt.Errorf("remove storage key %q: %w", key, err)
	}
	return nil
}

// Query returns entries whose keys begin with prefix, sorted by key. An empty
// prefix returns every entry.
func (b *SQLiteBackend) Query(ctx context.Context, prefix string) ([]Entry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	rows, err := b.db.QueryContext(ctx, `
		SELECT storage_key, value
		FROM storage_entries
		WHERE substr(storage_key, 1, length(?)) = ? COLLATE BINARY
		ORDER BY storage_key ASC`, prefix, prefix)
	if err != nil {
		return nil, fmt.Errorf("query storage keys with prefix %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()

	entries := make([]Entry, 0)
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.Key, &entry.Value); err != nil {
			return nil, fmt.Errorf("scan storage entry: %w", err)
		}
		entry.Value = cloneBytes(entry.Value)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate storage entries: %w", err)
	}
	return entries, nil
}

func validateKey(ctx context.Context, key string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if key == "" {
		return ErrInvalidKey
	}
	return nil
}

func contextError(ctx context.Context) error {
	return ctx.Err()
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}
