package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	portgenSidecarOracleCommitEnv  = "PORTGEN_SIDECAR_ORACLE_COMMIT"
	portgenSidecarOracleReleaseEnv = "PORTGEN_SIDECAR_ORACLE_RELEASE"
)

type fixtureCheckTarget struct {
	name          string
	args          []string
	outputs       []string
	sidecarOracle bool
}

var fixtureTestTargets = []fixtureCheckTarget{
	{"symdesk CLI", []string{"test", "-count=1", "./cmd/symdesk", "-run", "TestSymdeskCobraInventory"}, []string{"testdata/port/cli/symdesk-command-tree.json"}, false},
	{"symroom CLI and MCP", []string{"test", "-count=1", "./cmd/symroom", "-run", "TestSymRoomParserGrammar|TestSymRoomMCPInventory"}, []string{"testdata/port/cli/symroom-parser-grammar.json", "testdata/port/mcp/symroom-tools.json"}, false},
	{"symroom note CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortNoteCLIContract$"}, []string{"testdata/port/room/note-cli.json"}, false},
	{"symroom identity CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortIdentityCLIContract$"}, []string{"testdata/port/room/identity-cli.json"}, false},
	{"symroom member CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortMemberCLIContract$"}, []string{"testdata/port/room/member-cli.json"}, false},
	{"symroom decide CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortDecideCLIContract$"}, []string{"testdata/port/room/decide-cli.json"}, false},
	{"symroom index CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortIndexCLIContract$"}, []string{"testdata/port/room/index-cli.json"}, false},
	{"symroom verify", []string{"test", "-count=1", "./internal/room/journal", "-run", "^TestPortRoomVerifyContract$"}, []string{"testdata/port/room/verify.json"}, false},
	{"symroom verify CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortVerifyCLIContract$"}, []string{"testdata/port/room/verify-cli.json"}, false},
	{"symroom log", []string{"test", "-count=1", "./internal/room/journal", "-run", "^TestPortRoomLogContract$"}, []string{"testdata/port/room/log.json"}, false},
	{"symroom log CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortLogCLIContract$"}, []string{"testdata/port/room/log-cli.json"}, false},
	{"symroom artifact CLI", []string{"test", "-count=1", "./cmd/symroom", "-run", "^TestPortArtifactCLIContract$"}, []string{"testdata/port/room/artifact-cli.json"}, false},
	{"symroom watch stream", []string{"test", "-count=1", "./internal/room/desk", "-run", "^TestPortWatchStreamContract$"}, []string{"testdata/port/room/watch-stream.json"}, false},
	{"symroom brain profile CLI", []string{"test", "-count=1", "./internal/room/brainprofile", "-run", "^TestPortBrainProfileCLIContract$"}, []string{"testdata/port/room/brain-profile-cli.json"}, false},
	{"symroom init core", []string{"test", "-count=1", "./internal/room/room", "-run", "^TestPortRoomInitContract$"}, []string{"testdata/port/room/init.json"}, false},
	{"index backup", []string{"test", "-count=1", "./internal/retrieval", "-run", "^TestIndexBackupPortFixture$"}, []string{"testdata/port/retrieval/index-backup.json"}, false},
	{"index restore", []string{"test", "-count=1", "./internal/retrieval", "-run", "^TestIndexRestorePortFixture$"}, []string{"testdata/port/retrieval/index-restore.json"}, false},
	{"index relocation", []string{"test", "-count=1", "./internal/retrieval", "-run", "^TestIndexRelocatePortFixture$"}, []string{"testdata/port/retrieval/index-relocate.json"}, false},
	{"symdesk MCP", []string{"test", "-count=1", "./internal/tools", "-run", "TestSymdeskMCPInventory"}, []string{"testdata/port/mcp/symdesk-tools.json"}, false},
	{"self-hosted HTTP routes", []string{"test", "-count=1", "./internal/selfhost", "-run", "TestSelfhostHTTPInventory"}, []string{"testdata/port/http/routes.json"}, false},
	{"vault resolution", []string{"test", "-count=1", "./internal/service", "-run", "TestVaultResolutionInventory"}, []string{"testdata/port/vault/resolution.json"}, false},
	{"health links", []string{"test", "-count=1", "./internal/health", "-run", "TestHealthLinkResolutionInventory"}, []string{"testdata/port/vault/health-links.json"}, false},
	{"notebook parser", []string{"test", "-count=1", "./internal/notebook", "-run", "TestNotebookParseInventory"}, []string{"testdata/port/vault/notebook.json"}, false},
	{"search metadata", []string{"test", "-count=1", "./internal/retrieval/internal/engine", "-run", "TestSearchMetadataInventory"}, []string{"testdata/port/vault/metadata.json"}, false},
	{"mobile vault writer", []string{"test", "-count=1", "./internal/vault", "-run", "TestMobileWriterFixture"}, []string{"testdata/port/vault/mobile-writer.json"}, false},
	{"vault write filesystem", []string{"test", "-count=1", "./internal/vault", "-run", "TestPortVaultWriteFilesystemContract"}, []string{"testdata/port/vault/filesystem-writes.json"}, false},
	{"vault note operations", []string{"test", "-count=1", "./internal/service", "-run", "TestPortNoteOperationContract"}, []string{"testdata/port/vault/note-operations.json"}, false},
	{"vault history lifecycle", []string{"test", "-count=1", "./internal/history", "-run", "TestPortHistoryLifecycleContract"}, []string{"testdata/port/vault/history-lifecycle.json"}, false},
	{"vault history purge", []string{"test", "-count=1", "./internal/history", "-run", "^TestPortHistoryPurgeContract$"}, []string{"testdata/port/vault/history-purge.json"}, false},
	{"vault history prune", []string{"test", "-count=1", "./internal/history", "-run", "^TestPortHistoryPruneContract$"}, []string{"testdata/port/vault/history-prune.json"}, false},
	{"vault history service", []string{"test", "-count=1", "./internal/service", "-run", "^TestPortHistoryServiceContract$"}, []string{"testdata/port/vault/history-service.json"}, false},
	{"vault selected trash purge", []string{"test", "-count=1", "./internal/history", "-run", "^TestPortHistorySelectedTrashPurgeContract$"}, []string{"testdata/port/vault/history-trash-purge.json"}, false},
	{"vault retention corpus", []string{"test", "-count=1", "./internal/retention", "-run", "TestPortRetentionContract"}, []string{"testdata/port/vault/retention.json"}, false},
	{"vault retention rules", []string{"test", "-count=1", "./internal/retention", "-run", "TestPortRetentionRulesContract"}, []string{"testdata/port/vault/retention-rules.json"}, false},
	{"authoritative retention state", []string{"test", "-count=1", "./internal/service", "-run", "^TestPortRetentionStateContract$"}, []string{"testdata/port/vault/retention-state.json"}, false},
	{"room run projection", []string{"test", "-count=1", "./internal/room/run", "-run", "^TestPortRunProjectionContract$"}, []string{"testdata/port/room/run-projection.json"}, false},
	{"room merge read", []string{"test", "-count=1", "./internal/room/journal", "-run", "^TestPortRoomMergeReadContract$"}, []string{"testdata/port/room/merge-read.json"}, false},
	{"room index", []string{"test", "-count=1", "./internal/room/index", "-run", "^TestPortSymRoomIndexOracle$"}, []string{"testdata/port/room/index.json"}, false},
	{"room run CLI", []string{"test", "-count=1", "./internal/room/run", "-run", "^TestPortRunCLIContract$"}, []string{"testdata/port/room/run-cli.json"}, false},
	{"room run wait CLI", []string{"test", "-count=1", "./internal/room/run", "-run", "^TestPortRunWaitCLIContract$"}, []string{"testdata/port/room/run-wait-cli.json"}, false},
	{"room run mutation CLI", []string{"test", "-count=1", "./internal/room/run", "-run", "^TestPortRunMutationCLIContract$"}, []string{"testdata/port/room/run-mutations-cli.json"}, false},
	{"room MCP", []string{"test", "-count=1", "./internal/room/mcp", "-run", "^TestSymRoomMCPRepresentativeOracle$"}, []string{"testdata/port/room/mcp-parity.json"}, false},
	{"room MCP mutations", []string{"test", "-count=1", "./internal/room/mcp", "-run", "^TestSymRoomMCPMutationOracle$"}, []string{"testdata/port/room/mcp-artifact.txt", "testdata/port/room/mcp-mutations.json"}, false},
	{"dataset sync", []string{"test", "-count=1", "./internal/service", "-run", "^TestPortDataset(SyncContract|SyncServiceContract|ImportContract)$"}, []string{"testdata/port/dataset/sync.json", "testdata/port/dataset/service-sync.json", "testdata/port/dataset/import.json"}, false},
	{"dataset purge", []string{"test", "-count=1", "./internal/service", "-run", "^TestPortDatasetPurgeContract$"}, []string{"testdata/port/dataset/purge.json"}, false},
	{"sidecar contracts", []string{"test", "-count=1", "./internal/sidecar", "-run", "TestPortSidecarContract"}, []string{"testdata/port/sidecar/contracts.json"}, false},
	{"sidecar lifecycle", []string{"test", "-count=1", "./internal/sidecar", "-run", "TestPortSidecarLifecycleContract"}, []string{"testdata/port/sidecar/lifecycle.json"}, true},
	{"sidecar oracle metadata", []string{"test", "-count=1", "./scripts/rust-port/cmd/sidecar-roundtrip", "-run", "TestCommittedSidecarOracleIdentities"}, []string{"testdata/port/sidecar/large-corpus.json", "testdata/port/sidecar/roundtrip.json"}, false},
}

