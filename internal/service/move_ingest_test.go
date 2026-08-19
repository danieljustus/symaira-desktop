package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// withDisabledTool makes compose.ResolveFunc report name as unavailable,
// matching what compose.Resolve returns when a tool genuinely isn't
// installed, so call sites that gracefully degrade behave the same as they
// would in production. It restores the original ResolveFunc via t.Cleanup.
func withDisabledTool(t *testing.T, name string) {
	t.Helper()
	prev := compose.ResolveFunc
	compose.ResolveFunc = func(n string) (string, error) {
		if n == name {
			return "", fmt.Errorf("%s not found (disabled in test)", n)
		}
		return prev(n)
	}
	compose.ResetCache()
	t.Cleanup(func() {
		compose.ResolveFunc = prev
		compose.ResetCache()
	})
}

// withMockTool redirects compose.ResolveFunc so name resolves to path (a
// test-double binary) while every other tool name continues to resolve
// normally. It restores the original ResolveFunc via t.Cleanup.
func withMockTool(t *testing.T, name, path string) {
	t.Helper()
	prev := compose.ResolveFunc
	compose.ResolveFunc = func(n string) (string, error) {
		if n == name {
			return path, nil
		}
		return prev(n)
	}
	compose.ResetCache()
	t.Cleanup(func() {
		compose.ResolveFunc = prev
		compose.ResetCache()
	})
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	vaultPath := t.TempDir()
	db, err := sidecar.Open(filepath.Join(vaultPath, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Prevent accidental calls to the real symseek binary on PATH during tests.
	withDisabledTool(t, "symseek")

	return New(vaultPath, db)
}

func TestNoteMoveRemovesStaleIndexEntry(t *testing.T) {
	svc := newTestService(t)

	if _, err := svc.NoteNew("Move Me", "unique-move-content", ""); err != nil {
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
	if results[0].Path != "Moved.md" {
		t.Errorf("expected hit at Moved.md, got %v", results[0].Path)
	}

	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "Moved.md")); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
}

func TestNoteMoveDeniesTraversal(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.NoteNew("Stay", "content", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.NoteMove("Stay.md", "../escape.md"); err == nil {
		t.Error("expected traversal to be denied")
	}
}

func TestIngestIndexesInboxNote(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
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
		if f.Path == res["path"] {
			found = true
		}
	}
	if !found {
		t.Errorf("ingested note %s not in index: %v", res["path"], files)
	}
}
