package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// TestPortContactsSchemaContract executes the production migration runner twice.
// applied_at is replaced by a fixed sentinel after the first run: the second
// migration pass must preserve it, proving bookkeeping is not rewritten.
func TestPortContactsSchemaContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	type object struct {
		Type  string  `json:"type"`
		Name  string  `json:"name"`
		Table string  `json:"table"`
		SQL   *string `json:"sql"`
	}
	type migrationRow struct {
		Version   int    `json:"version"`
		Name      string `json:"name"`
		AppliedAt string `json:"applied_at"`
	}
	fixture := struct {
		Oracle       any               `json:"oracle"`
		SourceHashes map[string]string `json:"source_hashes"`
		Objects      []object          `json:"objects"`
		Migrations   []migrationRow    `json:"migrations"`
	}{Oracle: fixtureoracle.Current(), SourceHashes: map[string]string{}}
	paths := []string{"internal/contacts/internal/storage/sqlite/migrate.go", "internal/contacts/internal/storage/sqlite/db.go"}
	entries, err := filepath.Glob(filepath.Join(root, "internal/contacts/internal/storage/sqlite/migrations/*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	for _, path := range paths {
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		fixture.SourceHashes[path] = hex.EncodeToString(h[:])
	}
	ctx := context.Background()
	db, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.Exec("UPDATE schema_migrations SET applied_at = '2000-01-02T03:04:05.000Z'"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.Type, &o.Name, &o.Table, &o.SQL); err != nil {
			t.Fatal(err)
		}
		fixture.Objects = append(fixture.Objects, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query("SELECT version, name, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var m migrationRow
		if err := rows.Scan(&m.Version, &m.Name, &m.AppliedAt); err != nil {
			t.Fatal(err)
		}
		fixture.Migrations = append(fixture.Migrations, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/schema.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts schema fixture drift; regenerate with PORT_GENERATE=1")
		}
	}
}
