//go:build windows

package vault

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurePathWindowsDriveAndUNCInputsStayConfined(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`C:\outside\note.md`,
		`D:\other\..\note.md`,
		`\\server\share\note.md`,
		`..\outside\note.md`,
	} {
		resolved, err := SecurePath(root, input)
		if err != nil {
			continue
		}
		rel, relErr := filepath.Rel(canonicalRoot, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Fatalf("input %q resolved outside root: %q (rel %q, err %v)", input, resolved, rel, relErr)
		}
	}
}
