package retrieval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/config"
)

// IndexLocation returns the effective standalone retrieval database path.
func IndexLocation() (string, error) {
	return IndexLocationForVault("")
}

// IndexLocationForVault returns the effective retrieval database path for a
// vault without opening it. An explicit IndexPath remains authoritative.
func IndexLocationForVault(vaultRoot string) (string, error) {
	cfg, err := config.Reload()
	if err != nil {
		return "", err
	}
	if cfg != nil && strings.TrimSpace(cfg.IndexPath) != "" {
		return configuredIndexPath(cfg)
	}
	if strings.TrimSpace(vaultRoot) == "" {
		return configuredIndexPath(cfg)
	}
	return vaultIndexPath(vaultRoot)
}

// BackupIndex snapshots the standalone retrieval database to destination.
func BackupIndex(destination string) error {
	return BackupIndexForVault("", destination)
}

// BackupIndexForVault creates a consistent SQLite snapshot of the effective
// vault index, including committed rows still present in its WAL.
func BackupIndexForVault(vaultRoot, destination string) error {
	location, err := IndexLocationForVault(vaultRoot)
	if err != nil {
		return err
	}
	if err := ensureIndexExists(location); err != nil {
		return err
	}
	return snapshotIndexFile(location, destination)
}

// RestoreIndex atomically replaces the standalone retrieval database with a
// validated regular-file backup.
func RestoreIndex(source string) error {
	return RestoreIndexForVault("", source)
}

// RestoreIndexForVault restores the effective vault index from a snapshot.
// The backup is never modified. Callers must ensure no long-lived client is
// open while restoring.
func RestoreIndexForVault(vaultRoot, source string) error {
	location, err := IndexLocationForVault(vaultRoot)
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

// RelocateIndex moves the standalone retrieval database and persists its new
// location in user configuration.
func RelocateIndex(destination string) error {
	return RelocateIndexForVault("", destination)
}

// RelocateIndexForVault rejects vault-scoped relocation because IndexPath is a
// global override and would collapse isolation. Empty vaultRoot preserves the
// standalone relocation behavior.
func RelocateIndexForVault(vaultRoot, destination string) error {
	if vaultRoot != "" {
		return fmt.Errorf("cannot relocate a vault-scoped retrieval index; use backup/restore or run relocate without --vault for a deliberate global index_path override")
	}
	location, err := IndexLocationForVault("")
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
	if err := snapshotIndexFile(location, destination); err != nil {
		return err
	}
	cfg, err := config.Reload()
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

func snapshotIndexFile(source, destination string) error {
	if filepath.Clean(source) == filepath.Clean(destination) {
		return fmt.Errorf("source and destination are the same index file")
	}
	if err := ensureIndexExists(source); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create index directory: %w", err)
	}

	placeholder, err := os.CreateTemp(filepath.Dir(destination), ".symdesk-snapshot-*.db")
	if err != nil {
		return fmt.Errorf("create snapshot path: %w", err)
	}
	tmpName := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close snapshot placeholder: %w", err)
	}
	if err := os.Remove(tmpName); err != nil {
		return fmt.Errorf("prepare snapshot path: %w", err)
	}
	defer func() { _ = os.Remove(tmpName) }()

	conn, err := sqlitekit.Open(source)
	if err != nil {
		return fmt.Errorf("open retrieval index for snapshot: %w", err)
	}
	quoted := strings.ReplaceAll(tmpName, "'", "''")
	_, snapshotErr := conn.Exec("VACUUM INTO '" + quoted + "'") // #nosec G202 -- generated path is SQL-quoted above.
	closeErr := conn.Close()
	if snapshotErr != nil {
		return fmt.Errorf("snapshot retrieval index: %w", snapshotErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close retrieval snapshot source: %w", closeErr)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("protect retrieval snapshot: %w", err)
	}
	if err := validateSQLiteHeader(tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("replace retrieval snapshot: %w", err)
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
	sourceInfo, err := input.Stat()
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("stat source index file: %w", err)
	}
	copied, err := io.Copy(tmp, input)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy index file: %w", err)
	}
	if copied != sourceInfo.Size() {
		_ = tmp.Close()
		return fmt.Errorf("copy index file: copied %d bytes, want %d", copied, sourceInfo.Size())
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
