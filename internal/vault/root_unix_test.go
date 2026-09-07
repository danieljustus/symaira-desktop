//go:build !windows

package vault

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfinedRootRejectsExternalSymlinksAndReadsContained(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(root, "inside.md")
	outsideFile := filepath.Join(outside, "outside.md")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	containedLink := filepath.Join(root, "contained.md")
	externalLink := filepath.Join(root, "external.md")
	if err := os.Symlink(inside, containedLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, externalLink); err != nil {
		t.Fatal(err)
	}

	doc, err := ParseFileInRoot(root, containedLink)
	if err != nil {
		t.Fatalf("contained symlink should remain readable: %v", err)
	}
	if doc.Body != "inside" {
		t.Fatalf("contained symlink body = %q, want inside", doc.Body)
	}
	if _, _, err := ReadFileInRoot(root, externalLink); err == nil {
		t.Fatal("external file symlink was readable")
	}
	if !IsExternalSymlink(root, externalLink) {
		t.Fatal("external file symlink was not classified as external")
	}
}

func TestConfinedRootRejectsExternalDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "nested.md")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ReadFileInRoot(root, filepath.Join(linkDir, "nested.md")); err == nil {
		t.Fatal("external directory symlink was readable")
	}
}
