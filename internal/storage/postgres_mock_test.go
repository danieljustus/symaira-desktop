package storage

// Mock-based unit tests for the PostgreSQL backend. These run in the default
// `make test` run without any live database; the opt-in integration tests in
// postgres_test.go (build tag "postgres") remain the live-server coverage.
//
// pgxmock treats expectation SQL strings as regular expressions, so the
// "$1" placeholders in the backend's statements are escaped as \$1 below.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

// pgxmock's PgxPoolIface satisfies the same pgxPool seam as *pgxpool.Pool.
var _ pgxPool = pgxmock.PgxPoolIface(nil)

func newMockPostgresBackend(t *testing.T) (*PostgresBackend, pgxmock.PgxPoolIface) {
	t.Helper()
	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	return &PostgresBackend{pool: mockPool}, mockPool
}

func TestPostgresOpenEmptyDSN(t *testing.T) {
	_, err := OpenPostgres(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("OpenPostgres(\"\") error = %v, want error containing %q", err, "must not be empty")
	}
}

func TestPostgresOpenInvalidDSN(t *testing.T) {
	_, err := OpenPostgres(context.Background(), "://")
	if err == nil {
		t.Fatal("OpenPostgres(\"://\") = nil error, want parse failure")
	}
}

func TestPostgresOpenUnreachable(t *testing.T) {
	// Port 1 refuses connections on any host; connect_timeout keeps the test
	// fast and deterministic even if the port were somehow filtered.
	_, err := OpenPostgres(context.Background(), "postgres://127.0.0.1:1/nope?connect_timeout=1")
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("OpenPostgres(unreachable) error = %v, want error mentioning connect", err)
	}
}

func TestPostgresGetNotFound(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	mockPool.ExpectQuery("SELECT value FROM storage_entries WHERE storage_key = \\$1").
		WithArgs("missing").
		WillReturnRows(pgxmock.NewRows([]string{"value"}))

	_, err := backend.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresGetValue(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	ctx := context.Background()
	mockPool.ExpectQuery("SELECT value FROM storage_entries WHERE storage_key = \\$1").
		WithArgs("key").
		WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow([]byte("alpha")))

	got, err := backend.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "alpha" {
		t.Fatalf("Get = %q, want %q", got, "alpha")
	}

	// The returned slice must not alias the backend's data (cloneBytes).
	got[0] = 'X'
	mockPool.ExpectQuery("SELECT value FROM storage_entries WHERE storage_key = \\$1").
		WithArgs("key").
		WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow([]byte("alpha")))
	again, err := backend.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get after caller mutation: %v", err)
	}
	if string(again) != "alpha" {
		t.Fatalf("Get after caller mutation = %q, want %q", again, "alpha")
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresSet(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	ctx := context.Background()
	mockPool.ExpectExec("INSERT INTO storage_entries").
		WithArgs("key", []byte("value")).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := backend.Set(ctx, "key", []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A nil value is stored as a non-nil empty value.
	mockPool.ExpectExec("INSERT INTO storage_entries").
		WithArgs("nilkey", []byte{}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := backend.Set(ctx, "nilkey", nil); err != nil {
		t.Fatalf("Set(nil): %v", err)
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresSetError(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	mockPool.ExpectExec("INSERT INTO storage_entries").
		WithArgs("key", []byte("value")).
		WillReturnError(errors.New("boom"))

	err := backend.Set(context.Background(), "key", []byte("value"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Set error = %v, want error containing %q", err, "boom")
	}
}

func TestPostgresRemove(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	ctx := context.Background()
	mockPool.ExpectExec("DELETE FROM storage_entries").
		WithArgs("key").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	if err := backend.Remove(ctx, "key"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Removing an absent key succeeds: the backend only fails on Exec errors.
	mockPool.ExpectExec("DELETE FROM storage_entries").
		WithArgs("absent").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	if err := backend.Remove(ctx, "absent"); err != nil {
		t.Fatalf("Remove(absent): %v", err)
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresQuery(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	ctx := context.Background()
	expectQuery := func() {
		mockPool.ExpectQuery("SELECT storage_key, value").
			WithArgs("config:").
			WillReturnRows(pgxmock.NewRows([]string{"storage_key", "value"}).
				AddRow("config:host", []byte("h")).
				AddRow("config:port", []byte("p")))
	}

	expectQuery()
	entries, err := backend.Query(ctx, "config:")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Query(config:) = %d entries, want 2", len(entries))
	}
	if entries[0].Key != "config:host" || string(entries[0].Value) != "h" {
		t.Fatalf("entries[0] = %+v, want {config:host h}", entries[0])
	}
	if entries[1].Key != "config:port" || string(entries[1].Value) != "p" {
		t.Fatalf("entries[1] = %+v, want {config:port p}", entries[1])
	}

	// Values are isolated from caller mutation (cloneBytes per entry).
	expectQuery()
	entries[0].Value[0] = 'X'
	again, err := backend.Query(ctx, "config:")
	if err != nil {
		t.Fatalf("Query after caller mutation: %v", err)
	}
	if string(again[0].Value) != "h" {
		t.Fatalf("Query after caller mutation = %q, want %q", again[0].Value, "h")
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresQueryError(t *testing.T) {
	backend, mockPool := newMockPostgresBackend(t)
	mockPool.ExpectQuery("SELECT storage_key, value").
		WithArgs("config:").
		WillReturnError(errors.New("db down"))

	_, err := backend.Query(context.Background(), "config:")
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("Query error = %v, want error containing %q", err, "db down")
	}
}

func TestPostgresQueryContextCancelled(t *testing.T) {
	backend, _ := newMockPostgresBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// No DB expectation is set: the cancellation must short-circuit before
	// any SQL reaches the pool.
	if _, err := backend.Query(ctx, "x"); err == nil {
		t.Fatal("Query with cancelled context = nil error, want context error")
	}
}

func TestPostgresClose(t *testing.T) {
	backend, _ := newMockPostgresBackend(t)
	backend.Close()
	backend.Close() // safe to call repeatedly
}

func TestPostgresMockInvalidKey(t *testing.T) {
	backend, _ := newMockPostgresBackend(t)
	ctx := context.Background()
	if err := backend.Set(ctx, "", []byte("x")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Set(\"\") error = %v, want ErrInvalidKey", err)
	}
	if _, err := backend.Get(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Get(\"\") error = %v, want ErrInvalidKey", err)
	}
	if err := backend.Remove(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Remove(\"\") error = %v, want ErrInvalidKey", err)
	}
}
