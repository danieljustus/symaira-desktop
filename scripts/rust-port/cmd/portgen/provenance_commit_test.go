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

func TestVerifyProvenanceAncestry(t *testing.T) {
	t.Run("accepts the checked revision itself", func(t *testing.T) {
		repoRoot := newProvenanceBaseRepository(t)
		head := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
		if err := verifyProvenanceAncestry(repoRoot, head); err != nil {
			t.Fatalf("verifyProvenanceAncestry() error = %v", err)
		}
	})

	t.Run("accepts an ancestor oracle", func(t *testing.T) {
		repoRoot := newProvenanceRecordRepository(t)
		oracle := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD~1")
		writePortgenTestFile(t, repoRoot, provenanceFixture, "{\"schema_version\": 1}\n")
		portgenGit(t, repoRoot, "add", "--", provenanceFixture)
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: later provenance record")
		if err := verifyProvenanceAncestry(repoRoot, oracle); err != nil {
			t.Fatalf("verifyProvenanceAncestry() error = %v", err)
		}
	})

	t.Run("accepts squash-merge shaped history", func(t *testing.T) {
		// Regression guard for the shape this repository actually produces: the
		// functional commit and the provenance commit are squashed into one
		// commit whose parent is unrelated to the record. Ancestry, not a direct
		// parent relation, is what the fail-closed digests rely on.
		repoRoot := newProvenanceRecordRepository(t)
		oracle := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD~1")
		writePortgenTestFile(t, repoRoot, "docs/rust-port/README.md", "# squash-merged\n")
		portgenGit(t, repoRoot, "add", "--", "docs/rust-port/README.md")
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: squash-merged change")
		if err := verifyProvenanceAncestry(repoRoot, oracle); err != nil {
			t.Fatalf("verifyProvenanceAncestry() error = %v, want squash-merge history accepted", err)
		}
	})

	t.Run("rejects a malformed oracle", func(t *testing.T) {
		repoRoot := newProvenanceRecordRepository(t)
		err := verifyProvenanceAncestry(repoRoot, "not-a-sha")
		if err == nil || !strings.Contains(err.Error(), "lowercase full 40-character SHA") {
			t.Fatalf("verifyProvenanceAncestry() error = %v, want malformed-SHA failure", err)
		}
	})

	t.Run("rejects a commit that is not in the checked history", func(t *testing.T) {
		repoRoot := newProvenanceRecordRepository(t)
		err := verifyProvenanceAncestry(repoRoot, strings.Repeat("a", 40))
		if err == nil || !strings.Contains(err.Error(), "neither the checked revision") {
			t.Fatalf("verifyProvenanceAncestry() error = %v, want non-ancestor failure", err)
		}
	})

	t.Run("rejects a valid side-branch commit", func(t *testing.T) {
		repoRoot := newProvenanceRecordRepository(t)
		mainBranch := portgenGitOutput(t, repoRoot, "branch", "--show-current")
		head := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
		portgenGit(t, repoRoot, "checkout", "-q", "-b", "side", head)
		writePortgenTestFile(t, repoRoot, "side-branch.txt", "not an ancestor of the checked revision\n")
		portgenGit(t, repoRoot, "add", "--", "side-branch.txt")
		portgenGit(t, repoRoot, "commit", "-q", "-m", "test: side branch")
		side := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
		portgenGit(t, repoRoot, "checkout", "-q", mainBranch)

		err := verifyProvenanceAncestry(repoRoot, side)
		if err == nil || !strings.Contains(err.Error(), "neither the checked revision") {
			t.Fatalf("verifyProvenanceAncestry() error = %v, want non-ancestor failure", err)
		}
	})
}

