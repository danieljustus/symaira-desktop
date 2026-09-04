package retrieval

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	retrievaldb "github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestBackupIndexForVaultIncludesCommittedWAL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	vaultRoot := t.TempDir()
	indexPath, err := IndexPathForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	live, err := retrievaldb.OpenAt(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()
	if err := live.SaveDocument(&retrievaldb.Document{Path: "wal-only.md", Hash: "hash", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(indexPath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty WAL before snapshot: info=%v err=%v", info, err)
	}

	backup := filepath.Join(t.TempDir(), "vault-backup.db")
	if err := BackupIndexForVault(vaultRoot, backup); err != nil {
		t.Fatalf("BackupIndexForVault: %v", err)
	}
	copyDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copyDB.Close() }()
	var count int
	if err := copyDB.QueryRow("SELECT COUNT(*) FROM documents WHERE path = 'wal-only.md'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backup document count = %d, want 1", count)
	}
}

func TestCopyIndexFileIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	destination := filepath.Join(dir, "nested", "backup.db")
	if err := os.WriteFile(source, []byte("derived-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyIndexFile(source, destination); err != nil {
		t.Fatalf("copyIndexFile: %v", err)
	}
	got, err := os.ReadFile(destination) //nolint:gosec // G304: destination is a t.TempDir() path built by this test
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "derived-index" {
		t.Fatalf("backup = %q", got)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRestoreIndexRejectsNonSQLiteBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLiteHeader(path); err == nil {
		t.Fatal("validateSQLiteHeader accepted a non-SQLite file")
	}
}

func TestCopyIndexFileRejectsSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(path, []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyIndexFile(path, path); err == nil {
		t.Fatal("copyIndexFile accepted identical paths")
	}
}
