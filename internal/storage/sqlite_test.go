package storage

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSQLiteBackendImplementsStorageBackend(t *testing.T) {
	var _ StorageBackend = (*SQLiteBackend)(nil)
}

func TestSQLiteBackendKeyValueLifecycle(t *testing.T) {
	backend := openTestSQLiteBackend(t)
	ctx := context.Background()

	if _, err := backend.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if err := backend.Remove(ctx, "missing"); err != nil {
		t.Fatalf("Remove(missing) error = %v, want nil for an idempotent remove", err)
	}

	if err := backend.Set(ctx, "profile/name", []byte("Daniel")); err != nil {
		t.Fatalf("Set(profile/name): %v", err)
	}
	got, err := backend.Get(ctx, "profile/name")
	if err != nil {
		t.Fatalf("Get(profile/name): %v", err)
	}
	if string(got) != "Daniel" {
		t.Fatalf("Get(profile/name) = %q, want %q", got, "Daniel")
	}

	if err := backend.Set(ctx, "profile/name", []byte("Hermes")); err != nil {
		t.Fatalf("Set(profile/name) overwrite: %v", err)
	}
	got, err = backend.Get(ctx, "profile/name")
	if err != nil {
		t.Fatalf("Get(profile/name) after overwrite: %v", err)
	}
	if string(got) != "Hermes" {
		t.Fatalf("Get(profile/name) after overwrite = %q, want %q", got, "Hermes")
	}

	if err := backend.Set(ctx, "profile/theme", []byte("dark")); err != nil {
		t.Fatalf("Set(profile/theme): %v", err)
	}
	if err := backend.Set(ctx, "session/token", []byte("secret")); err != nil {
		t.Fatalf("Set(session/token): %v", err)
	}

	entries, err := backend.Query(ctx, "profile/")
	if err != nil {
		t.Fatalf("Query(profile/): %v", err)
	}
	want := []Entry{
		{Key: "profile/name", Value: []byte("Hermes")},
		{Key: "profile/theme", Value: []byte("dark")},
	}
	if len(entries) != len(want) {
		t.Fatalf("Query(profile/) returned %d entries, want %d: %#v", len(entries), len(want), entries)
	}
	for i := range want {
		if entries[i].Key != want[i].Key || !bytes.Equal(entries[i].Value, want[i].Value) {
			t.Errorf("Query(profile/)[%d] = %#v, want %#v", i, entries[i], want[i])
		}
	}

	entries, err = backend.Query(ctx, "does-not-exist/")
	if err != nil {
		t.Fatalf("Query(does-not-exist/): %v", err)
	}
	if entries == nil {
		t.Fatal("Query(does-not-exist/) returned nil slice, want an empty slice")
	}
	if len(entries) != 0 {
		t.Fatalf("Query(does-not-exist/) returned %d entries, want 0", len(entries))
	}

	if err := backend.Remove(ctx, "profile/name"); err != nil {
		t.Fatalf("Remove(profile/name): %v", err)
	}
	if _, err := backend.Get(ctx, "profile/name"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(profile/name) after Remove error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteBackendStoresEmptyValues(t *testing.T) {
	backend := openTestSQLiteBackend(t)
	ctx := context.Background()

	if err := backend.Set(ctx, "empty", nil); err != nil {
		t.Fatalf("Set(empty, nil): %v", err)
	}
	value, err := backend.Get(ctx, "empty")
	if err != nil {
		t.Fatalf("Get(empty): %v", err)
	}
	if value == nil || len(value) != 0 {
		t.Fatalf("Get(empty) = %#v, want a non-nil empty value", value)
	}
}

func TestSQLiteBackendCopiesByteValues(t *testing.T) {
	backend := openTestSQLiteBackend(t)
	ctx := context.Background()
	input := []byte{0, 1, 2, 3, 255}

	if err := backend.Set(ctx, "copy/key", input); err != nil {
		t.Fatalf("Set(copy/key): %v", err)
	}
	input[0] = 99

	got, err := backend.Get(ctx, "copy/key")
	if err != nil {
		t.Fatalf("Get(copy/key): %v", err)
	}
	want := []byte{0, 1, 2, 3, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get(copy/key) = %v, want %v", got, want)
	}

	got[1] = 98
	gotAgain, err := backend.Get(ctx, "copy/key")
	if err != nil {
		t.Fatalf("Get(copy/key) after mutating result: %v", err)
	}
	if !bytes.Equal(gotAgain, want) {
		t.Fatalf("Get(copy/key) changed after mutating returned bytes: %v, want %v", gotAgain, want)
	}

	entries, err := backend.Query(ctx, "copy/")
	if err != nil {
		t.Fatalf("Query(copy/): %v", err)
	}
	if len(entries) != 1 || !bytes.Equal(entries[0].Value, want) {
		t.Fatalf("Query(copy/) = %#v, want one entry with %v", entries, want)
	}
	entries[0].Value[2] = 97
	gotAgain, err = backend.Get(ctx, "copy/key")
	if err != nil {
		t.Fatalf("Get(copy/key) after mutating Query result: %v", err)
	}
	if !bytes.Equal(gotAgain, want) {
		t.Fatalf("Get(copy/key) changed after mutating Query result: %v, want %v", gotAgain, want)
	}
}

func TestSQLiteBackendQueryUsesLiteralCaseSensitivePrefixes(t *testing.T) {
	backend := openTestSQLiteBackend(t)
	ctx := context.Background()
	values := map[string][]byte{
		"profile/name":        []byte("lower"),
		"Profile/name":        []byte("upper"),
		"profile%/literal":    []byte("percent"),
		"profile_%/wildcards": []byte("underscore"),
	}
	for key, value := range values {
		if err := backend.Set(ctx, key, value); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}

	entries, err := backend.Query(ctx, "profile%/")
	if err != nil {
		t.Fatalf("Query(profile%%/): %v", err)
	}
	if len(entries) != 1 || entries[0].Key != "profile%/literal" {
		t.Fatalf("Query(profile%%/) = %#v, want only literal percent key", entries)
	}

	entries, err = backend.Query(ctx, "profile/")
	if err != nil {
		t.Fatalf("Query(profile/): %v", err)
	}
	if len(entries) != 1 || entries[0].Key != "profile/name" {
		t.Fatalf("Query(profile/) = %#v, want only lowercase profile key", entries)
	}
}

func TestSQLiteBackendInitializesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "storage.db")
	first, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(first): %v", err)
	}
	if err := first.Set(context.Background(), "persisted", []byte("value")); err != nil {
		t.Fatalf("Set(persisted): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	second, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(second): %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close(second): %v", err)
		}
	})
	got, err := second.Get(context.Background(), "persisted")
	if err != nil {
		t.Fatalf("Get(persisted) after reopen: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("Get(persisted) after reopen = %q, want %q", got, "value")
	}
}

func TestSQLiteBackendCloseIsIdempotent(t *testing.T) {
	backend := openTestSQLiteBackend(t)
	if err := backend.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSQLiteBackendHonorsCanceledContext(t *testing.T) {
	backend := openTestSQLiteBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := backend.Get(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get canceled context error = %v, want context.Canceled", err)
	}
	if err := backend.Set(ctx, "key", []byte("value")); !errors.Is(err, context.Canceled) {
		t.Errorf("Set canceled context error = %v, want context.Canceled", err)
	}
	if err := backend.Remove(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Errorf("Remove canceled context error = %v, want context.Canceled", err)
	}
	if _, err := backend.Query(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("Query canceled context error = %v, want context.Canceled", err)
	}
}

func openTestSQLiteBackend(t *testing.T) *SQLiteBackend {
	t.Helper()

	backend, err := OpenSQLite(filepath.Join(t.TempDir(), "storage.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return backend
}
