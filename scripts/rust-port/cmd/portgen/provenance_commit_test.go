package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestVerifyProvenanceCommitRequiresDirectOutputOnlyChild(t *testing.T) {
	t.Run("accepts provenance-only child", func(t *testing.T) {
		repoRoot, oracle := newProvenanceCommitRepository(t, nil)
		if err := verifyProvenanceCommit(repoRoot, oracle); err != nil {
			t.Fatalf("verifyProvenanceCommit() error = %v", err)
		}
	})

	t.Run("rejects unrelated final-commit content", func(t *testing.T) {
		repoRoot, oracle := newProvenanceCommitRepository(t, map[string]string{
			"unreviewed.txt": "this must not be hidden in Q\n",
		})
		err := verifyProvenanceCommit(repoRoot, oracle)
		if err == nil || !strings.Contains(err.Error(), "unexpected path") {
			t.Fatalf("verifyProvenanceCommit() error = %v, want unexpected-path failure", err)
		}
	})

	t.Run("rejects wrong oracle parent", func(t *testing.T) {
		repoRoot, _ := newProvenanceCommitRepository(t, nil)
		err := verifyProvenanceCommit(repoRoot, strings.Repeat("a", 40))
		if err == nil || !strings.Contains(err.Error(), "does not equal the direct parent") {
			t.Fatalf("verifyProvenanceCommit() error = %v, want direct-parent failure", err)
		}
	})

	t.Run("rejects a valid side-branch commit", func(t *testing.T) {
		repoRoot, oracle := newProvenanceCommitRepository(t, nil)
		mainBranch := portgenGitOutput(t, repoRoot, "branch", "--show-current")
		portgenGit(t, repoRoot, "checkout", "-q", "-b", "side", oracle)
		writePortgenTestFile(t, repoRoot, "side-branch.txt", "not the oracle parent\n")
		portgenGit(t, repoRoot, "add", "--", "side-branch.txt")
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: side branch")
		side := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
		portgenGit(t, repoRoot, "checkout", "-q", mainBranch)

		err := verifyProvenanceCommit(repoRoot, side)
		if err == nil || !strings.Contains(err.Error(), "direct parent") {
			t.Fatalf("verifyProvenanceCommit() error = %v, want direct-parent failure", err)
		}
	})

	t.Run("rejects merge commit Q", func(t *testing.T) {
		repoRoot := newProvenanceBaseRepository(t)
		oracle := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
		baseBranch := portgenGitOutput(t, repoRoot, "branch", "--show-current")
		portgenGit(t, repoRoot, "checkout", "-q", "-b", "side")
		writePortgenTestFile(t, repoRoot, "side.txt", "independent side parent\n")
		portgenGit(t, repoRoot, "add", "--", "side.txt")
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: side parent")
		portgenGit(t, repoRoot, "checkout", "-q", baseBranch)
		writePortgenTestFile(t, repoRoot, provenanceFixture, "{}\n")
		portgenGit(t, repoRoot, "add", "--", provenanceFixture)
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: provenance child")
		portgenGit(t, repoRoot, "merge", "--no-ff", "-m", "test: merge Q", "side")

		err := verifyProvenanceCommit(repoRoot, oracle)
		if err == nil || !strings.Contains(err.Error(), "exactly one parent") {
			t.Fatalf("verifyProvenanceCommit() error = %v, want one-parent failure", err)
		}
	})

	t.Run("requires provenance file in Q", func(t *testing.T) {
		repoRoot := newProvenanceBaseRepository(t)
		oracle := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
		writePortgenTestFile(t, repoRoot, "testdata/port/cli/symdesk-command-tree.json", "{}\n")
		portgenGit(t, repoRoot, "add", "--", "testdata/port/cli/symdesk-command-tree.json")
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: fixture-only Q without provenance")

		err := verifyProvenanceCommit(repoRoot, oracle)
		if err == nil || !strings.Contains(err.Error(), "must change") {
			t.Fatalf("verifyProvenanceCommit() error = %v, want missing-provenance failure", err)
		}
	})
}

