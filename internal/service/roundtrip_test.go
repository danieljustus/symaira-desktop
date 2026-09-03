package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestRoundtrip(t *testing.T) {
	vaultPath, err := filepath.Abs("testdata/vault")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(vaultPath)
	_ = os.MkdirAll(vaultPath, 0700)

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	_ = os.Remove(dbPath)

	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	svc := New(vaultPath, db)

	// 1. Note New
	path, err := svc.NoteNew("Roundtrip Test", "Testing content [[Target Note]]", "")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Search
	results, err := svc.Search("Testing")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Title != "Roundtrip Test" {
		t.Errorf("expected to find note in search: %v", results)
	}

	// 3. Backlinks
	links, err := svc.Backlinks("Target Note")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0] != path {
		t.Errorf("expected backlink from Target Note to Roundtrip Test, got %v", links)
	}

	// 4. Props
	props, err := svc.Props(path)
	if err != nil {
		t.Fatal(err)
	}
	if props["title"] != "Roundtrip Test" {
		t.Errorf("expected title property 'Roundtrip Test', got %v", props["title"])
	}

	// 5. Note Move
	err = svc.NoteMove(path, "Moved_Note.md")
	if err != nil {
		t.Fatal(err)
	}

	// Wait a bit just in case
	time.Sleep(100 * time.Millisecond)

	ls, err := svc.Ls("")
	if err != nil {
		t.Fatal(err)
	}
	foundMoved := false
	for _, l := range ls {
		if l.Path == "Moved_Note.md" {
			foundMoved = true
		}
	}
	if !foundMoved {
		t.Errorf("expected Moved_Note.md in ls, got %v", ls)
	}
}

// TestLsPopulatesEmptySidecarFromExistingVaultFiles covers Ls's lazy
// bootstrap path (service.go, "A per-vault sidecar may be new after an
// upgrade"): a fresh, empty sidecar over a vault that already has Markdown
// files on disk must still index them before Ls returns, now that this
// delegates to sidecar.DB.RefreshIndex's stat-based fast path (issue #180).
func TestLsPopulatesEmptySidecarFromExistingVaultFiles(t *testing.T) {
	vaultPath, err := filepath.Abs("testdata/vault_bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(vaultPath)
	_ = os.MkdirAll(vaultPath, 0700)
	t.Cleanup(func() { _ = os.RemoveAll(vaultPath) })

	if err := os.WriteFile(filepath.Join(vaultPath, "Existing.md"), []byte("---\ntitle: Existing\n---\nBody"), 0600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	svc := New(vaultPath, db)

	ls, err := svc.Ls("")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range ls {
		if l.Path == "Existing.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Ls to lazily index pre-existing vault files, got %v", ls)
	}

	// A second Ls call must keep returning the same result without error.
	ls2, err := svc.Ls("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ls2) != len(ls) {
		t.Fatalf("expected stable ls results, got %v then %v", ls, ls2)
	}
}

func TestEventsStress(t *testing.T) {
	// A simple test to simulate writing 1000 files
	vaultPath, err := filepath.Abs("testdata/vault_stress")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(vaultPath)
	_ = os.MkdirAll(vaultPath, 0700)

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	svc := New(vaultPath, db)

	for i := 0; i < 100; i++ { // Reduced to 100 for fast CI tests, but concept stands
		// We use NoteNew to write files rapidly
		_, err := svc.NoteNew(fmt.Sprintf("Stress Note %d", i), "Content", "")
		if err != nil {
			t.Fatal(err)
		}
	}

	ls, err := svc.Ls("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 100 {
		t.Errorf("expected 100 files in sidecar, got %d", len(ls))
	}
}