var fixtureGeneratorTargets = []fixtureCheckTarget{
	{"configuration corpus", []string{"run", "./scripts/rust-port/cmd/configgen", "--check"}, []string{"testdata/port/core/config.json"}, false},
	{"core corpus", []string{"run", "./scripts/rust-port/cmd/coregen", "--check"}, []string{"testdata/port/core/document-formats.json", "testdata/port/core/german-search.json", "testdata/port/core/simhash.json", "testdata/port/core/textnorm.json"}, false},
	{"search-query corpus", []string{"run", "./scripts/rust-port/cmd/querygen", "--check"}, []string{"testdata/port/core/search-query.json"}, false},
	{"vault parser corpus", []string{"run", "./scripts/rust-port/cmd/vaultgen", "--check"}, []string{"testdata/port/vault/parse.json"}, false},
	{"vault filesystem corpus", []string{"run", "./scripts/rust-port/cmd/vaultfsgen", "--check"}, []string{"testdata/port/vault/filesystem.json"}, false},
	{"vault frontmatter writes", []string{"run", "./scripts/rust-port/cmd/vaultwritegen", "--check"}, []string{"testdata/port/vault/frontmatter-write.json"}, false},
	{"typed vault corpus", []string{"run", "./scripts/rust-port/cmd/typedvaultgen", "--check"}, []string{"testdata/port/vault/typed.json"}, false},
	{"representative corpus", []string{"run", "./scripts/rust-port/cmd/representativegen", "--check"}, []string{"testdata/port/http/representative.json", "testdata/port/representative/cases.json"}, false},
	{"MCP corpus", []string{"run", "./scripts/rust-port/cmd/mcpgen", "--check"}, []string{"testdata/port/mcp/representative.json"}, false},
}