func TestRunProvenanceCheckAcceptsImmutablePToQ(t *testing.T) {
	repoRoot := newProvenanceBaseRepository(t)
	for _, rel := range fixturePaths {
		writePortgenTestFile(t, repoRoot, rel, "{}\n")
	}
	writePortgenTestFile(t, repoRoot, provenanceFixture, "{}\n")
	portgenGit(t, repoRoot, "add", "--", "testdata/port")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: complete immutable P fixtures")
	oracle := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")

	productionDigest, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, oracle)
	if err != nil {
		t.Fatal(err)
	}
	generatorDigest, err := inventory.ComputeGitRevisionGeneratorSourceDigest(repoRoot, oracle)
	if err != nil {
		t.Fatal(err)
	}
	checksums := make(map[string]string, len(fixturePaths))
	for _, rel := range fixturePaths {
		sum, err := gitRevisionChecksum(repoRoot, oracle, rel)
		if err != nil {
			t.Fatal(err)
		}
		checksums[rel] = sum
	}
	content, err := json.MarshalIndent(inventory.ProvenanceDocument{
		SchemaVersion:          1,
		Oracle:                 inventory.Oracle{Commit: oracle, Release: "test-release"},
		ProductionSourceDigest: productionDigest,
		GeneratorSourceDigest:  generatorDigest,
		SurfaceCounts:          expectedSurfaceCounts(),
		FixtureChecksums:       checksums,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writePortgenTestFile(t, repoRoot, provenanceFixture, string(append(content, '\n')))
	portgenGit(t, repoRoot, "add", "--", provenanceFixture)
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: provenance-only Q")

	originalRunner := runFixtureCheckTarget
	runFixtureCheckTarget = func(_ string, _ string, _ []string, _ fixtureCheckTarget) error { return nil }
	t.Cleanup(func() { runFixtureCheckTarget = originalRunner })
	if err := runProvenanceCheck(repoRoot); err != nil {
		t.Fatalf("runProvenanceCheck() error = %v", err)
	}
}

func TestResolveGenerationOracleCommitDefaultsToHEAD(t *testing.T) {
	repoRoot := newProvenanceBaseRepository(t)
	want := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")

	got, err := resolveGenerationOracleCommit(repoRoot, "")
	if err != nil {
		t.Fatalf("resolveGenerationOracleCommit(\"\") error = %v", err)
	}
	if got != want {
		t.Fatalf("resolveGenerationOracleCommit(\"\") = %s, want HEAD %s", got, want)
	}
	if _, err := resolveGenerationOracleCommit(repoRoot, "not-a-sha"); err == nil {
		t.Fatal("resolveGenerationOracleCommit() accepted a malformed requested commit")
	}
	writePortgenTestFile(t, repoRoot, "unrelated.txt", "new HEAD\n")
	portgenGit(t, repoRoot, "add", "--", "unrelated.txt")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: advance head")
	if _, err := resolveGenerationOracleCommit(repoRoot, want); err == nil {
		t.Fatal("resolveGenerationOracleCommit() accepted an oracle that is not current HEAD")
	}
}

func TestSanitizedCheckEnvironmentRemovesActivationVariablesCaseInsensitively(t *testing.T) {
	got := sanitizedCheckEnvironment([]string{
		"SAFE=retained",
		"PORT_GENERATE=1",
		"portgen_generate=1",
		"COREGEN_GENERATE=1",
		"SYMDESK_PORT_GENERATE=1",
		"OTHER_PORT_GENERATE=1",
		"PORTGEN_SIDECAR_ORACLE_COMMIT=poison",
		"PORTGEN_SIDECAR_ORACLE_RELEASE=poison",
		"GOFLAGS=-modfile=poison.mod",
		"GIT_DIR=/poison",
		"PATH=/poison",
	})
	joined := "\n" + strings.Join(got, "\n")
	for _, forbidden := range []string{"PORT_GENERATE=", "PORTGEN_GENERATE=", "COREGEN_GENERATE=", "PORTGEN_SIDECAR_ORACLE_COMMIT=", "PORTGEN_SIDECAR_ORACLE_RELEASE=", "GOFLAGS=-modfile", "GIT_DIR=", "PATH=/poison"} {
		if strings.Contains(joined, "\n"+forbidden) {
			t.Fatalf("sanitizedCheckEnvironment() retained %q in %#v", forbidden, got)
		}
	}
	for _, required := range []string{"SAFE=retained", "GOWORK=off", "GOENV=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "PATH="} {
		if !strings.Contains(joined, "\n"+required) {
			t.Fatalf("sanitizedCheckEnvironment() omitted %q from %#v", required, got)
		}
	}
}

func TestRunFixtureChecksInjectsValidatedSidecarOracle(t *testing.T) {
	expected := inventory.Oracle{Commit: strings.Repeat("a", 40), Release: "validated-release"}
	t.Setenv(portgenSidecarOracleCommitEnv, strings.Repeat("b", 40))
	t.Setenv(portgenSidecarOracleReleaseEnv, "caller-controlled")

	originalRunner := runFixtureCheckTarget
	t.Cleanup(func() { runFixtureCheckTarget = originalRunner })
	var captured []string
	runFixtureCheckTarget = func(_ string, _ string, environment []string, target fixtureCheckTarget) error {
		if target.sidecarOracle {
			captured = append([]string(nil), environment...)
		}
		return nil
	}

	if err := runFixtureChecks(t.TempDir(), expected); err != nil {
		t.Fatalf("runFixtureChecks() error = %v", err)
	}
	joined := "\n" + strings.Join(captured, "\n")
	for _, required := range []string{
		portgenSidecarOracleCommitEnv + "=" + expected.Commit,
		portgenSidecarOracleReleaseEnv + "=" + expected.Release,
	} {
		if strings.Count(joined, "\n"+required) != 1 {
			t.Fatalf("sidecar environment = %#v, want exactly one %q", captured, required)
		}
	}
	for _, forbidden := range []string{
		portgenSidecarOracleCommitEnv + "=" + strings.Repeat("b", 40),
		portgenSidecarOracleReleaseEnv + "=caller-controlled",
	} {
		if strings.Contains(joined, "\n"+forbidden) {
			t.Fatalf("sidecar environment retained caller input %q: %#v", forbidden, captured)
		}
	}
}

func TestImmutableSourceSnapshotExcludesLiveIgnoredInputs(t *testing.T) {
	repoRoot := newProvenanceBaseRepository(t)
	writePortgenTestFile(t, repoRoot, ".gitignore", "ignored.go\n")
	portgenGit(t, repoRoot, "add", "--", ".gitignore")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: ignore live generator poison")
	writePortgenTestFile(t, repoRoot, "ignored.go", "package ignored\n")

	snapshot, cleanup, err := createImmutableSourceSnapshot(repoRoot, "HEAD")
	if err != nil {
		t.Fatalf("createImmutableSourceSnapshot() error = %v", err)
	}
	t.Cleanup(cleanup)

	if _, err := os.Stat(filepath.Join(snapshot, "go.mod")); err != nil {
		t.Fatalf("snapshot omitted tracked file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "ignored.go")); !os.IsNotExist(err) {
		t.Fatalf("snapshot included ignored live input: stat error = %v", err)
	}
}

func newProvenanceCommitRepository(t *testing.T, extra map[string]string) (string, string) {
	t.Helper()
	repoRoot := newProvenanceBaseRepository(t)
	oracle := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
	writePortgenTestFile(t, repoRoot, provenanceFixture, "{}\n")
	for rel, content := range extra {
		writePortgenTestFile(t, repoRoot, rel, content)
	}
	portgenGit(t, repoRoot, "add", "--", ".")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: provenance-only Q")
	return repoRoot, oracle
}

func newProvenanceBaseRepository(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	portgenGit(t, repoRoot, "init", "-q")
	writePortgenTestFile(t, repoRoot, "go.mod", "module example.test/portgen\n\ngo 1.26.6\n")
	writePortgenTestFile(t, repoRoot, "cmd/tool/main.go", "package main\n")
	writePortgenTestFile(t, repoRoot, "internal/core/core.go", "package core\n")
	writePortgenTestFile(t, repoRoot, "internal/sidecar/port_lifecycle_contract_test.go", "package sidecar\n")
	writePortgenTestFile(t, repoRoot, "crates/symdesk-index/src/contract_tests.rs", "mod contract_tests {}\n")
	writePortgenTestFile(t, repoRoot, "scripts/rust-port/placeholder.go", "package rustport\n")
	writePortgenTestFile(t, repoRoot, "Makefile", "all:\n\t@true\n")
	portgenGit(t, repoRoot, "add", "--", ".")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: functional P")
	return repoRoot
}

func writePortgenTestFile(t *testing.T, repoRoot, rel, content string) {
	t.Helper()
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func portgenGit(t *testing.T, repoRoot string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Portgen Test", "-c", "user.email=portgen-test@example.invalid"}, args...)...)
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func portgenGitOutput(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
