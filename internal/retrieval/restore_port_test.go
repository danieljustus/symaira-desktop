package retrieval

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

type restorePortFixture struct {
	SchemaVersion   int               `json:"schema_version"`
	SourceHashes    map[string]string `json:"source_hashes"`
	RestoredRows    []backupPortRow   `json:"restored_rows"`
	Header          string            `json:"header"`
	Mode            string            `json:"mode"`
	DirectoryMode   string            `json:"directory_mode"`
	SourceUnchanged bool              `json:"source_unchanged"`
	SamePathError   string            `json:"same_path_error"`
	InvalidPrefix   string            `json:"invalid_prefix"`
	DirectoryPrefix string            `json:"directory_prefix"`
	MissingPrefix   string            `json:"missing_prefix"`
}

func TestIndexRestorePortFixture(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	fixture := restorePortFixture{
		SchemaVersion: 1, SourceHashes: map[string]string{},
		Mode: "0600", DirectoryMode: "0700",
	}
	for _, rel := range []string{"internal/retrieval/maintenance.go", "internal/retrieval/internal/config/config.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(hash[:])
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	vault := t.TempDir()
	destination, err := IndexPathForVault(vault)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	for _, setup := range []struct{ path, body string }{{backup, "restored row"}, {destination, "stale row"}} {
		if err := os.MkdirAll(filepath.Dir(setup.path), 0o700); err != nil {
			t.Fatal(err)
		}
		db, err := sqlitekit.Open(setup.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE restore_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO restore_rows VALUES (1, ?)", setup.body); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestoreIndexForVault(vault, backup); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	fixture.SourceUnchanged = bytes.Equal(before, after)
	if !fixture.SourceUnchanged {
		t.Fatal("restore changed the source backup")
	}
	db, err := sqlitekit.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	fixture.RestoredRows, err = readRestorePortRows(db)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Header = string(content[:16])
	if fixture.Header != "SQLite format 3\x00" {
		t.Fatalf("restored header = %q", fixture.Header)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
		if fixture.Mode != "0600" {
			t.Fatalf("restored file mode = %s", fixture.Mode)
		}
		info, err = os.Stat(filepath.Dir(destination))
		if err != nil {
			t.Fatal(err)
		}
		fixture.DirectoryMode = fmt.Sprintf("%04o", info.Mode().Perm())
	}
	if err := RestoreIndexForVault(vault, destination); err == nil {
		t.Fatal("same-path restore succeeded")
	} else {
		fixture.SamePathError = err.Error()
	}
	invalid := filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(invalid, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreIndexForVault(vault, invalid); err == nil {
		t.Fatal("invalid backup restored")
	} else {
		fixture.InvalidPrefix = strings.SplitN(err.Error(), ":", 2)[0]
	}
	if err := RestoreIndexForVault(vault, t.TempDir()); err == nil {
		t.Fatal("directory backup restored")
	} else {
		fixture.DirectoryPrefix = strings.SplitN(err.Error(), ":", 2)[0]
	}
	if err := RestoreIndexForVault(vault, filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing backup restored")
	} else {
		fixture.MissingPrefix = strings.SplitN(err.Error(), ":", 2)[0]
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/retrieval/index-restore.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatal("Go index restore fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}

func readRestorePortRows(db *sql.DB) ([]backupPortRow, error) {
	rows, err := db.Query("SELECT id, body FROM restore_rows ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []backupPortRow
	for rows.Next() {
		var row backupPortRow
		if err := rows.Scan(&row.ID, &row.Body); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
