package selfhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/storage"
)

// ServerState is the server's persistent configuration and session-cache
// store, backed by the storage contract (#311): SQLite by default, PostgreSQL
// in production deployments (see ServerConfig.StateDSN). The rebuildable
// sidecar index (sidecar.DB) is deliberately NOT routed through this store —
// it remains SQLite-owned and performance-critical.
//
// All keys are namespaced with a "state:" prefix so a shared backend instance
// cannot collide with other consumers of the same storage database.
type ServerState struct {
	backend storage.StorageBackend
}

const stateKeyPrefix = "state:"

// State keys used by the server.
const (
	// StateKeyLegacyAdminMigrated records that the one-time legacy-token
	// admin-user migration ran. Without it the migration is guarded only by
	// "no users exist", which would re-create the well-known legacy admin
	// user after an administrator deliberately removed every user.
	StateKeyLegacyAdminMigrated = stateKeyPrefix + "legacy_admin_migrated"
)

// OpenServerState opens the server's persistent state store. With an empty
// dsn it uses the SQLite backend at <vaultRoot>/.symdesk/server/state.db;
// with a PostgreSQL connection string it uses the Postgres backend, which is
// the production path for multi-user self-hosting.
func OpenServerState(ctx context.Context, vaultRoot, dsn string) (*ServerState, error) {
	var backend storage.StorageBackend

	if dsn != "" {
		b, err := storage.OpenPostgres(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("server state (postgres): %w", err)
		}
		backend = b
	} else {
		path := filepath.Join(vaultRoot, ".symdesk", "server", "state.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("server state directory: %w", err)
		}
		b, err := storage.OpenSQLite(path)
		if err != nil {
			return nil, fmt.Errorf("server state (sqlite): %w", err)
		}
		backend = b
	}

	return &ServerState{backend: backend}, nil
}

// Close releases the underlying backend, when it supports closing.
func (s *ServerState) Close() error {
	if closer, ok := s.backend.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// Get returns the raw value stored under key, or an error wrapping
// storage.ErrNotFound when the key is absent.
func (s *ServerState) Get(ctx context.Context, key string) ([]byte, error) {
	return s.backend.Get(ctx, stateKeyPrefix+key)
}

// Set stores value under key, replacing any existing value.
func (s *ServerState) Set(ctx context.Context, key string, value []byte) error {
	return s.backend.Set(ctx, stateKeyPrefix+key, value)
}

// Remove deletes key. Removing an absent key succeeds.
func (s *ServerState) Remove(ctx context.Context, key string) error {
	return s.backend.Remove(ctx, stateKeyPrefix+key)
}

// GetJSON decodes the value stored under key into dest, or returns
// storage.ErrNotFound when the key is absent.
func (s *ServerState) GetJSON(ctx context.Context, key string, dest any) error {
	raw, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decode server state key %q: %w", key, err)
	}
	return nil
}

// SetJSON encodes value as JSON and stores it under key.
func (s *ServerState) SetJSON(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode server state key %q: %w", key, err)
	}
	return s.Set(ctx, key, raw)
}

// Has reports whether key exists. It returns false when the key is absent.
func (s *ServerState) Has(ctx context.Context, key string) (bool, error) {
	_, err := s.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetIfAbsent stores value under key only when the key does not exist yet,
// reporting whether it wrote. It is used for one-time migration markers so
// restarting the server cannot re-run them.
func (s *ServerState) SetIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	exists, err := s.Has(ctx, key)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if err := s.Set(ctx, key, value); err != nil {
		return false, err
	}
	return true, nil
}
