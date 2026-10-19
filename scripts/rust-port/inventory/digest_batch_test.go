package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #1243: the batched revision digest must hash exactly the bytes and order the
// former per-file `git show` loop hashed, including empty, binary and
// newline-free blobs, and must fail closed on an object that is not a blob.
func TestGitRevisionDigestBatchMatchesPerFileShow(t *testing.T) {
	configPath, cleanup, err := PrivateGitConfig()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	git := func(root string, args ...string) []byte {
		t.Helper()
		//nolint:gosec // args are fixed test operations in a disposable local repository, without a shell.
		cmd := exec.Command("git", append([]string{"-c", "user.name=Digest Batch Test", "-c", "user.email=digest-batch@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		cmd.Env = inventoryGitEnvironment(os.Environ(), configPath)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	root := t.TempDir()
	git(root, "init", "--initial-branch=main", "--template=", root)
	files := map[string]string{
		"go.mod":                      "module digest-batch.test\n\ngo 1.26.6\n",
		"cmd/tool/main.go":            "package main\nfunc main() {}\n",
		"internal/a/empty.go":         "",
		"internal/a/no-newline.go":    "package a",
		"internal/b/data.bin":         "\x00\x01\n\r\n\xff",
		"internal/b/b_test.go":        "package b\n",
		"internal/b/testdata/fix.txt": "excluded\n",
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(root, "add", ".")
	git(root, "commit", "-q", "-m", "base")
	revision := strings.TrimSpace(string(git(root, "rev-parse", "HEAD")))

	got, err := ComputeGitRevisionProductionSourceDigest(root, revision)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"ls-tree", "-r", "--name-only", revision, "--", "cmd", "internal"}, productionContractFiles()...)
	var listed []string
	for _, rel := range strings.Split(strings.TrimSpace(string(git(root, args...))), "\n") {
		if isProductionContractInput(rel) {
			listed = append(listed, rel)
		}
	}
	if len(listed) != 5 {
		t.Fatalf("expected five production inputs, got %v", listed)
	}
	hasher := sha256.New()
	sort.Strings(listed)
	for _, rel := range listed {
		_, _ = io.WriteString(hasher, rel+"\n")
		_, _ = hasher.Write(git(root, "show", revision+":"+rel))
	}
	if want := hex.EncodeToString(hasher.Sum(nil)); got != want {
		t.Fatalf("batched digest %s differs from per-file show digest %s", got, want)
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"internal/a/missing.go", "internal"} {
		err := inventoryGitBlobs(root, gitPath, revision, []string{"go.mod", rel}, func(string, io.Reader) error { return nil })
		if err == nil || !strings.Contains(err.Error(), rel) {
			t.Fatalf("object %s: expected a fail-closed error naming it, got %v", rel, err)
		}
	}
}
