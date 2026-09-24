package retrieval

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

type backupPortRow struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type backupPortFixture struct {
	SchemaVersion   int             `json:"schema_version"`
	InputRows       []backupPortRow `json:"input_rows"`
	ObservedRows    []backupPortRow `json:"observed_rows"`
	WALWasNonempty  bool            `json:"wal_was_nonempty"`
	Header          string          `json:"header"`
	Mode            string          `json:"mode,omitempty"`
	DirectoryMode   string          `json:"directory_mode,omitempty"`
	SamePathError   string          `json:"same_path_error"`
	BlockedParent   string          `json:"blocked_parent_error"`
	ReplacementOkay bool            `json:"replacement_okay"`
}

func TestIndexBackupPortFixture(t *testing.T) {
	input := []backupPortRow{{ID: 1, Body: "WAL-only row"}, {ID: 2, Body: "Müller / 東京"}}
	// POSIX mode expectations are observed below on Unix. Windows has no POSIX
	// mode contract, so retain these values only to keep the shared fixture stable.
	fixture := backupPortFixture{SchemaVersion: 1, InputRows: input, Mode: "0600", DirectoryMode: "0700"}
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := sqlitekit.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE backup_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
	for _, row := range input {
		if _, err := db.Exec("INSERT INTO backup_rows (id, body) VALUES (?, ?)", row.ID, row.Body); err != nil {
			t.Fatal(err)
		}
	}
	walInfo, err := os.Stat(source + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("expected committed rows in a nonempty WAL: info=%v err=%v", walInfo, err)
	}
	fixture.WALWasNonempty = true

	destination := filepath.Join(root, "nested", "backup.db")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshotIndexFile(source, destination); err != nil {
		t.Fatalf("snapshotIndexFile: %v", err)
	}
	fixture.ReplacementOkay = true
	createdDestination := filepath.Join(root, "created", "deeper", "backup.db")
	if err := snapshotIndexFile(source, createdDestination); err != nil {
		t.Fatalf("snapshotIndexFile with new destination directories: %v", err)
	}
	backup, err := sqlitekit.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ObservedRows, err = readBackupPortRows(backup)
	if closeErr := backup.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	header, err := os.ReadFile(destination) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if len(header) < 16 {
		t.Fatalf("snapshot has only %d bytes", len(header))
	}
	fixture.Header = string(header[:16])
	if fixture.Header != "SQLite format 3\x00" {
		t.Fatalf("snapshot header = %q", fixture.Header)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
		if fixture.Mode != "0600" {
			t.Fatalf("snapshot mode = %s, want 0600", fixture.Mode)
		}
		directoryInfo, err := os.Stat(filepath.Dir(createdDestination))
		if err != nil {
			t.Fatal(err)
		}
		fixture.DirectoryMode = fmt.Sprintf("%04o", directoryInfo.Mode().Perm())
		if fixture.DirectoryMode != "0700" {
			t.Fatalf("created snapshot directory mode = %s, want 0700", fixture.DirectoryMode)
		}
	}
	if err := snapshotIndexFile(source, source+string(os.PathSeparator)+"."); err == nil {
		t.Fatal("snapshotIndexFile accepted paths that clean to the same file")
	} else {
		fixture.SamePathError = err.Error()
	}
	blockedParent := filepath.Join(root, "ordinary-file")
	if err := os.WriteFile(blockedParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshotIndexFile(source, filepath.Join(blockedParent, "backup.db")); err == nil {
		t.Fatal("snapshotIndexFile accepted a non-directory destination parent")
	} else {
		fixture.BlockedParent = strings.SplitN(err.Error(), ":", 2)[0]
	}

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	fixturePath := filepath.Join("..", "..", "testdata", "port", "retrieval", "index-backup.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(fixturePath) //nolint:gosec // fixturePath is fixed and repo-relative.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("index backup fixture is stale; run PORT_GENERATE=1 go test ./internal/retrieval -run '^TestIndexBackupPortFixture$'")
	}
}

func readBackupPortRows(db *sql.DB) ([]backupPortRow, error) {
	rows, err := db.Query("SELECT id, body FROM backup_rows ORDER BY id")
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
