package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifyOracleSourceGuard(t *testing.T) {
	tests := []struct {
		name     string
		revision string
		release  string
		edit     func(t *testing.T, root string)
	}{
		{
			name: "pristine passes",
		},
		{
			name: "changed source rejected",
			edit: func(t *testing.T, root string) {
				path := firstPinnedSource(t, root)
				if !filepath.IsLocal(path) {
					t.Fatalf("path %s is not local", path)
				}
				// #nosec G304 -- test fixture file read within temp repo clone
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
				if err != nil {
					t.Fatal(err)
				}
				// #nosec G703,G304 -- test fixture file write within temp repo clone
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), append(content, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "added source rejected",
			edit: func(t *testing.T, root string) {
				path := filepath.Join(root, "internal", "history", "added_guard.go")
				if err := os.WriteFile(path, []byte("package history\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, root, "add", "internal/history/added_guard.go")
			},
		},
		{
			name: "untracked source rejected",
			edit: func(t *testing.T, root string) {
				path := filepath.Join(root, "internal", "history", "untracked_guard.go")
				if err := os.WriteFile(path, []byte("package history\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ignored added source rejected",
			edit: func(t *testing.T, root string) {
				ignore := filepath.Join(root, ".gitignore")
				// #nosec G304 -- ignore path is inside t.TempDir() clone
				file, err := os.OpenFile(ignore, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("internal/history/ignored_guard.go\n"); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "internal", "history", "ignored_guard.go")
				if err := os.WriteFile(path, []byte("package history\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "wrong revision rejected",
			revision: "HEAD",
		},
		{
			name:    "wrong release label rejected",
			release: "invented-release",
		},
		{
			name: "missing source rejected",
			edit: func(t *testing.T, root string) {
				path := firstPinnedSource(t, root)
				if !filepath.IsLocal(path) {
					t.Fatalf("path %s is not local", path)
				}
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed go.mod rejected",
			edit: func(t *testing.T, root string) {
				path := filepath.Join(root, "go.mod")
				// #nosec G304 -- reading go.mod in t.TempDir() clone
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// #nosec G703,G304 -- modifying go.mod in t.TempDir() clone
				if err := os.WriteFile(path, append(content, []byte("\n// modified\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed go.sum rejected",
			edit: func(t *testing.T, root string) {
				path := filepath.Join(root, "go.sum")
				// #nosec G304 -- reading go.sum in t.TempDir() clone
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// #nosec G703,G304 -- modifying go.sum in t.TempDir() clone
				if err := os.WriteFile(path, append(content, []byte("\n# modified\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := clonePinnedRepo(t)
			revision := test.revision
			if revision == "" {
				revision = defaultOracleCommit
			}
			release := test.release
			if release == "" {
				release = defaultOracleRelease
			}
			_, err := verifyOracle(root, revision, release)
			if test.edit == nil {
				if test.revision == "" && test.release == "" && err != nil {
					t.Fatalf("verifyOracle rejected pristine clone: %v", err)
				}
				if (test.revision != "" || test.release != "") && err == nil {
					t.Fatal("verifyOracle accepted an invalid revision or release")
				}
				return
			}
			test.edit(t, root)
			if _, err := verifyOracle(root, revision, release); err == nil {
				t.Fatal("verifyOracle accepted a mutated clone")
			}
		})
	}
}

func clonePinnedRepo(t *testing.T) string {
	t.Helper()
	source, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "repo")
	runGit(t, filepath.Dir(root), "clone", "--no-local", source, root)
	runGit(t, root, "checkout", "--detach", defaultOracleCommit)
	return root
}

func firstPinnedSource(t *testing.T, root string) string {
	t.Helper()
	output, err := gitOutput(root, "ls-tree", "-r", "--name-only", defaultOracleCommit, "--", "internal/history")
	if err != nil {
		t.Fatal(err)
	}
	paths := goSourcePaths(output)
	if len(paths) == 0 {
		t.Fatal("pinned revision has no Go sources")
	}
	return paths[0]
}

const testGitCommandTimeout = 30 * time.Second

func runGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testGitCommandTimeout)
	defer cancel()
	// #nosec G204 -- test helper executing fixed git command with test args in t.TempDir()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("git %v timed out: %v", args, ctx.Err())
		}
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return output
}
