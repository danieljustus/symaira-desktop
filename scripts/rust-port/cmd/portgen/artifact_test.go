package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodePatchArtifact(t *testing.T) {
	base := strings.Repeat("a", 40)
	patch := []byte("diff --git a/" + fixturePaths[0] + " b/" + fixturePaths[0] + "\n")
	artifact := []byte(artifactMagic + "\n# base-commit: " + base + "\n")
	artifact = append(artifact, patch...)
	gotPatch, gotBase, err := decodePatchArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if gotBase != base || string(gotPatch) != string(patch) {
		t.Fatalf("decoded artifact = (%s, %q), want (%s, %q)", gotBase, gotPatch, base, patch)
	}
	if noOpPatch, noOpBase, err := decodePatchArtifact([]byte(artifactMagic + "\n# base-commit: " + base + "\n")); err != nil || len(noOpPatch) != 0 || noOpBase != base {
		t.Fatalf("decode no-op artifact = (%q, %s, %v)", noOpPatch, noOpBase, err)
	}
	for _, malformed := range [][]byte{
		[]byte("not an artifact\n"),
		[]byte(artifactMagic + "\n# base-commit: bad\ndiff\n"),
	} {
		if _, _, err := decodePatchArtifact(malformed); err == nil {
			t.Fatalf("decodePatchArtifact accepted %q", malformed)
		}
	}
}

func TestValidatePatchPathsRejectsEscapesAndMetadataChanges(t *testing.T) {
	allowed := fixturePaths[0]
	valid := "diff --git a/" + allowed + " b/" + allowed + "\n--- a/" + allowed + "\n+++ b/" + allowed + "\n"
	if err := validatePatchPaths([]byte(valid)); err != nil {
		t.Fatalf("valid patch rejected: %v", err)
	}
	for name, patch := range map[string]string{
		"traversal":         "diff --git a/../../outside b/../../outside\n",
		"absolute":          "diff --git a/tmp/outside b/tmp/outside\n",
		"new file":          "diff --git a/" + allowed + " b/" + allowed + "\nnew file mode 100644\n",
		"mode change":       "diff --git a/" + allowed + " b/" + allowed + "\nold mode 100644\nnew mode 120000\n",
		"rename":            "diff --git a/" + allowed + " b/" + allowed + "\nrename from " + allowed + "\n",
		"unallowlisted +++": "diff --git a/" + allowed + " b/" + allowed + "\n--- a/" + allowed + "\n+++ b/../../outside\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePatchPaths([]byte(patch)); err == nil {
				t.Fatalf("validatePatchPaths accepted %q", patch)
			}
		})
	}
}

func TestApplyPatchCreatesSingleParentQWithoutEscapingAllowlist(t *testing.T) {
	repoRoot, base := newFixtureTreeRepository(t)
	if runtime.GOOS != "windows" {
		hooks := t.TempDir()
		hook := filepath.Join(hooks, "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 73\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		portgenGit(t, repoRoot, "config", "core.hooksPath", hooks)
	}
	fixture := fixturePaths[0]
	fixturePath := filepath.Join(repoRoot, filepath.FromSlash(fixture))
	if err := os.WriteFile(fixturePath, []byte("generated fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, err := gitOutput(repoRoot, "diff", "--binary", "--no-ext-diff", "--no-renames", base, "--", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, []byte("original fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := gitOutput(repoRoot, "status", "--porcelain", "--", fixture); err != nil || len(status) != 0 {
		t.Fatalf("restored patch source is not clean: status=%q err=%v", status, err)
	}
	if err := applyPatchInWorktree(repoRoot, base, patch); err != nil {
		t.Fatalf("applyPatchInWorktree() error = %v", err)
	}
	q, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQChanges(repoRoot, base, q); err != nil {
		t.Fatalf("validateQChanges() error = %v", err)
	}
	actual, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "generated fixture\n" {
		t.Fatalf("fixture content = %q", actual)
	}
}

func TestValidateDestinationsRejectsSymlinkAndHardlink(t *testing.T) {
	path := fixturePaths[0]
	t.Run("symlink file", func(t *testing.T) {
		root := t.TempDir()
		fixture := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fixture), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("safe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, fixture); err != nil {
			t.Skipf("symlink unavailable on this platform: %v", err)
		}
		if err := validateDestinations(root, []string{path}); err == nil {
			t.Fatal("validateDestinations accepted a symlink output")
		}
	})
	t.Run("symlink parent", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.MkdirAll(filepath.Join(outside, filepath.FromSlash(filepath.Dir(path))), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, filepath.FromSlash(path)), []byte("safe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "testdata")); err != nil {
			t.Skipf("symlink unavailable on this platform: %v", err)
		}
		if err := validateDestinations(root, []string{path}); err == nil {
			t.Fatal("validateDestinations accepted a symlink parent")
		}
	})
	t.Run("hardlink file", func(t *testing.T) {
		root := t.TempDir()
		fixture := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fixture), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture, []byte("shared\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.Link(fixture, outside); err != nil {
			t.Skipf("hardlink unavailable on this platform: %v", err)
		}
		if err := validateDestinations(root, []string{path}); err == nil {
			t.Fatal("validateDestinations accepted a hardlinked output")
		}
	})
}

func newFixtureTreeRepository(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := newProvenanceBaseRepository(t)
	for _, rel := range append(append([]string(nil), fixturePaths...), provenanceFixture) {
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("original fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	portgenGit(t, repoRoot, "add", "--", ".")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: complete fixture tree")
	return repoRoot, portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
}
