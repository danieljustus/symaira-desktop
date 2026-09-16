// Command portgen coordinates generation and drift-checking of language-neutral Go oracle fixtures.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOracleRelease = "post-v0.12.2-security-880"
	provenanceFixture    = "testdata/port/provenance.json"
)

var fixturePaths = []string{
	"testdata/port/cli/cases.json",
	"testdata/port/cli/symdesk-command-tree.json",
	"testdata/port/cli/symroom-parser-grammar.json",
	"testdata/port/core/config.json",
	"testdata/port/core/document-formats.json",
	"testdata/port/core/german-search.json",
	"testdata/port/core/search-query.json",
	"testdata/port/core/simhash.json",
	"testdata/port/core/textnorm.json",
	"testdata/port/mcp/symdesk-tools.json",
	"testdata/port/mcp/representative.json",
	"testdata/port/mcp/symroom-tools.json",
	"testdata/port/http/routes.json",
	"testdata/port/http/representative.json",
	"testdata/port/vault/filesystem.json",
	"testdata/port/vault/parse.json",
	"testdata/port/vault/resolution.json",
	"testdata/port/vault/health-links.json",
	"testdata/port/vault/typed.json",
	"testdata/port/vault/notebook.json",
	"testdata/port/vault/metadata.json",
	"testdata/port/vault/mobile-writer.json",
	"testdata/port/sidecar/contracts.json",
	"testdata/port/sidecar/lifecycle.json",
	"testdata/port/sidecar/roundtrip.json",
	"testdata/port/sidecar/large-corpus.json",
	"testdata/port/representative/cases.json",
}

func main() {
	check := flag.Bool("check", false, "fail if any fixture or oracle provenance has drifted")
	commit := flag.String("oracle-commit", "", "Go oracle commit (defaults to current HEAD during generation)")
	release := flag.String("oracle-release", defaultOracleRelease, "Go oracle release")
	flag.Parse()

	repoRoot, err := findRepoRoot()
	if err != nil {
		fatal("find repo root: %v", err)
	}

	if *check {
		runCheck(repoRoot)
		return
	}

	runGenerate(repoRoot, *commit, *release)
}

func runGenerate(repoRoot, commit, release string) {
	resolvedCommit, err := resolveGenerationOracleCommit(repoRoot, commit)
	if err != nil {
		fatal("resolve generation oracle: %v", err)
	}
	commit = resolvedCommit
	fmt.Printf("Generating Go oracle fixtures (oracle %s / %s)...\n", commit, release)

	// 1. Run package-local generators
	packages := []struct {
		pkg string
		run string
	}{
		{"./cmd/symdesk", "TestSymdeskCobraInventory"},
		{"./cmd/symroom", "TestSymRoomParserGrammar|TestSymRoomMCPInventory"},
		{"./internal/tools", "TestSymdeskMCPInventory"},
		{"./internal/selfhost", "TestSelfhostHTTPInventory"},
	}

	for _, target := range packages {
		//nolint:gosec // fixed generator targets, never derived from fixture output
		cmd := exec.Command("go", "test", "-count=1", target.pkg, "-run", target.run)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "PORT_GENERATE=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fatal("generate %s (%s): %v\noutput: %s", target.pkg, target.run, err, string(out))
		}
	}

	// 2. Compute provenance and checksums
	sourceDigest, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		fatal("compute production source digest: %v", err)
	}
	revisionDigest, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, commit)
	if err != nil {
		fatal("compute oracle revision source digest: %v", err)
	}
	if sourceDigest != revisionDigest {
		fatal("working-tree production source does not match oracle commit %s: current=%s oracle=%s", commit, sourceDigest, revisionDigest)
	}
	generatorDigest, err := inventory.ComputeGeneratorSourceDigest(repoRoot)
	if err != nil {
		fatal("compute fixture generator digest: %v", err)
	}

	checksums := make(map[string]string, len(fixturePaths))
	for _, rel := range fixturePaths {
		path := filepath.Join(repoRoot, rel)
		sum, err := inventory.ComputeFileChecksum(path)
		if err != nil {
			fatal("checksum %s: %v", rel, err)
		}
		checksums[rel] = sum
	}

	provenance := inventory.ProvenanceDocument{
		SchemaVersion: 1,
		Oracle: inventory.Oracle{
			Commit:  commit,
			Release: release,
		},
		ProductionSourceDigest: sourceDigest,
		GeneratorSourceDigest:  generatorDigest,
		SurfaceCounts: inventory.SurfaceCounts{
			SymdeskTotalCommands:   207,
			SymdeskNonRootCommands: 206,
			SymroomSubcommands:     16,
			SymdeskMCPTools:        57,
			SymroomMCPTools:        8,
			SelfhostHTTPRoutes:     21,
		},
		FixtureChecksums: checksums,
	}

	provContent, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		fatal("marshal provenance: %v", err)
	}
	provContent = append(provContent, '\n')

	provPath := filepath.Join(repoRoot, provenanceFixture)
	if err := os.WriteFile(provPath, provContent, 0o600); err != nil {
		fatal("write provenance: %v", err)
	}

	fmt.Printf("PASS generated all fixtures and provenance at %s\n", provenanceFixture)
}

func runCheck(repoRoot string) {
	if err := runProvenanceCheck(repoRoot); err != nil {
		fatal("verify fixtures and oracle provenance: %v", err)
	}
	fmt.Println("PASS all fixtures and oracle provenance verified")
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find repository root containing go.mod")
}

func fatal(format string, args ...any) {
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(&buf, "FAIL "+format+"\n", args...)
	_, _ = os.Stderr.Write(buf.Bytes())
	os.Exit(1)
}
