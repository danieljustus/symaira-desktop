package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameBinaryDetectsHardLinkAndDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left")
	right := filepath.Join(dir, "right")
	alias := filepath.Join(dir, "alias")
	if err := os.WriteFile(left, []byte("same bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("same bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(left, alias); err != nil {
		t.Fatal(err)
	}
	if same, err := sameBinary(left, alias); err != nil || !same {
		t.Fatalf("hard link not detected: same=%t err=%v", same, err)
	}
	if same, err := sameBinary(left, right); err != nil || same {
		t.Fatalf("distinct files conflated: same=%t err=%v", same, err)
	}
}
