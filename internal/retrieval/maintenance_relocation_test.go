package retrieval

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	retrievaldb "github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestRelocateIndexPersistsConfiguredLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	original, err := IndexLocation()
	if err != nil {
		t.Fatal(err)
	}
	live, err := retrievaldb.OpenAt(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.SaveDocument(&retrievaldb.Document{Path: "relocated.md", Hash: "hash", UpdatedAt: time.Now().UTC()}); err != nil {
		_ = live.Close()
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "symseek.db")
	if err := RelocateIndex(destination); err != nil {
		t.Fatalf("RelocateIndex: %v", err)
	}

	gotLocation, err := IndexLocation()
	if err != nil {
		t.Fatal(err)
	}
	if gotLocation != destination {
		t.Fatalf("IndexLocation() = %q, want %q", gotLocation, destination)
	}
	copyDB, err := sql.Open("sqlite", destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copyDB.Close() }()
	var count int
	if err := copyDB.QueryRow("SELECT COUNT(*) FROM documents WHERE path = 'relocated.md'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("relocated document count = %d, want 1", count)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original index was not preserved: %v", err)
	}
}
