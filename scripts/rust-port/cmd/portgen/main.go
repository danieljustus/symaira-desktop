// Command portgen coordinates generation and drift-checking of language-neutral Go oracle fixtures.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	"testdata/port/cli/history-tasks.json",
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
	"testdata/port/ai/recipe-validate.json",
	"testdata/port/room/mcp-parity.json",
	"testdata/port/room/mcp-artifact.txt",
	"testdata/port/room/mcp-mutations.json",
	"testdata/port/dataset/sync.json",
	"testdata/port/dataset/service-sync.json",
	"testdata/port/dataset/import.json",
	"testdata/port/dataset/purge.json",
	"testdata/port/dataset/cli.json",
	"testdata/port/sidecar/contracts.json",
	"testdata/port/sidecar/metadata.json",
	"testdata/port/sidecar/lifecycle.json",
	"testdata/port/sidecar/large-corpus.json",
	"testdata/port/sidecar/roundtrip.json",
	"testdata/port/representative/cases.json",
}

func main() {
	check := flag.Bool("check", false, "fail if any fixture or oracle provenance has drifted")
	commit := flag.String("oracle-commit", "", "Go oracle commit (defaults to current HEAD during generation)")
	release := flag.String("oracle-release", defaultOracleRelease, "Go oracle release")
	fixtureOracleCommit := flag.String("fixture-oracle-commit", "", "oracle commit for core and vault fixture corpora (defaults to --oracle-commit)")
	applyArtifact := flag.String("apply-artifact", "", "validate and apply a generated patch in a disposable worktree, then print its commit")
	flag.Parse()

	repoRoot, err := findRepoRoot()
	if err != nil {
		fatal("find repo root: %v", err)
	}

	if *check {
		if *applyArtifact != "" {
			fatal("--check and --apply-artifact cannot be used together")
		}
		runCheck(repoRoot)
		return
	}

	if *applyArtifact != "" {
		if err := applyArtifactCommit(repoRoot, *applyArtifact); err != nil {
			fatal("apply fixture artifact: %v", err)
		}
		return
	}
	if err := generateArtifact(repoRoot, *commit, *release, *fixtureOracleCommit, os.Stdout); err != nil {
		fatal("generate fixture artifact: %v", err)
	}
}

func runGenerate(repoRoot, commit, release string) {
	if err := generateArtifact(repoRoot, commit, release, "", os.Stdout); err != nil {
		fatal("generate fixture artifact: %v", err)
	}
}

func generateArtifact(repoRoot, commit, release, fixtureOracleCommit string, output io.Writer) error {
	resolvedCommit, err := resolveGenerationOracleCommit(repoRoot, commit)
	if err != nil {
		return fmt.Errorf("resolve generation oracle: %w", err)
	}
	commit = resolvedCommit
	if fixtureOracleCommit == "" {
		fixtureOracleCommit = commit
	}
	fixtureOracleCommit, err = resolveGenerationOracleCommit(repoRoot, fixtureOracleCommit)
	if err != nil {
		return fmt.Errorf("resolve core/vault fixture oracle: %w", err)
	}
	if err := verifyCleanWorktree(repoRoot); err != nil {
		return fmt.Errorf("generation requires a clean worktree: %w", err)
	}
	if err := verifyNoUntrackedGeneratorInputs(repoRoot); err != nil {
		return fmt.Errorf("generation source guard: %w", err)
	}
	base, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve patch base: %w", err)
	}
	if err := verifyCallerSnapshot(repoRoot, base); err != nil {
		return err
	}
	snapshot, cleanup, err := createImmutableSourceSnapshot(repoRoot, base)
	if err != nil {
		return fmt.Errorf("create private generation worktree: %w", err)
	}
	defer cleanup()
	configPath, cleanup, err := inventory.PrivateGitConfig()
	if err != nil {
		return fmt.Errorf("prepare generation Git config: %w", err)
	}
	defer cleanup()
	goTool, err := trustedGoTool()
	if err != nil {
		return fmt.Errorf("resolve generation Go tool: %w", err)
	}
	generationEnv := sanitizedCheckEnvironment(os.Environ(), configPath)
	fmt.Fprintf(os.Stderr, "Generating Go oracle fixtures in a disposable worktree (base %s, oracle %s / %s)...\n", base, commit, release)
	if err := validateFixtureDestinations(snapshot); err != nil {
		return fmt.Errorf("validate private generation destinations: %w", err)
	}
	if err := runCompleteFixtureGeneration(goTool, snapshot, generationEnv, inventory.Oracle{Commit: commit, Release: release}, fixtureOracleCommit); err != nil {
		return err
	}
	if err := validateFixtureDestinations(snapshot); err != nil {
		return fmt.Errorf("validate generated fixture destinations: %w", err)
	}
	if err := commitFixtureOutputs(snapshot, base); err != nil && !errors.Is(err, errNoFixtureChanges) {
		return err
	}
	if err := runProvenanceCheck(snapshot); err != nil {
		return fmt.Errorf("validate generated P/Q in private worktree: %w", err)
	}
	patch, err := createPatchArtifact(snapshot, base)
	if err != nil {
		return err
	}
	if err := verifyCallerSnapshot(repoRoot, base); err != nil {
		return err
	}
	if _, err := output.Write(patch); err != nil {
		return fmt.Errorf("write reviewable patch artifact: %w", err)
	}
	return nil
}

