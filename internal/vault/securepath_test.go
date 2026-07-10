package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurePath(t *testing.T) {
	root := t.TempDir()

	abs, err := SecurePath(root, "notes/foo.md")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	if !strings.HasPrefix(abs, canonicalRoot) {
		t.Errorf("expected path under vault root, got %s", abs)
	}

	if _, err := SecurePath(root, "../outside.md"); err == nil {
		t.Error("expected traversal to be denied")
	}
	if _, err := SecurePath(root, "notes/../../outside.md"); err == nil {
		t.Error("expected nested traversal to be denied")
	}

	abs, err = SecurePath(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs, canonicalRoot) {
		t.Errorf("expected root, got %s", abs)
	}
}

func TestSecurePath_FileSymlinkEscape(t *testing.T) {
	vaultDir := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := writeFile(outsideFile, "secret"); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(vaultDir, "link.txt")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatal(err)
	}

	if _, err := SecurePath(vaultDir, "link.txt"); err == nil {
		t.Error("expected file symlink escape to be denied")
	}
}

func TestSecurePath_DirSymlinkEscape(t *testing.T) {
	vaultDir := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "data.md")
	if err := writeFile(outsideFile, "data"); err != nil {
		t.Fatal(err)
	}

	linkDir := filepath.Join(vaultDir, "linked")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Fatal(err)
	}

	if _, err := SecurePath(vaultDir, "linked/data.md"); err == nil {
		t.Error("expected directory symlink escape to be denied")
	}
}

func TestSecurePath_MissingParent(t *testing.T) {
	vaultDir := t.TempDir()

	abs, err := SecurePath(vaultDir, "new/nested/file.md")
	if err != nil {
		t.Fatalf("expected new-file path to resolve, got: %v", err)
	}
	canonicalVault, _ := filepath.EvalSymlinks(vaultDir)
	if !strings.HasPrefix(abs, canonicalVault) {
		t.Errorf("expected path under vault, got %s", abs)
	}
}

func TestSecurePath_LegitimateNestedPath(t *testing.T) {
	vaultDir := t.TempDir()

	abs, err := SecurePath(vaultDir, "notes/sub/deep/file.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonicalVault, _ := filepath.EvalSymlinks(vaultDir)
	if !strings.HasPrefix(abs, canonicalVault) {
		t.Errorf("expected path under vault, got %s", abs)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
