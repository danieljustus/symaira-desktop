package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

var sha256LowerHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func runProvenanceCheck(repoRoot string) error {
	if err := verifyCleanWorktree(repoRoot); err != nil {
		return err
	}
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve checked provenance revision: %w", err)
	}
	if err := verifyPortFixtureTreeEntries(repoRoot, head); err != nil {
		return fmt.Errorf("verify checked provenance tree entries: %w", err)
	}
	provData, err := gitOutput(repoRoot, "show", head+":"+provenanceFixture)
	if err != nil {
		return fmt.Errorf("read %s from checked tree: %w", provenanceFixture, err)
	}
	prov, err := decodeCanonicalProvenance(provData)
	if err != nil {
		return fmt.Errorf("decode %s: %w", provenanceFixture, err)
	}
	if err := validateProvenanceDocument(prov); err != nil {
		return fmt.Errorf("validate %s: %w", provenanceFixture, err)
	}
	if err := verifyProvenanceCommitAt(repoRoot, head, prov.Oracle.Commit); err != nil {
		return fmt.Errorf("verify P-to-Q provenance relationship: %w", err)
	}
	if err := verifyPortFixtureTreeEntries(repoRoot, prov.Oracle.Commit); err != nil {
		return fmt.Errorf("verify oracle provenance tree entries: %w", err)
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
	if err := runFixtureChecks(snapshot, prov.Oracle); err != nil {
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

func decodeCanonicalProvenance(content []byte) (inventory.ProvenanceDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document inventory.ProvenanceDocument
	if err := decoder.Decode(&document); err != nil {
		return inventory.ProvenanceDocument{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return inventory.ProvenanceDocument{}, fmt.Errorf("contains multiple JSON values")
		}
		return inventory.ProvenanceDocument{}, fmt.Errorf("read trailing JSON content: %w", err)
	}
	canonical, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return inventory.ProvenanceDocument{}, fmt.Errorf("canonicalize document: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(content, canonical) {
		return inventory.ProvenanceDocument{}, fmt.Errorf("is not canonical generated JSON")
	}
	return document, nil
}

func validateProvenanceDocument(document inventory.ProvenanceDocument) error {
	if document.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema version %d", document.SchemaVersion)
	}
	if !fullGitCommit.MatchString(document.Oracle.Commit) {
		return fmt.Errorf("oracle commit must be a lowercase full 40-character SHA")
	}
	if document.Oracle.Release == "" {
		return fmt.Errorf("oracle release must not be empty")
	}
	if document.SurfaceCounts != expectedSurfaceCounts() {
		return fmt.Errorf("surface counts do not match the checked fixture contract")
	}
	if !sha256LowerHex.MatchString(document.ProductionSourceDigest) {
		return fmt.Errorf("production source digest must be lowercase SHA-256 hex")
	}
	if !sha256LowerHex.MatchString(document.GeneratorSourceDigest) {
		return fmt.Errorf("generator source digest must be lowercase SHA-256 hex")
	}
	if len(document.FixtureChecksums) != len(fixturePaths) {
		return fmt.Errorf("fixture checksum set has %d entries, want %d", len(document.FixtureChecksums), len(fixturePaths))
	}
	for _, rel := range fixturePaths {
		checksum, ok := document.FixtureChecksums[rel]
		if !ok {
			return fmt.Errorf("fixture %s missing from checksum set", rel)
		}
		if !sha256LowerHex.MatchString(checksum) {
			return fmt.Errorf("fixture %s has a noncanonical checksum", rel)
		}
	}
	for rel := range document.FixtureChecksums {
		if !isPortDerivedOutput(rel) {
			return fmt.Errorf("checksum set contains path outside the fixture manifest: %s", rel)
		}
	}
	return nil
}

func expectedSurfaceCounts() inventory.SurfaceCounts {
	return inventory.SurfaceCounts{
		SymdeskTotalCommands:   207,
		SymdeskNonRootCommands: 206,
		SymroomSubcommands:     16,
		SymdeskMCPTools:        57,
		SymroomMCPTools:        8,
		SelfhostHTTPRoutes:     21,
	}
}