var runFixtureCheckTarget = func(goTool, repoRoot string, environment []string, target fixtureCheckTarget) error {
	//nolint:gosec // target args are static fixture controls declared above.
	command := exec.Command(goTool, target.args...)
	command.Dir = repoRoot
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\noutput: %s", target.name, err, string(output))
	}
	return nil
}

var fixtureGenerationEnvironment = map[string]struct{}{
	"GENERATE_PORT_FIXTURES": {},
	"PORT_FIXTURES_GENERATE": {},
	"PORTGEN_GENERATE":       {},
	"PORT_GENERATE":          {},
	"SYMDESK_PORT_GENERATE":  {},
}

func sanitizedCheckEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+9)
	home, profile := "", ""
	for _, item := range environment {
		name, value, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		switch upper {
		case "HOME":
			home = value
		case "USERPROFILE":
			profile = value
		}
		if isFixtureGenerationEnvironment(name) || upper == "PORT_FIXTURE_PATH" || name == portgenSidecarOracleCommitEnv || name == portgenSidecarOracleReleaseEnv || strings.HasPrefix(upper, "GO") || strings.HasPrefix(upper, "GIT") || upper == "PATH" {
			continue
		}
		result = append(result, item)
	}
	// The fixture generators resolve the home directory only to normalize it
	// into the fixture, but one has to resolve at all: POSIX reads HOME, Windows
	// reads USERPROFILE. A runner that reaches the Go tools through a POSIX
	// shell started with --noprofile carries neither name, which made the
	// configuration corpus fail with "user home dir: %userprofile% is not
	// defined". Fill the missing name from the present one and fall back to a
	// writable scratch directory when both are absent; the Makefile's runtime
	// environment pins both names to one directory for the same reason.
	var missing []string
	switch {
	case home == "" && profile == "":
		scratch := filepath.Join(os.TempDir(), "portgen-fixture-home")
		_ = os.MkdirAll(scratch, 0o700)
		missing = []string{"HOME=" + scratch, "USERPROFILE=" + scratch}
	case home == "":
		missing = []string{"HOME=" + profile}
	case profile == "":
		missing = []string{"USERPROFILE=" + home}
	}
	return append(append(result, missing...),
		//nolint:staticcheck // the harness deliberately uses the GOROOT it was built with
		"PATH="+pinnedCheckPath(),
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_TERMINAL_PROMPT=0",
		"GOWORK=off",
		"GOENV=off",
		"GOFLAGS=-mod=readonly",
		"GOTOOLCHAIN=local",
		"CGO_ENABLED=0",
	)
}

