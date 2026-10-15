package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSandboxSetupErrorsPreserveExistingEvidence(t *testing.T) {
	root := t.TempDir()
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, path := range []string{home, workspace, filepath.Join(workspace, "kept-directory")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	keptFile := filepath.Join(workspace, "kept-file")
	if err := os.WriteFile(keptFile, []byte("retained fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := buildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, sandbox, want string
		setup               SetupFile
	}{
		{"unsupported base", root, "unsupported setup base", SetupFile{Base: "other", Path: "new"}},
		{"empty path", root, "non-empty and relative", SetupFile{}},
		{"absolute path", root, "non-empty and relative", SetupFile{Path: keptFile}},
		{"unsupported kind", root, "unsupported setup kind", SetupFile{Path: "new", Kind: "socket"}},
		{"directory over file", root, "", SetupFile{Path: "kept-file", Kind: "directory"}},
		{"file over directory", root, "", SetupFile{Path: "kept-directory", Content: "replacement"}},
		{"link over file", root, "", SetupFile{Path: "kept-file", Kind: "symlink", LinkTarget: "kept-directory"}},
		{"non-directory root", keptFile, "", SetupFile{Path: "new", Content: "replacement"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := setupSandboxFile(test.sandbox, home, workspace, test.setup, nil)
			if err == nil || test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected setup rejection containing %q, got %v", test.want, err)
			}
			after, err := buildManifest(root)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected setup changed retained evidence: %v", err)
			}
		})
	}
}
