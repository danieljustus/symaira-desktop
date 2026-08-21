package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello world"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	if hash == "" {
		t.Error("hash is empty")
	}
	// SHA-256 of "hello world" is known.
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != want {
		t.Errorf("hash = %q, want %q", hash, want)
	}
}

func TestHashFile_NotFound(t *testing.T) {
	_, err := hashFile(filepath.Join(t.TempDir(), "nonexistent.txt"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestHashFile_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	// SHA-256 of empty string.
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hash != want {
		t.Errorf("hash = %q, want %q", hash, want)
	}
}