func runCompleteFixtureGeneration(goTool, repoRoot string, generationEnv []string, oracle inventory.Oracle, fixtureOracleCommit string) error {
	// These generator commands are the former core-fixtures-generate and
	// vault-fixtures-generate Make prerequisites. They run in the private
	// worktree so the top-level flow has one write boundary.
	commands := []struct {
		name string
		args []string
	}{
		{"configuration corpus", []string{"run", "./scripts/rust-port/cmd/configgen", "--oracle-commit", fixtureOracleCommit, "--oracle-release", oracle.Release}},
		{"core corpus", []string{"run", "./scripts/rust-port/cmd/coregen", "--oracle-commit", fixtureOracleCommit, "--oracle-release", oracle.Release}},
		{"search-query corpus", []string{"run", "./scripts/rust-port/cmd/querygen", "--oracle-commit", fixtureOracleCommit, "--oracle-release", oracle.Release}},
		{"vault parser corpus", []string{"run", "./scripts/rust-port/cmd/vaultgen", "--oracle-commit", fixtureOracleCommit, "--oracle-release", oracle.Release}},
		{"vault filesystem corpus", []string{"run", "./scripts/rust-port/cmd/vaultfsgen", "--oracle-commit", fixtureOracleCommit, "--oracle-release", oracle.Release}},
		{"typed vault corpus", []string{"run", "./scripts/rust-port/cmd/typedvaultgen"}},
	}
	for _, target := range commands {
		if err := runGeneratorCommand(goTool, repoRoot, generationEnv, target.name, target.args...); err != nil {
			return err
		}
	}
	vaultTargets := []struct {
		pkg string
		run string
	}{
		{"./internal/service", "^TestVaultResolutionInventory$"},
		{"./internal/health", "^TestHealthLinkResolutionInventory$"},
		{"./internal/notebook", "^TestNotebookParseInventory$"},
		{"./internal/retrieval/internal/engine", "^TestSearchMetadataInventory$"},
		{"./internal/vault", "^TestMobileWriterFixture$"},
	}
	for _, target := range vaultTargets {
		if err := runGeneratorCommand(goTool, repoRoot, generationEnvWithActivation(generationEnv), target.pkg+" "+target.run, "test", "-count=1", target.pkg, "-run", target.run); err != nil {
			return err
		}
	}

	// 1. Run package-local generators
	packages := []struct {
		pkg string
		run string
	}{
		{"./internal/config", "^TestPortConfigPrecedenceContract$"},
		{"./cmd/symdesk", "TestSymdeskCobraInventory|^TestIndex(Maintenance|Build)ProcessPortFixture$|^TestPort(VaultSelection|RecipeValidate|HistoryTasks)CLIContract$"},
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
		{"./internal/service", "^TestPortDataset(SyncContract|SyncServiceContract|ImportContract|PurgeContract|QueryCLIContract)$|^TestPortHistoryServiceContract$"},
		{"./internal/tools", "TestSymdeskMCPInventory"},
		{"./internal/selfhost", "TestSelfhostHTTPInventory"},
		{"./internal/sidecar", "^TestPortSidecar(Contract|MetadataContract)$"},
	}

	for _, target := range packages {
		//nolint:gosec // fixed generator targets, never derived from fixture output
		if err := runGeneratorCommand(goTool, repoRoot, generationEnvWithActivation(generationEnv), target.pkg+" "+target.run, "test", "-count=1", target.pkg, "-run", target.run); err != nil {
			return err
		}
	}
	// Keep this independent Go process fixture in the same P/Q generation as
	// the package-produced MCP and CLI fixtures.
	//nolint:gosec // trustedGoTool selects the executable; the generator path is fixed
	if err := runGeneratorCommand(goTool, repoRoot, generationEnv, "MCP initialize fixture", "run", "./scripts/rust-port/cmd/mcpgen"); err != nil {
		return err
	}

	commit, release := oracle.Commit, oracle.Release
	sidecarOracle := oracle
	if err := runSidecarLifecycleGenerator(repoRoot, sidecarOracle); err != nil {
		return fmt.Errorf("generate sidecar lifecycle fixture: %w", err)
	}

	// 2. Refresh the two byte-sensitive sidecar metadata fixtures, then compute
	// provenance and checksums. The lifecycle fixture above is generated by its
	// own executable source contract rather than rewritten post hoc.
	if err := syncSidecarOracleMetadata(repoRoot, commit, release); err != nil {
		return fmt.Errorf("synchronize sidecar oracle metadata: %w", err)
	}

	// The recorded oracle must describe the bytes this generation actually read:
	// a dirty tree or a foreign revision cannot relabel the fixtures. The
	// generator digest is recorded from the generating tree (HEAD), because the
	// harness identity is separate from the pinned production revision.
	worktreeSource, err := inventory.ComputeProductionSourceDigest(repoRoot)
	if err != nil {
		return fmt.Errorf("compute working-tree production source digest: %w", err)
	}
	sourceDigest, err := inventory.ComputeGitRevisionProductionSourceDigest(repoRoot, commit)
	if err != nil {
		return fmt.Errorf("compute oracle revision source digest: %w", err)
	}
	if worktreeSource != sourceDigest {
		return fmt.Errorf("working-tree production source does not match oracle commit %s: current=%s oracle=%s", commit, worktreeSource, sourceDigest)
	}
	generatorDigest, err := inventory.ComputeGitRevisionGeneratorSourceDigest(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("compute generating-tree generator source digest: %w", err)
	}

	checksums := make(map[string]string, len(fixturePaths))
	for _, rel := range fixturePaths {
		path := filepath.Join(repoRoot, rel)
		sum, err := inventory.ComputeFileChecksum(path)
		if err != nil {
			return fmt.Errorf("checksum %s: %w", rel, err)
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
		return fmt.Errorf("marshal provenance: %w", err)
	}
	provContent = append(provContent, '\n')

	provPath := filepath.Join(repoRoot, provenanceFixture)
	if err := os.WriteFile(provPath, provContent, 0o600); err != nil {
		return fmt.Errorf("write provenance in disposable worktree: %w", err)
	}
	return nil
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
