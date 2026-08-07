//go:build postgres

// Integration test for the PostgreSQL backend. Not part of the default
// `make test` run — it requires a reachable PostgreSQL instance.
//
// Usage:
//
//	createdb symdesk_storage_test   # once
//	TEST_DATABASE_URL='postgres://localhost:5432/symdesk_storage_test?sslmode=disable' \
//	    go test -tags postgres -count=1 ./internal/storage/ -run TestPostgres
//
// The test drops and recreates the storage_entries table so repeated runs are
// idempotent.
package storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func openTestPostgres(t *testing.T) *PostgresBackend {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	backend, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	// Idempotent run: start from a clean table.
	if _, err := backend.pool.Exec(ctx, "DROP TABLE IF EXISTS storage_entries"); err != nil {
		t.Fatalf("drop storage table: %v", err)
	}
	reopened, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen postgres backend: %v", err)
	}
	*backend = *reopened
	return backend
}

func TestPostgresGetSetRemoveQuery(t *testing.T) {
	backend := openTestPostgres(t)
	ctx := context.Background()

	if _, err := backend.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}

	if err := backend.Set(ctx, "config:port", []byte("8787")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := backend.Set(ctx, "config:host", []byte("127.0.0.1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := backend.Set(ctx, "session:abc", []byte(`{"user":"admin"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := backend.Get(ctx, "config:port")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "8787" {
		t.Fatalf("Get = %q, want %q", got, "8787")
	}

	// Set replaces.
	if err := backend.Set(ctx, "config:port", []byte("9000")); err != nil {
		t.Fatalf("Set replace: %v", err)
	}
	got, _ = backend.Get(ctx, "config:port")
	if string(got) != "9000" {
		t.Fatalf("Get after replace = %q, want %q", got, "9000")
	}

	// Query with prefix, sorted by key in byte order.
	entries, err := backend.Query(ctx, "config:")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Query(config:) = %d entries, want 2", len(entries))
	}
	if entries[0].Key != "config:host" || entries[1].Key != "config:port" {
		t.Fatalf("Query order = [%s %s], want [config:host config:port]", entries[0].Key, entries[1].Key)
	}

	// Empty prefix returns everything.
	all, err := backend.Query(ctx, "")
	if err != nil {
		t.Fatalf("Query(\"\"): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("Query(\"\") = %d entries, want 3", len(all))
	}

	// Remove is idempotent.
	if err := backend.Remove(ctx, "session:abc"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := backend.Remove(ctx, "session:abc"); err != nil {
		t.Fatalf("Remove absent: %v", err)
	}
	if _, err := backend.Get(ctx, "session:abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Remove = %v, want ErrNotFound", err)
	}
}

func TestPostgresValueIsolation(t *testing.T) {
	backend := openTestPostgres(t)
	ctx := context.Background()

	// The backend must not retain caller-owned buffers (contract).
	value := []byte("alpha")
	if err := backend.Set(ctx, "iso", value); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value[0] = 'X'

	got, err := backend.Get(ctx, "iso")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "alpha" {
		t.Fatalf("backend retained caller buffer: got %q, want %q", got, "alpha")
	}
}

func TestPostgresInvalidKey(t *testing.T) {
	backend := openTestPostgres(t)
	ctx := context.Background()

	if err := backend.Set(ctx, "", []byte("x")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Set(\"\") = %v, want ErrInvalidKey", err)
	}
	if _, err := backend.Get(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Get(\"\") = %v, want ErrInvalidKey", err)
	}
}
