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
	"testdata/port/cli/symdesk-command-tree.json",
	"testdata/port/cli/config-vault-selection.json",
	"testdata/port/cli/symroom-parser-grammar.json",
	"testdata/port/core/config.json",
	"testdata/port/core/config-precedence.json",
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
	"testdata/port/vault/filesystem-writes.json",
	"testdata/port/vault/frontmatter-write.json",
	"testdata/port/vault/history-lifecycle.json",
	"testdata/port/vault/history-purge.json",
	"testdata/port/vault/history-prune.json",
	"testdata/port/vault/history-service.json",
	"testdata/port/vault/history-trash-purge.json",
	"testdata/port/vault/note-operations.json",
	"testdata/port/vault/retention.json",
	"testdata/port/vault/retention-rules.json",
	"testdata/port/vault/retention-state.json",
	"testdata/port/room/run-projection.json",
	"testdata/port/room/run-cli.json",
	"testdata/port/room/run-wait-cli.json",
	"testdata/port/room/run-mutations-cli.json",
	"testdata/port/room/note-cli.json",
	"testdata/port/room/decide-cli.json",
	"testdata/port/room/identity-cli.json",
	"testdata/port/room/member-cli.json",
	"testdata/port/room/merge-read.json",
	"testdata/port/room/index.json",
	"testdata/port/room/index-cli.json",
	"testdata/port/room/verify.json",
	"testdata/port/room/verify-cli.json",
	"testdata/port/room/log.json",
	"testdata/port/room/log-cli.json",
	"testdata/port/room/artifact-cli.json",
	"testdata/port/room/artifact-identity-cli.json",
	"testdata/port/room/watch-stream.json",
	"testdata/port/room/brain-profile-cli.json",
	"testdata/port/room/init.json",
	"testdata/port/room/init-cli.json",
	"testdata/port/room/watch-cli.json",
	"testdata/port/room/doctor-cli.json",
	"testdata/port/room/checkpoint-cli.json",
	"testdata/port/room/run-approval-cli.json",
	"testdata/port/retrieval/index-backup.json",
	"testdata/port/retrieval/index-restore.json",
	"testdata/port/retrieval/index-relocate.json",
	"testdata/port/retrieval/index-location.json",
	"testdata/port/cli/index-maintenance-process.json",
	"testdata/port/cli/index-build-process.json",
	"testdata/port/room/mcp-parity.json",
	"testdata/port/room/mcp-artifact.txt",
	"testdata/port/room/mcp-mutations.json",
	"testdata/port/dataset/sync.json",
	"testdata/port/dataset/service-sync.json",
	"testdata/port/dataset/import.json",
	"testdata/port/dataset/purge.json",
	"testdata/port/sidecar/contracts.json",
	"testdata/port/sidecar/lifecycle.json",
	"testdata/port/sidecar/large-corpus.json",
	"testdata/port/sidecar/roundtrip.json",
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
	if err := verifyCleanWorktree(repoRoot); err != nil {
		fatal("generation requires a clean worktree: %v", err)
	}
	fmt.Printf("Generating Go oracle fixtures (oracle %s / %s)...\n", commit, release)

	// 1. Run package-local generators
	packages := []struct {
		pkg string
		run string
	}{
		{"./internal/config", "^TestPortConfigPrecedenceContract$"},
		{"./cmd/symdesk", "TestSymdeskCobraInventory|^TestIndex(Maintenance|Build)ProcessPortFixture$|^TestPortVaultSelectionCLIContract$"},
		{"./internal/room/journal", "^TestPortRoomVerifyContract$"},
		{"./internal/room/journal", "^TestPortRoomLogContract$"},
		{"./cmd/symroom", "TestSymRoomParserGrammar|TestSymRoomMCPInventory|TestPort(Note|Decide|Identity|Member|Index|Verify|Log|Artifact|ArtifactIdentity|Init|Watch|Doctor|Checkpoint)CLIContract"},
		{"./cmd/symroom", "^TestPortRunApprovalCLIContract$"},
		{"./internal/room/run", "^TestPortRunProjectionContract$"},
		{"./internal/room/room", "^TestPortRoomInitContract$"},
		{"./internal/room/journal", "^TestPortRoomMergeReadContract$"},
		{"./internal/room/desk", "^TestPortWatchStreamContract$"},
		{"./internal/room/brainprofile", "^TestPortBrainProfileCLIContract$"},
		{"./internal/room/index", "^TestPortSymRoomIndexOracle$"},
		{"./internal/retrieval", "^TestIndex(Backup|Restore|Relocate|Location)PortFixture$"},
		{"./internal/room/run", "^TestPortRun(Wait|Mutation)?CLIContract$"},
		{"./internal/room/mcp", "^TestSymRoomMCP(Representative|Mutation)Oracle$"},
		{"./internal/history", "^TestPortHistory(PurgeContract|PruneContract|SelectedTrashPurgeContract)$"},
		{"./internal/service", "^TestPortDataset(SyncContract|SyncServiceContract|ImportContract|PurgeContract)$|^TestPortHistoryServiceContract$"},
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
	// Keep this independent Go process fixture in the same P/Q generation as
	// the package-produced MCP and CLI fixtures.
	cmd := exec.Command("go", "run", "./scripts/rust-port/cmd/mcpgen")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		fatal("generate MCP initialize fixture: %v\noutput: %s", err, string(out))
	}

	sidecarOracle := inventory.Oracle{Commit: commit, Release: release}
	if err := runSidecarLifecycleGenerator(repoRoot, sidecarOracle); err != nil {
		fatal("generate sidecar lifecycle fixture: %v", err)
	}

	// 2. Refresh the two byte-sensitive sidecar metadata fixtures, then compute
	// provenance and checksums. The lifecycle fixture above is generated by its
	// own executable source contract rather than rewritten post hoc.
	if err := syncSidecarOracleMetadata(repoRoot, commit, release); err != nil {
		fatal("synchronize sidecar oracle metadata: %v", err)
	}

	// The recorded oracle must describe the bytes this generation actually read:
	// a dirty tree or a foreign revision cannot relabel the fixtures. The
	// generator digest is recorded from the generating tree (HEAD), because the
	// harness identity is separate from the pinned production revision.
	worktreeSource, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		fatal("compute working-tree production source digest: %v", err)
	}
	sourceDigest, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, commit)
	if err != nil {
		fatal("compute oracle revision source digest: %v", err)
	}
	if worktreeSource != sourceDigest {
		fatal("working-tree production source does not match oracle commit %s: current=%s oracle=%s", commit, worktreeSource, sourceDigest)
	}
	generatorDigest, err := inventory.ComputeGitRevisionGeneratorSourceDigest(repoRoot, "HEAD")
	if err != nil {
		fatal("compute generating-tree generator source digest: %v", err)
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
		SchemaVersion:          1,
		Oracle:                 sidecarOracle,
		ProductionSourceDigest: sourceDigest,
		GeneratorSourceDigest:  generatorDigest,
		SurfaceCounts:          expectedSurfaceCounts(),
		FixtureChecksums:       checksums,
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
