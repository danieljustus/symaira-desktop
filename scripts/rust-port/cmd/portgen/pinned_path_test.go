package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPinnedCheckPathCanResolveGitForOracleChecks is the regression control for
// the sanitized check environment: the frontmatter write generator resolves the
// pinned Go revision from immutable Git objects, but the sanitized environment
// drops the ambient PATH, so with a GOROOT-only PATH the check target used to
// fail with "git: executable file not found in $PATH" — a red gate that never
// checked the fixture. The resolution has to happen inside the child process,
// which is what the shell probe below reproduces.
func TestPinnedCheckPathCanResolveGitForOracleChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH probe runs through a POSIX shell; the pinned PATH logic is platform-shared")
	}
	pinned := pinnedCheckPath()
	//nolint:staticcheck // the harness deliberately uses the GOROOT it was built with
	goOnlyPath := filepath.Join(runtime.GOROOT(), "bin")
	if pinned == goOnlyPath {
		t.Fatalf("pinned check PATH is the GOROOT-only PATH %q; git is not resolvable for the oracle checks", pinned)
	}
	if !strings.HasSuffix(pinned, string(filepath.ListSeparator)+goOnlyPath) {
		t.Fatalf("pinned check PATH %q does not keep the GOROOT tool directory", pinned)
	}

	if err := probeGitWithPath(t, pinned); err != nil {
		t.Fatalf("pinned check PATH cannot resolve git: %v", err)
	}
	if err := probeGitWithPath(t, goOnlyPath); err == nil {
		t.Fatalf("a GOROOT-only PATH resolved git; the negative control no longer reproduces the failure it guards")
	}
}

// probeGitWithPath resolves git the way a child process does: the parent finds
// the shell, the shell then searches the PATH handed to it.
func probeGitWithPath(t *testing.T, path string) error {
	t.Helper()
	//nolint:gosec // fixed arguments: a PATH probe with no caller input
	command := exec.Command("sh", "-c", "command -v git")
	command.Env = []string{"PATH=" + path}
	return command.Run()
}
