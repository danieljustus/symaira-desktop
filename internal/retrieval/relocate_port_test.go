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
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/config"
)

type relocatePortRow struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type relocatePortFixture struct {
	SchemaVersion           int               `json:"schema_version"`
	InputRows               []relocatePortRow `json:"input_rows"`
	SourceRowsAfter         []relocatePortRow `json:"source_rows_after"`
	RelocatedRows           []relocatePortRow `json:"relocated_rows"`
	WALWasNonempty          bool              `json:"wal_was_nonempty"`
	PersistedIndexPath      string            `json:"persisted_index_path"`
	Header                  string            `json:"header"`
	Mode                    string            `json:"mode,omitempty"`
	DestinationReplaced     bool              `json:"destination_replaced"`
	SamePathError           string            `json:"same_path_error"`
	RenameConflictError     string            `json:"rename_conflict_error"`
	ConflictMarkerUnchanged bool              `json:"conflict_marker_unchanged"`
	BlockedParentError      string            `json:"blocked_parent_error"`
	BlockedMarkerUnchanged  bool              `json:"blocked_marker_unchanged"`
}

func TestIndexRelocatePortFixture(t *testing.T) {
	input := []relocatePortRow{{ID: 1, Body: "committed WAL row"}, {ID: 2, Body: "Müller / 東京"}}
	fixture := relocatePortFixture{SchemaVersion: 1, InputRows: input, Mode: "0600"}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	source, err := IndexLocation()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlitekit.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE relocate_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
	for _, row := range input {
		if _, err := db.Exec("INSERT INTO relocate_rows (id, body) VALUES (?, ?)", row.ID, row.Body); err != nil {
			t.Fatal(err)
		}
	}
	wal, err := os.Stat(source + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("expected committed rows in nonempty WAL: info=%v err=%v", wal, err)
	}
	fixture.WALWasNonempty = true

	destination := filepath.Join(root, "relocated", "retrieval.db")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RelocateIndex(destination); err != nil {
		t.Fatalf("RelocateIndex: %v", err)
	}
	cfg, err := config.Reload()
	if err != nil {
		t.Fatal(err)
	}
	fixture.PersistedIndexPath = strings.Replace(destination, root, "$ROOT", 1)
	if cfg.IndexPath != destination {
		t.Fatalf("persisted index_path = %q, want %q", cfg.IndexPath, destination)
	}
	if location, err := IndexLocation(); err != nil || location != destination {
		t.Fatalf("IndexLocation() = %q, %v; want %q", location, err, destination)
	}
	fixture.SourceRowsAfter, err = readRelocatePortRows(db)
	if err != nil {
		t.Fatal(err)
	}
	relocated, err := sqlitekit.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	fixture.RelocatedRows, err = readRelocatePortRows(relocated)
	if closeErr := relocated.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Header = string(data[:16])
	fixture.DestinationReplaced = bytes.Equal(data[:16], []byte("SQLite format 3\x00"))
	if !fixture.DestinationReplaced || fixture.RelocatedRows == nil || fixture.SourceRowsAfter == nil {
		t.Fatal("relocation did not replace the target with a readable snapshot")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
		if fixture.Mode != "0600" {
			t.Fatalf("relocated database mode = %s, want 0600", fixture.Mode)
		}
	}
	if err := RelocateIndexForVault("", destination); err == nil {
		t.Fatal("relocation to source path succeeded")
	} else {
		fixture.SamePathError = err.Error()
	}
	conflict := filepath.Join(root, "occupied.db")
	if err := os.Mkdir(conflict, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := []byte("preserve the existing destination")
	if err := os.WriteFile(filepath.Join(conflict, "marker"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RelocateIndexForVault("", conflict); err == nil {
		t.Fatal("relocation replaced a directory destination")
	} else {
		fixture.RenameConflictError = strings.SplitN(err.Error(), ":", 2)[0]
	}
	afterConflict, err := os.ReadFile(filepath.Join(conflict, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.ConflictMarkerUnchanged = bytes.Equal(marker, afterConflict)
	if !fixture.ConflictMarkerUnchanged {
		t.Fatal("failed rename changed existing destination contents")
	}
	blockedParent := filepath.Join(root, "ordinary-file")
	marker = []byte("keep this marker")
	if err := os.WriteFile(blockedParent, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RelocateIndexForVault("", filepath.Join(blockedParent, "retrieval.db")); err == nil {
		t.Fatal("relocation accepted a non-directory parent")
	} else {
		fixture.BlockedParentError = strings.SplitN(err.Error(), ":", 2)[0]
	}
	afterMarker, err := os.ReadFile(blockedParent)
	if err != nil {
		t.Fatal(err)
	}
	fixture.BlockedMarkerUnchanged = bytes.Equal(marker, afterMarker)
	if !fixture.BlockedMarkerUnchanged {
		t.Fatal("failed relocation changed the blocked parent marker")
	}
	if cfg, err := config.Reload(); err != nil || cfg.IndexPath != destination {
		t.Fatalf("failed relocation changed config: cfg=%+v err=%v", cfg, err)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source file location unavailable")
	}
	fixturePath := filepath.Join(filepath.Dir(sourceFile), "../../testdata/port/retrieval/index-relocate.json")
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("PORT_GENERATE") == "1" || os.Getenv("INDEX_RELOCATE_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("index relocation fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./internal/retrieval -run '^TestIndexRelocatePortFixture$'")
	}
}

func readRelocatePortRows(db *sql.DB) ([]relocatePortRow, error) {
	rows, err := db.Query("SELECT id, body FROM relocate_rows ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]relocatePortRow, 0)
	for rows.Next() {
		var row relocatePortRow
		if err := rows.Scan(&row.ID, &row.Body); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
