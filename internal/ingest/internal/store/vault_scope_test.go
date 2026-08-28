package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

func TestCreateOrGet_ScopesHashToVault(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	ctx := context.Background()
	vaultA := filepath.Join(t.TempDir(), "vault-a")
	vaultB := filepath.Join(t.TempDir(), "vault-b")

	docA, created, err := s.CreateOrGet(ctx, "/tmp/a.txt", "same-hash", "text/plain", vaultA)
	if err != nil {
		t.Fatalf("create in vault A: %v", err)
	}
	if !created {
		t.Fatal("expected the first document to be created")
	}

	docB, created, err := s.CreateOrGet(ctx, "/tmp/b.txt", "same-hash", "text/plain", vaultB)
	if err != nil {
		t.Fatalf("same hash in vault B: %v", err)
	}
	if !created || docB.ID == docA.ID {
		t.Fatalf("expected a distinct document in vault B: docA=%+v docB=%+v created=%v", docA, docB, created)
	}

	sameVault, created, err := s.CreateOrGet(ctx, "/tmp/c.txt", "same-hash", "text/plain", vaultB)
	if err != nil {
		t.Fatalf("duplicate in vault B: %v", err)
	}
	if created || sameVault.ID != docB.ID {
		t.Fatalf("expected vault B duplicate to return document %d, got %+v created=%v", docB.ID, sameVault, created)
	}

	if err := s.SetVaultAndArchivePath(ctx, docA.ID, filepath.Join(vaultA, "a.md"), filepath.Join(vaultA, "archive", "same-hash.txt"), "", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.ByHashInVault(ctx, "same-hash", vaultA)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.VaultRoot != vaultA {
		t.Fatalf("stored vault root = %q, want %q", duplicate.VaultRoot, vaultA)
	}
	if _, err := s.ByHashInVault(ctx, "same-hash", filepath.Join(t.TempDir(), "other")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected no document in unrelated vault, got %v", err)
	}

	// A migrated legacy row has no vault_root, but its existing note path
	// still identifies the vault and must continue to deduplicate there.
	legacy, created, err := s.CreateOrGet(ctx, "/tmp/legacy.txt", "legacy-hash", "text/plain")
	if err != nil || !created {
		t.Fatalf("create legacy row: doc=%+v created=%v err=%v", legacy, created, err)
	}
	legacyNote := filepath.Join(vaultA, "legacy.md")
	if err := s.SetVaultAndArchivePath(ctx, legacy.ID, legacyNote, "", "", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	foundLegacy, created, err := s.CreateOrGet(ctx, "/tmp/legacy-copy.txt", "legacy-hash", "text/plain", vaultA)
	if err != nil {
		t.Fatalf("look up legacy row in its vault: %v", err)
	}
	if created || foundLegacy.ID != legacy.ID {
		t.Fatalf("expected legacy same-vault duplicate, got %+v created=%v", foundLegacy, created)
	}
}

func TestVaultScopeMigrationPreservesRelatedRows(t *testing.T) {
	legacyMigrations := fstest.MapFS{}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "014_document_vault_scope.sql") {
			continue
		}
		data, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatal(err)
		}
		legacyMigrations[name] = &fstest.MapFile{Data: data}
	}

	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sqlitekit.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := sqlitekit.Migrate(db, legacyMigrations); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	result, err := db.Exec(`INSERT INTO documents (source_path, sha256, mime) VALUES ('source.txt', 'legacy-hash', 'text/plain')`)
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs (document_id, kind) VALUES (?, 'ingest')`, documentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO document_extractions (document_id, profile, field_type, value) VALUES (?, 'default', 'title', 'Legacy title')`, documentID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("migrate legacy store: %v", err)
	}
	defer func() { _ = s.Close() }()
	foreignKeyRows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if foreignKeyRows.Next() {
		var table, parent string
		var rowID, foreignKeyID int64
		if err := foreignKeyRows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation after migration: table=%s row=%d parent=%s fk=%d", table, rowID, parent, foreignKeyID)
	}
	if err := foreignKeyRows.Close(); err != nil {
		t.Fatal(err)
	}

	for table := range map[string]struct{}{"jobs": {}, "document_extractions": {}} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE document_id = ?", documentID).Scan(&count); err != nil {
			t.Fatalf("query %s after migration: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s rows after migration = %d, want 1", table, count)
		}
	}
}
