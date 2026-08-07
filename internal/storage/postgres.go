package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxPool is the minimal connection-pool surface PostgresBackend needs. It
// is satisfied by *pgxpool.Pool in production and by pgxmock in tests.
type pgxPool interface {
	Ping(ctx context.Context) error
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Close()
}

// PostgresBackend implements StorageBackend on PostgreSQL for production
// self-hosting deployments. It owns the connection pool passed in by
// OpenPostgres. The storage_entries table mirrors the SQLite adapter's shape
// so both backends expose identical key/value semantics; keys are sorted in
// byte order (COLLATE "C") to match the contract's "sorted by key" guarantee
// regardless of the cluster's locale.
type PostgresBackend struct {
	pool pgxPool
}

var _ StorageBackend = (*PostgresBackend)(nil)
var _ pgxPool = (*pgxpool.Pool)(nil)

// OpenPostgres connects to PostgreSQL using a pgx connection string or URL
// (e.g. "postgres://user:pass@host:5432/dbname" or the pgx keyword format)
// and ensures the storage_entries table exists. It pings the database so a
// misconfigured production backend fails at startup instead of on the first
// request. The caller must call Close when done.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresBackend, error) {
	if dsn == "" {
		return nil, errors.New("storage: PostgreSQL DSN must not be empty")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS storage_entries (
			storage_key TEXT PRIMARY KEY NOT NULL,
			value BYTEA NOT NULL
		)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("initialize PostgreSQL storage table: %w", err)
	}

	return &PostgresBackend{pool: pool}, nil
}

// Close closes the connection pool. It is safe to call repeatedly.
func (b *PostgresBackend) Close() error {
	b.pool.Close()
	return nil
}

// Get returns the value stored under key, or an error wrapping ErrNotFound
// when key is absent.
func (b *PostgresBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if err := validateKey(ctx, key); err != nil {
		return nil, err
	}

	var value []byte
	err := b.pool.QueryRow(ctx,
		"SELECT value FROM storage_entries WHERE storage_key = $1", key,
	).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("get storage key %q: %w", key, err)
	}
	return cloneBytes(value), nil
}

// Set stores value under key, replacing any existing value. A nil value is
// stored as a non-nil empty value.
func (b *PostgresBackend) Set(ctx context.Context, key string, value []byte) error {
	if err := validateKey(ctx, key); err != nil {
		return err
	}

	_, err := b.pool.Exec(ctx, `
		INSERT INTO storage_entries (storage_key, value) VALUES ($1, $2)
		ON CONFLICT(storage_key) DO UPDATE SET value = EXCLUDED.value`, key, cloneBytes(value))
	if err != nil {
		return fmt.Errorf("set storage key %q: %w", key, err)
	}
	return nil
}

// Remove deletes key. Removing an absent key succeeds.
func (b *PostgresBackend) Remove(ctx context.Context, key string) error {
	if err := validateKey(ctx, key); err != nil {
		return err
	}

	if _, err := b.pool.Exec(ctx,
		"DELETE FROM storage_entries WHERE storage_key = $1", key,
	); err != nil {
		return fmt.Errorf("remove storage key %q: %w", key, err)
	}
	return nil
}

// Query returns entries whose keys begin with prefix, sorted by key in byte
// order. An empty prefix returns every entry.
func (b *PostgresBackend) Query(ctx context.Context, prefix string) ([]Entry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	rows, err := b.pool.Query(ctx, `
		SELECT storage_key, value
		FROM storage_entries
		WHERE substr(storage_key, 1, length($1)) = $1
		ORDER BY storage_key COLLATE "C" ASC`, prefix)
	if err != nil {
		return nil, fmt.Errorf("query storage keys with prefix %q: %w", prefix, err)
	}
	defer rows.Close()

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