func TestRunProvenanceCheckAcceptsAncestorOracle(t *testing.T) {
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
	got, err = resolveGenerationOracleCommit(repoRoot, want)
	if err != nil {
		t.Fatalf("resolveGenerationOracleCommit(ancestor) error = %v, want the ancestor accepted", err)
	}
	if got != want {
		t.Fatalf("resolveGenerationOracleCommit(ancestor) = %s, want %s", got, want)
	}

	portgenGit(t, repoRoot, "checkout", "-q", "-b", "side", "HEAD~1")
	writePortgenTestFile(t, repoRoot, "side.txt", "independent side commit\n")
	portgenGit(t, repoRoot, "add", "--", "side.txt")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: side commit")
	side := portgenGitOutput(t, repoRoot, "rev-parse", "HEAD")
	portgenGit(t, repoRoot, "checkout", "-q", "-")
	if _, err := resolveGenerationOracleCommit(repoRoot, side); err == nil {
		t.Fatal("resolveGenerationOracleCommit() accepted an oracle outside the checked history")
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
		"PORT_DATASET_IMPORT_FIXTURE=/poison",
		"PORT_FIXTURE_PATH=/poison",
		"port_fixture_path=/poison-lower",
		"PORTGEN_SIDECAR_ORACLE_COMMIT=poison",
		"PORTGEN_SIDECAR_ORACLE_RELEASE=poison",
		"GOFLAGS=-modfile=poison.mod",
		"GOCACHE=/safe/build-cache",
		"GIT_DIR=/poison",
		"PATH=/poison",
	}, filepath.Join(t.TempDir(), "empty-gitconfig"))
	joined := "\n" + strings.Join(got, "\n")
	for _, forbidden := range []string{"PORT_GENERATE=", "PORTGEN_GENERATE=", "COREGEN_GENERATE=", "PORT_DATASET_IMPORT_FIXTURE=", "PORT_FIXTURE_PATH=", "port_fixture_path=", "PORTGEN_SIDECAR_ORACLE_COMMIT=", "PORTGEN_SIDECAR_ORACLE_RELEASE=", "GOFLAGS=-modfile", "GIT_DIR=", "PATH=/poison"} {
		if strings.Contains(joined, "\n"+forbidden) {
			t.Fatalf("sanitizedCheckEnvironment() retained %q in %#v", forbidden, got)
		}
	}
	for _, required := range []string{"SAFE=retained", "GOCACHE=/safe/build-cache", "GOWORK=off", "GOENV=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "PATH="} {
		if !strings.Contains(joined, "\n"+required) {
			t.Fatalf("sanitizedCheckEnvironment() omitted %q from %#v", required, got)
		}
	}
}

func TestSanitizedCheckEnvironmentKeepsBothHomeNames(t *testing.T) {
	for _, tc := range []struct {
		name      string
		base      []string
		required  string
		unchanged bool
	}{
		{name: "posix-only home gains the Windows name", base: []string{"HOME=/home/runner"}, required: "USERPROFILE=/home/runner"},
		{name: "windows-only home gains the POSIX name", base: []string{"USERPROFILE=C:\\Users\\runner"}, required: "HOME=C:\\Users\\runner"},
		{name: "both names present stay untouched", base: []string{"HOME=/home/a", "USERPROFILE=C:\\b"}, required: "HOME=/home/a", unchanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizedCheckEnvironment(tc.base, filepath.Join(t.TempDir(), "empty-gitconfig"))
			joined := "\n" + strings.Join(got, "\n")
			if !strings.Contains(joined, "\n"+tc.required) {
				t.Fatalf("sanitizedCheckEnvironment(%#v) omitted %q: %#v", tc.base, tc.required, got)
			}
			if tc.unchanged {
				counts := map[string]int{}
				for _, item := range got {
					name, _, _ := strings.Cut(item, "=")
					counts[strings.ToUpper(name)]++
				}
				for _, name := range []string{"HOME", "USERPROFILE"} {
					if counts[name] != 1 {
						t.Fatalf("sanitizedCheckEnvironment(%#v) duplicated %s: %#v", tc.base, name, got)
					}
				}
			}
		})
	}
}

func TestSanitizedCheckEnvironmentFallsBackToScratchHome(t *testing.T) {
	got := sanitizedCheckEnvironment([]string{"SAFE=retained"}, filepath.Join(t.TempDir(), "empty-gitconfig"))
	homes := map[string]string{}
	for _, item := range got {
		name, value, _ := strings.Cut(item, "=")
		if name == "HOME" || name == "USERPROFILE" {
			homes[name] = value
		}
	}
	if len(homes) != 2 {
		t.Fatalf("sanitizedCheckEnvironment() resolved %d home names without any environment, want both: %#v", len(homes), got)
	}
	if homes["HOME"] != homes["USERPROFILE"] {
		t.Fatalf("sanitizedCheckEnvironment() disagreed on the scratch home: %#v", homes)
	}
	if info, err := os.Stat(homes["HOME"]); err != nil || !info.IsDir() {
		t.Fatalf("sanitizedCheckEnvironment() scratch home %q is not a directory: info=%v err=%v", homes["HOME"], info, err)
	}
	if !strings.HasPrefix(homes["HOME"], os.TempDir()) {
		t.Fatalf("sanitizedCheckEnvironment() scratch home %q is outside the temporary directory", homes["HOME"])
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

// newProvenanceRecordRepository builds a repository whose HEAD records the port
// fixtures. HEAD~1 is the functional commit the record describes.
func newProvenanceRecordRepository(t *testing.T) string {
	t.Helper()
	repoRoot := newProvenanceBaseRepository(t)
	writePortgenTestFile(t, repoRoot, provenanceFixture, "{}\n")
	portgenGit(t, repoRoot, "add", "--", ".")
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: provenance record")
	return repoRoot
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
	//nolint:gosec // fixed git subcommands in a test fixture
	command := exec.Command("git", append([]string{"-c", "user.name=Portgen Test", "-c", "user.email=portgen-test@example.invalid"}, args...)...)
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func portgenGitOutput(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	//nolint:gosec // fixed git subcommands in a test fixture
	command := exec.Command("git", args...)
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
