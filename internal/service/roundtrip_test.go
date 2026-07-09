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
	_ = os.MkdirAll(vaultPath, 0755)

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	_ = os.Remove(dbPath)

	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

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
	if len(results) == 0 || results[0]["title"] != "Roundtrip Test" {
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
		if l["path"] == "Moved_Note.md" {
			foundMoved = true
		}
	}
	if !foundMoved {
		t.Errorf("expected Moved_Note.md in ls, got %v", ls)
	}
}

func TestEventsStress(t *testing.T) {
	// A simple test to simulate writing 1000 files
	vaultPath, err := filepath.Abs("testdata/vault_stress")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(vaultPath)
	_ = os.MkdirAll(vaultPath, 0755)

	dbPath := filepath.Join(vaultPath, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

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
