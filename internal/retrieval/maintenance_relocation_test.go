package retrieval

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRelocateIndexPersistsConfiguredLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	original, err := IndexLocation()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(original), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := append([]byte("SQLite format 3\x00"), []byte("derived-index")...)
	if err := os.WriteFile(original, contents, 0o600); err != nil {
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
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, contents) {
		t.Fatalf("relocated index contents = %q, want %q", got, contents)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original index was not preserved: %v", err)
	}
}
