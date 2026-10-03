//go:build !windows

package vault

import (
	"path/filepath"
	"testing"
)

func TestSecurePathPreservesUnixLiteralVolumeLikeNames(t *testing.T) {
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`C:\outside\note.md`, `C:outside.md`, `\\server\share\note.md`} {
		resolved, err := SecurePath(root, input)
		if err != nil || resolved != filepath.Join(canonical, input) {
			t.Errorf("literal %q: resolved=%q err=%v", input, resolved, err)
		}
	}
}
