package retrieval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/config"
)

// IndexLocation returns the actual shared retrieval database path. The path is
// derived from the same location used by the absorbed retrieval store.
func IndexLocation() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return configuredIndexPath(cfg)
}

// BackupIndex copies the closed retrieval database to destination. It is a
// file-level backup of the derived index; Markdown remains the source of truth.
func BackupIndex(destination string) error {
	location, err := IndexLocation()
	if err != nil {
		return err
	}
	if err := ensureIndexExists(location); err != nil {
		return err
	}
	return copyIndexFile(location, destination)
}

// RestoreIndex atomically replaces the derived retrieval database with a
// validated regular-file backup. The backup is never modified. Callers should
// ensure no long-lived retrieval client is open while restoring.
func RestoreIndex(source string) error {
	location, err := IndexLocation()
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat index backup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("index backup is not a regular file: %s", source)
	}
	if err := validateSQLiteHeader(source); err != nil {
		return err
	}
	return copyIndexFile(source, location)
}

// RelocateIndex moves the derived retrieval database and persists its new
// location in the user configuration. The source database is copied before
// configuration is changed, so a failed copy leaves the current location in
// effect.
func RelocateIndex(destination string) error {
	location, err := IndexLocation()
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("absolute index relocation path: %w", err)
	}
	if err := ensureIndexExists(location); err != nil {
		return err
	}
	if err := copyIndexFile(location, destination); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.IndexPath = destination
	if err := config.Save(config.GlobalPath(), cfg); err != nil {
		return fmt.Errorf("save index location: %w", err)
	}
	return nil
}

func ensureIndexExists(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat retrieval index: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("retrieval index is not a regular file: %s", path)
	}
	return nil
}

func copyIndexFile(source, destination string) error {
	if filepath.Clean(source) == filepath.Clean(destination) {
		return fmt.Errorf("source and destination are the same index file")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create index directory: %w", err)
	}
	input, err := os.Open(source) // #nosec G304 -- explicit user-selected backup path.
	if err != nil {
		return fmt.Errorf("open index file: %w", err)
	}
	defer func() { _ = input.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".symseek-restore-*")
	if err != nil {
		return fmt.Errorf("create temporary index file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary index file: %w", err)
	}
	if _, err := io.Copy(tmp, input); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy index file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync index file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary index file: %w", err)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("replace retrieval index: %w", err)
	}
	return nil
}

func validateSQLiteHeader(path string) error {
	f, err := os.Open(path) // #nosec G304 -- explicit user-selected backup path.
	if err != nil {
		return fmt.Errorf("open index backup: %w", err)
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 16)
	if _, err := io.ReadFull(f, header); err != nil {
		return fmt.Errorf("read index backup header: %w", err)
	}
	if !bytes.Equal(header, []byte("SQLite format 3\x00")) {
		return fmt.Errorf("index backup is not a SQLite database: %s", path)
	}
	return nil
}
