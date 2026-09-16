package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func runProvenanceCheck(repoRoot string) error {
	if err := verifyCleanWorktree(repoRoot); err != nil {
		return err
	}
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve checked provenance revision: %w", err)
	}
	provData, err := gitOutput(repoRoot, "show", head+":"+provenanceFixture)
	if err != nil {
		return fmt.Errorf("read %s from checked tree: %w", provenanceFixture, err)
	}
	var prov inventory.ProvenanceDocument
	if err := json.Unmarshal(provData, &prov); err != nil {
		return fmt.Errorf("unmarshal %s: %w", provenanceFixture, err)
	}
	if prov.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema version %d in %s", prov.SchemaVersion, provenanceFixture)
	}
	if err := verifyProvenanceCommitAt(repoRoot, head, prov.Oracle.Commit); err != nil {
		return fmt.Errorf("verify P-to-Q provenance relationship: %w", err)
	}

	headProduction, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, head)
	if err != nil {
		return fmt.Errorf("compute checked production source digest: %w", err)
	}
	oracleProduction, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, prov.Oracle.Commit)
	if err != nil {
		return fmt.Errorf("compute oracle production source digest: %w", err)
	}
	if headProduction != prov.ProductionSourceDigest {
		return fmt.Errorf("production source drift detected: checked=%s recorded=%s", headProduction, prov.ProductionSourceDigest)
	}
	if oracleProduction != prov.ProductionSourceDigest {
		return fmt.Errorf("recorded source digest is not the bytes at oracle commit %s: oracle=%s recorded=%s", prov.Oracle.Commit, oracleProduction, prov.ProductionSourceDigest)
	}

	headGenerator, err := inventory.ComputeGitRevisionGeneratorSourceDigest(repoRoot, head)
	if err != nil {
		return fmt.Errorf("compute checked generator source digest: %w", err)
	}
	oracleGenerator, err := inventory.ComputeGitRevisionGeneratorSourceDigest(repoRoot, prov.Oracle.Commit)
	if err != nil {
		return fmt.Errorf("compute oracle generator source digest: %w", err)
	}
	if headGenerator != prov.GeneratorSourceDigest {
		return fmt.Errorf("fixture generator drift detected: checked=%s recorded=%s", headGenerator, prov.GeneratorSourceDigest)
	}
	if oracleGenerator != prov.GeneratorSourceDigest {
		return fmt.Errorf("recorded generator digest is not the bytes at oracle commit %s: oracle=%s recorded=%s", prov.Oracle.Commit, oracleGenerator, prov.GeneratorSourceDigest)
	}

	for _, rel := range fixturePaths {
		expected, ok := prov.FixtureChecksums[rel]
		if !ok {
			return fmt.Errorf("fixture %s missing from provenance checksums", rel)
		}
		actual, err := gitRevisionChecksum(repoRoot, head, rel)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("fixture %s checksum mismatch (expected %s, got %s); regenerate from committed P and commit only derived outputs in Q", rel, expected, actual)
		}
	}

	snapshot, cleanup, err := createImmutableSourceSnapshot(repoRoot, head)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := runFixturePackageChecks(snapshot); err != nil {
		return fmt.Errorf("fixture drift in immutable checked source snapshot: %w", err)
	}
	if err := verifyCleanWorktree(repoRoot); err != nil {
		return fmt.Errorf("worktree changed during immutable provenance check: %w", err)
	}
	finalHead, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve final provenance revision: %w", err)
	}
	if finalHead != head {
		return fmt.Errorf("checked provenance revision changed during verification: started=%s finished=%s", head, finalHead)
	}
	return nil
}

func gitRevisionChecksum(repoRoot, revision, rel string) (string, error) {
	content, err := gitOutput(repoRoot, "show", revision+":"+rel)
	if err != nil {
		return "", fmt.Errorf("read fixture %s at %s: %w", rel, revision, err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}
