//go:build !windows

package vault

import (
	"os"
	"path/filepath"
	"syscall"
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

func TestConfinedRootRejectsSpecialAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFileInRoot(root, fifo); err == nil {
		t.Fatal("FIFO was accepted as a vault file")
	}
	if _, _, err := ReadFileInRoot(root, root); err == nil {
		t.Fatal("directory was accepted as a vault file")
	}
	large := filepath.Join(root, "large.md")
	//nolint:gosec // large is a fixed child of t.TempDir
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxRootReadBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFileInRoot(root, large); err == nil {
		t.Fatal("oversized file was accepted")
	}
}
