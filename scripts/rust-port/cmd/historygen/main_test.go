package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
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

func TestHTMLPathForGOOS(t *testing.T) {
	const (
		unixPath    = "html/<div><script>alert(1)</script>.md"
		windowsPath = "html/ampersand&injection.md"
	)

	gotWin := htmlPathForGOOS("windows")
	if gotWin != windowsPath {
		t.Fatalf("htmlPathForGOOS(windows) = %q, want %q", gotWin, windowsPath)
	}

	if !strings.Contains(gotWin, "&") {
		t.Errorf("expected Windows path %q to contain ampersand (&)", gotWin)
	}

	const forbiddenPathChars = `<>:"|?*`
	if strings.ContainsAny(gotWin, forbiddenPathChars) {
		t.Errorf("Windows path %q contains forbidden characters (%s)", gotWin, forbiddenPathChars)
	}

	base := filepath.Base(gotWin)
	const forbiddenFilenameChars = `<>:"/\|?*`
	if strings.ContainsAny(base, forbiddenFilenameChars) {
		t.Errorf("Windows filename %q contains forbidden characters (%s)", base, forbiddenFilenameChars)
	}

	for _, r := range gotWin {
		if r < 32 {
			t.Errorf("Windows path %q contains control character %d", gotWin, r)
		}
	}

	nonWindows := []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "aix", "plan9", "js", "wasip1"}
	for _, goos := range nonWindows {
		got := htmlPathForGOOS(goos)
		if got != unixPath {
			t.Errorf("htmlPathForGOOS(%q) = %q, want %q", goos, got, unixPath)
		}
	}
}

func TestClassifyError(t *testing.T) {
	t.Run("wrapped path error positive", func(t *testing.T) {
		baseErr := &os.PathError{
			Op:   "openat",
			Path: `\abs\evil.md`,
			Err:  errors.New("path escapes from parent"),
		}
		if got := classifyError(baseErr); got != "invalid_path" {
			t.Fatalf("classifyError(baseErr) = %q, want invalid_path", got)
		}

		wrappedErr := fmt.Errorf("wrapped context: %w", baseErr)
		if got := classifyError(wrappedErr); got != "invalid_path" {
			t.Fatalf("classifyError(wrappedErr) = %q, want invalid_path", got)
		}
	})

	t.Run("path text merely containing phrase negative control", func(t *testing.T) {
		pathWithPhraseErr := &os.PathError{
			Op:   "open",
			Path: "notes/path escapes from parent.md",
			Err:  os.ErrNotExist,
		}
		if got := classifyError(pathWithPhraseErr); got != "other" {
			t.Fatalf("classifyError(pathWithPhraseErr) = %q, want other", got)
		}

		plainTextErr := errors.New("read failed: path escapes from parent in log text")
		if got := classifyError(plainTextErr); got != "other" {
			t.Fatalf("classifyError(plainTextErr) = %q, want other", got)
		}
	})

	t.Run("unrelated permission error negative control", func(t *testing.T) {
		permErr := &os.PathError{
			Op:   "openat",
			Path: "/abs/evil.md",
			Err:  os.ErrPermission,
		}
		if got := classifyError(permErr); got != "other" {
			t.Fatalf("classifyError(permErr) = %q, want other", got)
		}

		wrappedPermErr := fmt.Errorf("permission wrapped: %w", permErr)
		if got := classifyError(wrappedPermErr); got != "other" {
			t.Fatalf("classifyError(wrappedPermErr) = %q, want other", got)
		}
	})

	t.Run("production snapshot rooted path rejection", func(t *testing.T) {
		const rootEnv = "SYMDESK_HISTORY_ROOTED_TEST_ROOT"
		vaultRoot := os.Getenv(rootEnv)
		if vaultRoot == "" {
			// Store retains an os.Root without a public Close method. Run the
			// assertion in a child so process exit releases its Windows handle
			// before the parent's temporary-directory cleanup.
			vaultRoot = t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestClassifyError$/^production_snapshot_rooted_path_rejection$", "-test.v") // #nosec G204 -- os.Executable returns this test binary; all arguments are fixed.
			cmd.Env = append(os.Environ(), rootEnv+"="+vaultRoot)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("rooted-path child: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "--- PASS: TestClassifyError/production_snapshot_rooted_path_rejection") {
				t.Fatalf("rooted-path child assertion did not execute:\n%s", output)
			}
			return
		}
		store := history.NewStore(vaultRoot)
		entry, err := store.Snapshot("/abs/evil.md")
		if err == nil {
			t.Fatalf("expected store.Snapshot(/abs/evil.md) to fail, got entry: %+v", entry)
		}
		if entry != nil {
			t.Fatalf("expected nil entry on error, got: %+v", entry)
		}
		if got := classifyError(err); got != "invalid_path" {
			t.Fatalf("classifyError(err) = %q, want %q (raw error: %v)", got, "invalid_path", err)
		}
	})
}
