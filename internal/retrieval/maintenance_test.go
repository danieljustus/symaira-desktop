package retrieval

import (
	"os"
	"path/filepath"
	"testing"
)

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
	got, err := os.ReadFile(destination)
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
