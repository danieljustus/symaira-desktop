package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
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
		//nolint:gosec // the fake hook must be executable to exercise hook isolation.
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
	//nolint:gosec // fixturePath is under this test's temporary repository.
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

func TestCommitFixtureOutputsAndCreatePatchArtifact(t *testing.T) {
	repoRoot, base := newFixtureTreeRepository(t)
	if err := verifyCallerSnapshot(repoRoot, base); err != nil {
		t.Fatalf("verifyCallerSnapshot() at P error = %v", err)
	}
	if err := commitFixtureOutputs(repoRoot, base); !errors.Is(err, errNoFixtureChanges) {
		t.Fatalf("commitFixtureOutputs() without changes error = %v, want errNoFixtureChanges", err)
	}
	empty, err := createPatchArtifact(repoRoot, base)
	if err != nil {
		t.Fatalf("createPatchArtifact() at P error = %v", err)
	}
	if want := artifactMagic + "\n# base-commit: " + base + "\n"; string(empty) != want {
		t.Fatalf("empty artifact = %q, want %q", empty, want)
	}
	fixturePath := filepath.Join(repoRoot, filepath.FromSlash(fixturePaths[0]))
	if err := os.WriteFile(fixturePath, []byte("generated fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitFixtureOutputs(repoRoot, base); err != nil {
		t.Fatalf("commitFixtureOutputs() error = %v", err)
	}
	if err := verifyCallerSnapshot(repoRoot, base); err == nil {
		t.Fatal("verifyCallerSnapshot() accepted a moved HEAD")
	}
	artifact, err := createPatchArtifact(repoRoot, base)
	if err != nil {
		t.Fatalf("createPatchArtifact() error = %v", err)
	}
	if !bytes.Contains(artifact, []byte("+generated fixture")) {
		t.Fatalf("artifact lacks generated change:\n%s", artifact)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitFixtureOutputs(repoRoot, base); err == nil || !strings.Contains(err.Error(), "untracked") {
		t.Fatalf("commitFixtureOutputs() with untracked output error = %v", err)
	}
}

func TestApplyArtifactCommitRejectsMissingAndForeignArtifacts(t *testing.T) {
	repoRoot, base := newFixtureTreeRepository(t)
	if err := applyArtifactCommit(repoRoot, filepath.Join(t.TempDir(), "missing.patch")); err == nil || !strings.Contains(err.Error(), "read patch artifact") {
		t.Fatalf("applyArtifactCommit(missing) error = %v", err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign.patch")
	other := strings.Repeat("b", len(base))
	if err := os.WriteFile(foreign, []byte(artifactMagic+"\n# base-commit: "+other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyArtifactCommit(repoRoot, foreign); err == nil || !strings.Contains(err.Error(), "does not match checked-out P") {
		t.Fatalf("applyArtifactCommit(foreign base) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, filepath.FromSlash(fixturePaths[0])), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyArtifactCommit(repoRoot, foreign); err == nil || !strings.Contains(err.Error(), "clean worktree") {
		t.Fatalf("applyArtifactCommit(dirty) error = %v", err)
	}
}

func TestRunGeneratorCommandReportsFailureOutput(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	environment := generationEnvWithActivation(os.Environ())
	if environment[len(environment)-1] != "PORT_GENERATE=1" {
		t.Fatalf("generationEnvWithActivation() tail = %q", environment[len(environment)-1])
	}
	if err := runGeneratorCommand(goTool, t.TempDir(), environment, "version", "version"); err != nil {
		t.Fatalf("runGeneratorCommand(version) error = %v", err)
	}
	err = runGeneratorCommand(goTool, t.TempDir(), environment, "bogus", "no-such-subcommand")
	if err == nil || !strings.Contains(err.Error(), "generate bogus") {
		t.Fatalf("runGeneratorCommand(bogus) error = %v", err)
	}
}
