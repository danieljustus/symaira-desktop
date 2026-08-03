// Package storage defines the persistent key/value storage contract used by
// future configurable storage backends. The package intentionally does not
// change the rebuildable SQLite sidecar; that index remains owned by
// internal/sidecar.
package storage

import (
	"context"
	"errors"
)

var (
	// ErrNotFound reports that a requested key does not exist.
	ErrNotFound = errors.New("storage: key not found")

	// ErrInvalidKey reports an empty key. Empty query prefixes are valid and
	// return all entries.
	ErrInvalidKey = errors.New("storage: key must not be empty")
)

// Entry is one key/value pair returned by StorageBackend.Query.
//
// Values are opaque bytes so callers can choose their own encoding (for
// example, JSON or TOML) without coupling a backend to a configuration format.
type Entry struct {
	Key   string
	Value []byte
}

// StorageBackend is the context-aware contract for persistent key/value state.
//
// Query treats prefix as a key prefix and returns matching entries in key order.
// Passing an empty prefix returns every entry. Set replaces an existing value,
// while Remove is idempotent and does not report an error for a missing key.
// Implementations must return ErrNotFound from Get when the key is absent.
// Implementations must not retain or mutate caller-owned value buffers.
type StorageBackend interface {
	// Get returns the value stored under key, or an error wrapping ErrNotFound
	// when key is absent.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores value under key, replacing any existing value.
	Set(ctx context.Context, key string, value []byte) error

	// Remove deletes key. Removing an absent key succeeds.
	Remove(ctx context.Context, key string) error

	// Query returns entries whose keys begin with prefix, sorted by key.
	Query(ctx context.Context, prefix string) ([]Entry, error)
}
