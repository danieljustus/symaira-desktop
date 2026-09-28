package inventory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryGitOutputTrustsOnlyCheckout(t *testing.T) {
	repoRoot := t.TempDir()
	runGeneratorDigestGit(t, repoRoot, "init", "-q")
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[safe]\n	directory = *\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	output, err := inventoryGitOutput(repoRoot, "config", "--get-all", "safe.directory")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(output)), filepath.ToSlash(repoRoot); got != want {
		t.Fatalf("unexpected trusted directories: got %q, want %q", got, want)
	}
}

func TestGeneratorDigestIncludesMakefileAndCanReadImmutableRevision(t *testing.T) {
	repoRoot := t.TempDir()
	runGeneratorDigestGit(t, repoRoot, "init", "-q")
	writeGeneratorDigestFile(t, repoRoot, "Makefile", "port-fixtures-check:\n\t@true\n")
	writeGeneratorDigestFile(t, repoRoot, "scripts/rust-port/cmd/example.go", "package cmd\n")
	runGeneratorDigestGit(t, repoRoot, "add", "--", ".")
	runGeneratorDigestGit(t, repoRoot, "commit", "-q", "-m", "test: generator P")
	revision := generatorDigestGitOutput(t, repoRoot, "rev-parse", "HEAD")

	immutableBefore, err := ComputeGitRevisionGeneratorSourceDigest(repoRoot, revision)
	if err != nil {
		t.Fatal(err)
	}
	workingBefore, err := ComputeGeneratorSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if immutableBefore != workingBefore {
		t.Fatalf("immutable and working generator digests differ before mutation: %s != %s", immutableBefore, workingBefore)
	}

	writeGeneratorDigestFile(t, repoRoot, "Makefile", "port-fixtures-check:\n\t@false\n")
	workingAfter, err := ComputeGeneratorSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if workingAfter == workingBefore {
		t.Fatal("generator digest did not change after Makefile mutation")
	}
	immutableAfter, err := ComputeGitRevisionGeneratorSourceDigest(repoRoot, revision)
	if err != nil {
		t.Fatal(err)
	}
	if immutableAfter != immutableBefore {
		t.Fatalf("revision digest changed after working-tree mutation: %s != %s", immutableAfter, immutableBefore)
	}
}

func TestGeneratorDigestIncludesPackageLocalFixtureGenerators(t *testing.T) {
	for _, relative := range []string{
		"internal/service/port_dataset_contract_test.go",
		"internal/service/port_retention_state_contract_test.go",
		"internal/service/port_noteops_contract_test.go",
		"internal/vault/port_writefs_contract_test.go",
		"internal/sidecar/port_metadata_contract_test.go",
		"internal/history/port_lifecycle_contract_test.go",
		"internal/retention/port_retention_contract_test.go",
		"internal/retention/port_retention_rules_contract_test.go",
	} {
		t.Run(relative, func(t *testing.T) {
			repoRoot := t.TempDir()
			runGeneratorDigestGit(t, repoRoot, "init", "-q")
			writeGeneratorDigestFile(t, repoRoot, "Makefile", "port-fixtures-check:\n\t@true\n")
			writeGeneratorDigestFile(t, repoRoot, relative, "package service\n// original oracle\n")
			runGeneratorDigestGit(t, repoRoot, "add", "--", ".")
			runGeneratorDigestGit(t, repoRoot, "commit", "-q", "-m", "test: fixture generator")
			revision := generatorDigestGitOutput(t, repoRoot, "rev-parse", "HEAD")
			before, err := ComputeGitRevisionGeneratorSourceDigest(repoRoot, revision)
			if err != nil {
				t.Fatal(err)
			}
			writeGeneratorDigestFile(t, repoRoot, relative, "package service\n// mutated oracle\n")
			after, err := ComputeGeneratorSourceDigest(repoRoot)
			if err != nil {
				t.Fatal(err)
			}
			if after == before {
				t.Fatalf("generator-only mutation was not detected: %s", relative)
			}
			immutable, err := ComputeGitRevisionGeneratorSourceDigest(repoRoot, revision)
			if err != nil {
				t.Fatal(err)
			}
			if immutable != before {
				t.Fatal("working-tree mutation changed immutable generator identity")
			}
		})
	}
}

func writeGeneratorDigestFile(t *testing.T, repoRoot, rel, content string) {
	t.Helper()
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGeneratorDigestGit(t *testing.T, repoRoot string, args ...string) {
	t.Helper()
	//nolint:gosec // fixed git subcommands in a test fixture
	command := exec.Command("git", append([]string{"-c", "user.name=Digest Test", "-c", "user.email=digest-test@example.invalid"}, args...)...)
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func generatorDigestGitOutput(t *testing.T, repoRoot string, args ...string) string {
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
