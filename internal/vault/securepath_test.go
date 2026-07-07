package vault

import (
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
	if abs != filepath.Join(root, "notes", "foo.md") {
		t.Errorf("unexpected path: %s", abs)
	}

	if _, err := SecurePath(root, "../outside.md"); err == nil {
		t.Error("expected traversal to be denied")
	}
	if _, err := SecurePath(root, "notes/../../outside.md"); err == nil {
		t.Error("expected nested traversal to be denied")
	}

	// The vault root itself is allowed.
	abs, err = SecurePath(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs, root) {
		t.Errorf("expected root, got %s", abs)
	}
}
