package pathutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRestrictToHome_InsideHome(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	file := filepath.Join(tempDir, "test.md")
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := RestrictToHome(file)
	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	// The returned path is resolved via EvalSymlinks, so compare against
	// the resolved file path rather than the original.
	resolved, _ := filepath.EvalSymlinks(file)
	if got != resolved {
		t.Errorf("expected %q, got %q", resolved, got)
	}
}

func TestRestrictToHome_OutsideHome(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	outside := filepath.Join(tempDir, "..", "outside.md")
	if err := os.WriteFile(outside, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = RestrictToHome(outside)
	if err == nil {
		t.Error("expected error for path outside home")
	}
}

func TestRestrictToHome_SymlinkOutsideHome(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	outsideDir := filepath.Join(tempDir, "..", "secret")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "data.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	linkDir := filepath.Join(tempDir, "notes")
	if err := os.MkdirAll(linkDir, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "secret.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}

	_, err = RestrictToHome(link)
	if err == nil {
		t.Error("expected error for symlink pointing outside home")
	}
}

func TestRestrictToHome_NonExistent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	_, err = RestrictToHome(filepath.Join(tempDir, "does-not-exist.md"))
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestRestrictToHome_ReturnsResolvedPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	link := filepath.Join(tempDir, "link")
	target := filepath.Join(tempDir, "target.md")
	if err := os.WriteFile(target, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := RestrictToHome(link)
	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	// The returned path is resolved via EvalSymlinks.
	resolved, _ := filepath.EvalSymlinks(link)
	if got != resolved {
		t.Errorf("expected resolved path %q, got %q", resolved, got)
	}
}

func TestRestrictToHome_BrokenSymlink(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	link := filepath.Join(tempDir, "broken")
	if err := os.Symlink(filepath.Join(tempDir, "missing"), link); err != nil {
		t.Fatal(err)
	}

	_, err = RestrictToHome(link)
	if err == nil {
		t.Error("expected error for broken symlink")
	}
}

func TestRestrictToHome_RelativePath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pathutil-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	subDir := filepath.Join(tempDir, "docs")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(subDir, "note.md")
	if err := os.WriteFile(file, []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(subDir); err != nil {
		t.Fatal(err)
	}

	got, err := RestrictToHome("note.md")
	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(file)
	if got != resolved {
		t.Errorf("expected resolved path %q, got %q", resolved, got)
	}
}

func TestPathRestrictionError(t *testing.T) {
	err := &PathRestrictionError{Path: "/tmp/secret", Root: "/home/user"}
	got := err.Error()
	if !strings.Contains(got, "/tmp/secret") {
		t.Errorf("expected error to mention path, got %q", got)
	}
	if !strings.Contains(got, "/home/user") {
		t.Errorf("expected error to mention root, got %q", got)
	}
}

func TestRestrictToHome_UnresolvableHome(t *testing.T) {
	t.Setenv("HOME", filepath.Join(os.TempDir(), "nonexistent-home-dir-"+strconv.Itoa(int(time.Now().UnixNano()))))

	_, err := RestrictToHome("note.md")
	if err == nil {
		t.Error("expected error when home directory cannot be resolved")
	}
}