func sidecarOracleEnvironment(environment []string, oracle inventory.Oracle) []string {
	result := append([]string(nil), sanitizedCheckEnvironment(environment)...)
	return append(result,
		portgenSidecarOracleCommitEnv+"="+oracle.Commit,
		portgenSidecarOracleReleaseEnv+"="+oracle.Release,
	)
}

func isFixtureGenerationEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	if _, ok := fixtureGenerationEnvironment[upper]; ok {
		return true
	}
	return strings.HasSuffix(upper, "_GENERATE")
}

// pinnedCheckPath is the PATH of the sanitized check environment: the GOROOT
// the harness was built with, plus the directory of a git executable resolved
// before the ambient PATH is dropped. One registered check target — the
// frontmatter write generator — resolves the pinned Go revision from
// immutable Git objects, so a PATH carrying only the Go tool would fail it
// with "git: executable file not found in $PATH" instead of checking the
// fixture. Every other entry stays a pure toolchain PATH.
func pinnedCheckPath() string {
	//nolint:staticcheck // the harness deliberately uses the GOROOT it was built with
	goBinaryPath := filepath.Join(runtime.GOROOT(), "bin")
	git, err := exec.LookPath("git")
	if err != nil {
		return goBinaryPath
	}
	gitDirectory := filepath.Dir(git)
	if !filepath.IsAbs(gitDirectory) {
		return goBinaryPath
	}
	return gitDirectory + string(os.PathListSeparator) + goBinaryPath
}

func validateFixtureCheckCoverage() error {
	covered := make(map[string]string, len(fixturePaths))
	for _, target := range append(append([]fixtureCheckTarget(nil), fixtureTestTargets...), fixtureGeneratorTargets...) {
		if target.name == "" || len(target.args) == 0 {
			return fmt.Errorf("fixture check target is incomplete")
		}
		for _, rel := range target.outputs {
			if previous, exists := covered[rel]; exists {
				return fmt.Errorf("fixture %s is covered twice (%s and %s)", rel, previous, target.name)
			}
			covered[rel] = target.name
		}
	}
	for _, rel := range fixturePaths {
		if _, exists := covered[rel]; !exists {
			return fmt.Errorf("fixture %s has no independent check target", rel)
		}
	}
	for rel := range covered {
		if !isPortDerivedOutput(rel) {
			return fmt.Errorf("fixture check target covers path outside the provenance allowlist: %s", rel)
		}
	}
	return nil
}

func trustedGoTool() (string, error) {
	// The bundled tool carries the platform executable suffix; without it the
	// Windows leg fails with "resolve Go tool bundled with portgen" before a
	// single fixture is checked.
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	//nolint:staticcheck // the harness deliberately uses the GOROOT it was built with
	path := filepath.Join(runtime.GOROOT(), "bin", name)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("resolve Go tool bundled with portgen: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("go tool path %s is a directory", path)
	}
	return path, nil
}

func runSidecarLifecycleGenerator(repoRoot string, oracle inventory.Oracle) error {
	goTool, err := trustedGoTool()
	if err != nil {
		return err
	}
	var target fixtureCheckTarget
	for _, candidate := range fixtureTestTargets {
		if candidate.sidecarOracle {
			target = candidate
			break
		}
	}
	if target.name == "" {
		return fmt.Errorf("sidecar lifecycle generator target is not registered")
	}
	environment := append(sidecarOracleEnvironment(os.Environ(), oracle), "PORT_GENERATE=1")
	return runFixtureCheckTarget(goTool, repoRoot, environment, target)
}

func runFixtureChecks(repoRoot string, sidecarOracle inventory.Oracle) error {
	if err := validateFixtureCheckCoverage(); err != nil {
		return err
	}
	goTool, err := trustedGoTool()
	if err != nil {
		return err
	}
	environment := sanitizedCheckEnvironment(os.Environ())
	targets := append(append([]fixtureCheckTarget(nil), fixtureTestTargets...), fixtureGeneratorTargets...)
	for _, target := range targets {
		targetEnvironment := environment
		if target.sidecarOracle {
			targetEnvironment = sidecarOracleEnvironment(os.Environ(), sidecarOracle)
		}
		if err := runFixtureCheckTarget(goTool, repoRoot, targetEnvironment, target); err != nil {
			return err
		}
	}
	return nil
}
