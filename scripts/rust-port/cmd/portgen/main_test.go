package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestDeterministicDigestComputation(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	d1, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("production source digest is not deterministic: %s != %s", d1, d2)
	}
}

func TestProvenanceVerificationPasses(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	provPath := filepath.Join(repoRoot, provenanceFixture)
	provData, err := os.ReadFile(provPath)
	if err != nil {
		t.Fatal(err)
	}
	var prov inventory.ProvenanceDocument
	if err := json.Unmarshal(provData, &prov); err != nil {
		t.Fatal(err)
	}

	if prov.Oracle.Commit != defaultOracleCommit {
		t.Fatalf("oracle commit mismatch: %s != %s", prov.Oracle.Commit, defaultOracleCommit)
	}
	if prov.Oracle.Release != defaultOracleRelease {
		t.Fatalf("oracle release mismatch: %s != %s", prov.Oracle.Release, defaultOracleRelease)
	}

	currentDigest, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if currentDigest != prov.ProductionSourceDigest {
		t.Fatalf("production source digest mismatch: %s != %s", currentDigest, prov.ProductionSourceDigest)
	}
	revisionDigest, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, prov.Oracle.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if revisionDigest != prov.ProductionSourceDigest {
		t.Fatalf("oracle revision digest mismatch: %s != %s", revisionDigest, prov.ProductionSourceDigest)
	}
	generatorDigest, err := inventory.ComputeGeneratorSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if generatorDigest != prov.GeneratorSourceDigest {
		t.Fatalf("fixture generator digest mismatch: %s != %s", generatorDigest, prov.GeneratorSourceDigest)
	}

	for _, rel := range fixturePaths {
		path := filepath.Join(repoRoot, rel)
		sum, err := inventory.ComputeFileChecksum(path)
		if err != nil {
			t.Fatal(err)
		}
		if sum != prov.FixtureChecksums[rel] {
			t.Fatalf("checksum mismatch for %s: %s != %s", rel, sum, prov.FixtureChecksums[rel])
		}
	}
}

func TestStaleFixtureMismatchDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := inventory.ComputeFileChecksum(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mutated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := inventory.ComputeFileChecksum(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("fixture checksum failed to detect a content mutation")
	}
}

func TestProductionSourceDriftDetected(t *testing.T) {
	repoRoot := t.TempDir()
	for _, dir := range []string{"cmd/tool", "internal/core", ".github/workflows", "home-assistant-addon/symdesk"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"cmd/tool/main.go":              "package main\n",
		"internal/core/data.sql":        "CREATE TABLE x (id INTEGER);\n",
		"go.mod":                        "module example.test/oracle\n",
		"go.sum":                        "",
		".goreleaser.yml":               "version: 2\n",
		"Dockerfile":                    "FROM scratch\n",
		"VAULT.md":                      "# contract\n",
		".github/workflows/release.yml": "name: release\n",
		"home-assistant-addon/symdesk/config.yaml": "version: 1\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(repoRoot, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "--", "."}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	before, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "internal/core/data.sql"), []byte("CREATE TABLE y (id INTEGER);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("production source digest failed to detect embedded SQL drift")
	}
}
