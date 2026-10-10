package inventory

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sourceSafetyGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Source Safety Test", "-c", "user.email=source-safety@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func sourceSafetyRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	sourceSafetyGit(t, root, "init", "--template=", "--initial-branch=main", root)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module source-safety.test\n\ngo 1.26.6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sourceSafetyGit(t, root, "add", ".")
	sourceSafetyGit(t, root, "commit", "-m", "base")
	return root, sourceSafetyGit(t, root, "rev-parse", "HEAD")
}

func TestCloneOracleSourceRejectsSymlinkIntoCaller(t *testing.T) {
	root, source := sourceSafetyRepo(t)
	alias := filepath.Join(t.TempDir(), "caller-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("native directory symlink unavailable: %v", err)
	}
	destination := filepath.Join(alias, "unexpected-source")
	if err := CloneOracleSource(root, source, destination); err == nil {
		t.Fatal("accepted an outside-looking symlink destination inside caller")
	}
	if _, err := os.Stat(filepath.Join(root, "unexpected-source")); !os.IsNotExist(err) {
		t.Fatalf("source operation wrote inside caller: %v", err)
	}
}

func TestCloneOracleSourcePreservesExistingEmptyDestination(t *testing.T) {
	root, source := sourceSafetyRepo(t)
	destination := t.TempDir()
	if err := CloneOracleSource(root, source, destination); err == nil {
		t.Fatal("claimed a pre-existing destination, even though it was empty")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pre-existing destination changed: entries=%v error=%v", entries, err)
	}
}

func TestCreateOracleBundleRejectsBranchOnlyAnchor(t *testing.T) {
	root, _ := sourceSafetyRepo(t)
	sourceSafetyGit(t, root, "switch", "-c", "source")
	if err := os.WriteFile(filepath.Join(root, "branch-only.txt"), []byte("not on main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sourceSafetyGit(t, root, "add", ".")
	sourceSafetyGit(t, root, "commit", "-m", "branch-only anchor")
	anchor := sourceSafetyGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module changed-source.test\n\ngo 1.26.6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sourceSafetyGit(t, root, "add", ".")
	sourceSafetyGit(t, root, "commit", "-m", "changed source")
	source := sourceSafetyGit(t, root, "rev-parse", "HEAD")
	if _, err := CreateOracleBundle(root, source, anchor); err == nil {
		t.Fatal("accepted a branch-only anchor that cannot survive squash into main")
	}
}

func TestInventoryGitReadsDoNotLazyFetch(t *testing.T) {
	origin, _ := sourceSafetyRepo(t)
	sourceSafetyGit(t, origin, "config", "uploadpack.allowFilter", "true")
	blob := sourceSafetyGit(t, origin, "rev-parse", "HEAD:go.mod")
	originURL := (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(origin), "/")}).String()
	clone := func(name string) string {
		root := filepath.Join(t.TempDir(), name)
		sourceSafetyGit(t, origin, "clone", "--filter=blob:none", "--no-checkout", "--", originURL, root)
		sourceSafetyGit(t, root, "config", "protocol.file.allow", "always")
		probe := exec.Command("git", "--no-lazy-fetch", "cat-file", "-e", blob)
		probe.Dir = root
		if err := probe.Run(); err == nil {
			t.Fatal("partial-clone control unexpectedly contains the omitted blob")
		}
		return root
	}
	control := clone("positive-control")
	if got := sourceSafetyGit(t, control, "cat-file", "blob", blob); got != "module source-safety.test\n\ngo 1.26.6" {
		t.Fatal("positive control did not actually lazy-fetch the genuine blob")
	}
	root := clone("guarded")
	if _, err := inventoryGitOutput(root, "cat-file", "blob", blob); err == nil {
		t.Fatal("read-only Git helper lazy-fetched a missing object into the caller")
	}
	probe := exec.Command("git", "--no-lazy-fetch", "cat-file", "-e", blob)
	probe.Dir = root
	if err := probe.Run(); err == nil {
		t.Fatal("read-only helper mutated caller by materializing the missing blob")
	}
}
