package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestFixtureCheckRegistryCoversProvenanceManifest(t *testing.T) {
	if err := validateFixtureCheckCoverage(); err != nil {
		t.Fatalf("validateFixtureCheckCoverage() error = %v", err)
	}
}

func TestDecodeCanonicalProvenanceRejectsUnknownAndDuplicateFields(t *testing.T) {
	document := testProvenanceDocument()
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if _, err := decodeCanonicalProvenance(content); err != nil {
		t.Fatalf("decodeCanonicalProvenance() rejected canonical document: %v", err)
	}

	unknown := append([]byte(nil), content[:len(content)-2]...)
	unknown = append(unknown, []byte(",\n  \"unexpected\": true\n}\n")...)
	if _, err := decodeCanonicalProvenance(unknown); err == nil {
		t.Fatal("decodeCanonicalProvenance() accepted an unknown field")
	}
	duplicate := []byte(`{"schema_version":1,"schema_version":1}` + "\n")
	if _, err := decodeCanonicalProvenance(duplicate); err == nil {
		t.Fatal("decodeCanonicalProvenance() accepted duplicate noncanonical fields")
	}
}

func TestValidateProvenanceDocumentRejectsExtraChecksum(t *testing.T) {
	document := testProvenanceDocument()
	if err := validateProvenanceDocument(document); err != nil {
		t.Fatalf("validateProvenanceDocument() rejected valid document: %v", err)
	}
	document.FixtureChecksums["testdata/port/unreviewed.json"] = strings.Repeat("a", 64)
	if err := validateProvenanceDocument(document); err == nil {
		t.Fatal("validateProvenanceDocument() accepted an extra checksum path")
	}
}

func TestVerifyPortFixtureTreeEntriesRejectsNonRegularOutput(t *testing.T) {
	repoRoot := newProvenanceBaseRepository(t)
	bad := fixturePaths[0]
	for _, rel := range fixturePaths {
		if rel != bad {
			writePortgenTestFile(t, repoRoot, rel, "{}\n")
		}
	}
	writePortgenTestFile(t, repoRoot, provenanceFixture, "{}\n")
	payload := filepath.Join(repoRoot, "symlink-payload")
	if err := os.WriteFile(payload, []byte("outside-target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blob := portgenGitOutput(t, repoRoot, "hash-object", "-w", "symlink-payload")
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	portgenGit(t, repoRoot, "add", "--", "testdata/port")
	portgenGit(t, repoRoot, "update-index", "--add", "--cacheinfo", "120000,"+blob+","+bad)
	portgenGit(t, repoRoot, "commit", "-q", "-m", "test: nonregular fixture entry")

	if err := verifyPortFixtureTreeEntries(repoRoot, "HEAD"); err == nil || !strings.Contains(err.Error(), "regular 100644 blob") {
		t.Fatalf("verifyPortFixtureTreeEntries() error = %v, want nonregular-entry failure", err)
	}
}

func TestFixtureCheckRegistryValidatesCurrentTree(t *testing.T) {
	if os.Getenv("PORTGEN_INTEGRATION") != "1" {
		t.Skip("set PORTGEN_INTEGRATION=1 to run all immutable fixture checks")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // test fixture path derived from the manifest constant
	content, err := os.ReadFile(filepath.Join(repoRoot, provenanceFixture))
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := decodeCanonicalProvenance(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := runFixtureChecks(repoRoot, provenance.Oracle); err != nil {
		t.Fatalf("runFixtureChecks() error = %v", err)
	}
}

func testProvenanceDocument() inventory.ProvenanceDocument {
	checksums := make(map[string]string, len(fixturePaths))
	for _, rel := range fixturePaths {
		checksums[rel] = strings.Repeat("a", 64)
	}
	return inventory.ProvenanceDocument{
		SchemaVersion:          1,
		Oracle:                 inventory.Oracle{Commit: strings.Repeat("a", 40), Release: "test-release"},
		ProductionSourceDigest: strings.Repeat("b", 64),
		GeneratorSourceDigest:  strings.Repeat("c", 64),
		SurfaceCounts:          expectedSurfaceCounts(),
		FixtureChecksums:       checksums,
	}
}
