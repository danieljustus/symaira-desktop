package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	vaultPath := t.TempDir()
	db, err := sidecar.Open(filepath.Join(vaultPath, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(vaultPath, db)
}

func TestNoteMoveRemovesStaleIndexEntry(t *testing.T) {
	svc := newTestService(t)

	if _, err := svc.NoteNew("Move Me", "unique-move-content"); err != nil {
		t.Fatal(err)
	}

	if err := svc.NoteMove("Move_Me.md", "Moved.md"); err != nil {
		t.Fatal(err)
	}

	results, err := svc.Search("unique-move-content")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 search hit after move, got %d (stale index entry?)", len(results))
	}
	if results[0]["path"] != "Moved.md" {
		t.Errorf("expected hit at Moved.md, got %v", results[0]["path"])
	}

	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "Moved.md")); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
}

func TestNoteMoveDeniesTraversal(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.NoteNew("Stay", "content"); err != nil {
		t.Fatal(err)
	}
	if err := svc.NoteMove("Stay.md", "../escape.md"); err == nil {
		t.Error("expected traversal to be denied")
	}
}

func TestIngestIndexesInboxNote(t *testing.T) {
	svc := newTestService(t)

	src := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Ingest(src)
	if err != nil {
		t.Fatal(err)
	}
	if res["path"] == "" {
		t.Fatal("expected ingest to return a note path")
	}

	files, err := svc.Ls("")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range files {
		if f["path"] == res["path"] {
			found = true
		}
	}
	if !found {
		t.Errorf("ingested note %s not in index: %v", res["path"], files)
	}
}
