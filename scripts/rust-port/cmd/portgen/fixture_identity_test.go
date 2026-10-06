package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestFixtureIdentityModelRejectsMixedLiveSources(t *testing.T) {
	p := inventory.Oracle{Commit: strings.Repeat("a", 40), Release: "live-release"}
	h := inventory.Oracle{Commit: strings.Repeat("b", 40), Release: "historical-release"}
	for _, header := range []string{"oracle", "oracle_commit", "oracle_revision"} {
		t.Run(header, func(t *testing.T) {
			for _, selected := range []inventory.Oracle{p, h} {
				value := any(selected.Commit)
				if header == "oracle" {
					value = selected
				}
				content, err := json.Marshal(map[string]any{header: value})
				if err != nil {
					t.Fatal(err)
				}
				_, err = validateFixtureIdentityDocument("testdata/port/core/config.json", content, p)
				if (err == nil) != (selected == p) {
					t.Fatalf("identity %s validation = %v", selected.Commit, err)
				}
			}
		})
	}
	for _, content := range []string{
		`{"oracle":{"commit":"` + p.Commit + `","release":"invented-release"}}`,
		`{"oracle_commit":"` + p.Commit + `","oracle_revision":"` + h.Commit + `"}`,
		`{"oracle":"unknown production API"}`,
		`{"oracle_commit":"HEAD"}`,
		`{"oracle_commit":"` + strings.ToUpper(p.Commit) + `"}`,
		`{"oracle":null}`,
	} {
		if _, err := validateFixtureIdentityDocument("testdata/port/core/config.json", []byte(content), p); err == nil {
			t.Fatalf("accepted invalid identity: %s", content)
		}
	}
	for _, rel := range []string{"testdata/port/vault/base-view-write.json", "testdata/port/vault/notebook-write.json"} {
		content, err := json.Marshal(map[string]string{"oracle": descriptiveFixtureOracle(rel)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validateFixtureIdentityDocument(rel, content, p); err != nil {
			t.Fatalf("declared API description rejected: %v", err)
		}
		if _, err := validateFixtureIdentityDocument("testdata/port/new.json", content, p); err == nil {
			t.Fatal("an undeclared descriptive role bypassed source validation")
		}
	}
}

func TestFixtureIdentityGateReadsCommittedSourcesAndVerifiesHistoricalAncestry(t *testing.T) {
	root := newProvenanceBaseRepository(t)
	ancestor := portgenGitOutput(t, root, "rev-parse", "HEAD")
	writePortgenTestFile(t, root, "internal/core/core.go", "package core\n// source P2\n")
	portgenGit(t, root, "add", "--", "internal/core/core.go")
	portgenGit(t, root, "commit", "-q", "-m", "test: replacement production P")
	live := inventory.Oracle{Commit: portgenGitOutput(t, root, "rev-parse", "HEAD"), Release: "live-release"}
	writeIdentity := func(rel, commit string) {
		t.Helper()
		writePortgenTestFile(t, root, rel, `{"oracle":{"commit":"`+commit+`","release":"live-release"}}`+"\n")
	}
	const observed = "testdata/port/core/config.json"
	const historical = "testdata/port/cli/cases.json"
	writeIdentity(observed, live.Commit)
	writeIdentity(historical, ancestor)
	portgenGit(t, root, "add", "--", "testdata/port")
	portgenGit(t, root, "commit", "-q", "-m", "test: explicit P observations and historical H input")
	if err := verifyFixtureIdentities(root, portgenGitOutput(t, root, "rev-parse", "HEAD"), live); err != nil {
		t.Fatalf("coherent P/H model rejected: %v", err)
	}
	writeIdentity(observed, ancestor)
	portgenGit(t, root, "add", "--", observed)
	portgenGit(t, root, "commit", "-q", "-m", "test: unexplained mixed source")
	writeIdentity(observed, live.Commit) // Dirty correction cannot hide the committed error.
	if err := verifyFixtureIdentities(root, portgenGitOutput(t, root, "rev-parse", "HEAD"), live); err == nil || !strings.Contains(err.Error(), "canonical P") {
		t.Fatalf("committed mixed identity escaped verification: %v", err)
	}
	portgenGit(t, root, "add", "--", observed)
	portgenGit(t, root, "commit", "-q", "-m", "test: restore canonical P")
	writeIdentity(historical, strings.Repeat("f", 40))
	portgenGit(t, root, "add", "--", historical)
	portgenGit(t, root, "commit", "-q", "-m", "test: nonexistent historical H")
	if err := verifyFixtureIdentities(root, portgenGitOutput(t, root, "rev-parse", "HEAD"), live); err == nil || !strings.Contains(err.Error(), "historical fixture identity") {
		t.Fatalf("historical role bypassed actual commit ancestry: %v", err)
	}
}

func TestGenerationRejectsTwoLivePinsBeforeExecutingGenerators(t *testing.T) {
	root := newProvenanceBaseRepository(t)
	p := portgenGitOutput(t, root, "rev-parse", "HEAD")
	writePortgenTestFile(t, root, "docs/change.md", "advance without changing production\n")
	portgenGit(t, root, "add", "--", "docs/change.md")
	portgenGit(t, root, "commit", "-q", "-m", "test: distinct equivalent source revision")
	head := portgenGitOutput(t, root, "rev-parse", "HEAD")
	var output strings.Builder
	if err := generateArtifact(root, p, "release", head, &output); err == nil || !strings.Contains(err.Error(), "one source P") {
		t.Fatalf("two equivalent-but-distinct live pins were accepted: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("rejected identity mix emitted an artifact")
	}
}
