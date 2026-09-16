package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type fixtureCheckTarget struct {
	name    string
	args    []string
	outputs []string
}

var fixtureTestTargets = []fixtureCheckTarget{
	{"symdesk CLI", []string{"test", "-count=1", "./cmd/symdesk", "-run", "TestSymdeskCobraInventory"}, []string{"testdata/port/cli/symdesk-command-tree.json"}},
	{"symroom CLI and MCP", []string{"test", "-count=1", "./cmd/symroom", "-run", "TestSymRoomParserGrammar|TestSymRoomMCPInventory"}, []string{"testdata/port/cli/symroom-parser-grammar.json", "testdata/port/mcp/symroom-tools.json"}},
	{"symdesk MCP", []string{"test", "-count=1", "./internal/tools", "-run", "TestSymdeskMCPInventory"}, []string{"testdata/port/mcp/symdesk-tools.json"}},
	{"self-hosted HTTP routes", []string{"test", "-count=1", "./internal/selfhost", "-run", "TestSelfhostHTTPInventory"}, []string{"testdata/port/http/routes.json"}},
	{"vault resolution", []string{"test", "-count=1", "./internal/service", "-run", "TestVaultResolutionInventory"}, []string{"testdata/port/vault/resolution.json"}},
	{"health links", []string{"test", "-count=1", "./internal/health", "-run", "TestHealthLinkResolutionInventory"}, []string{"testdata/port/vault/health-links.json"}},
	{"notebook parser", []string{"test", "-count=1", "./internal/notebook", "-run", "TestNotebookParseInventory"}, []string{"testdata/port/vault/notebook.json"}},
	{"search metadata", []string{"test", "-count=1", "./internal/retrieval/internal/engine", "-run", "TestSearchMetadataInventory"}, []string{"testdata/port/vault/metadata.json"}},
	{"mobile vault writer", []string{"test", "-count=1", "./internal/vault", "-run", "TestMobileWriterFixture"}, []string{"testdata/port/vault/mobile-writer.json"}},
	{"sidecar contracts", []string{"test", "-count=1", "./internal/sidecar", "-run", "TestPortSidecar(Contract|LifecycleContract)"}, []string{"testdata/port/sidecar/contracts.json", "testdata/port/sidecar/lifecycle.json"}},
}

var fixtureGeneratorTargets = []fixtureCheckTarget{
	{"configuration corpus", []string{"run", "./scripts/rust-port/cmd/configgen", "--check"}, []string{"testdata/port/core/config.json"}},
	{"core corpus", []string{"run", "./scripts/rust-port/cmd/coregen", "--check"}, []string{"testdata/port/core/document-formats.json", "testdata/port/core/german-search.json", "testdata/port/core/simhash.json", "testdata/port/core/textnorm.json"}},
	{"search-query corpus", []string{"run", "./scripts/rust-port/cmd/querygen", "--check"}, []string{"testdata/port/core/search-query.json"}},
	{"vault parser corpus", []string{"run", "./scripts/rust-port/cmd/vaultgen", "--check"}, []string{"testdata/port/vault/parse.json"}},
	{"vault filesystem corpus", []string{"run", "./scripts/rust-port/cmd/vaultfsgen", "--check"}, []string{"testdata/port/vault/filesystem.json"}},
	{"typed vault corpus", []string{"run", "./scripts/rust-port/cmd/typedvaultgen", "--check"}, []string{"testdata/port/vault/typed.json"}},
	{"representative corpus", []string{"run", "./scripts/rust-port/cmd/representativegen", "--check"}, []string{"testdata/port/http/representative.json", "testdata/port/representative/cases.json"}},
	{"MCP corpus", []string{"run", "./scripts/rust-port/cmd/mcpgen", "--check"}, []string{"testdata/port/mcp/representative.json"}},
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
	result := make([]string, 0, len(environment)+5)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if isFixtureGenerationEnvironment(name) || strings.HasPrefix(upper, "GO") || strings.HasPrefix(upper, "GIT") || upper == "PATH" {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"PATH="+filepath.Join(runtime.GOROOT(), "bin"),
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

func isFixtureGenerationEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	if _, ok := fixtureGenerationEnvironment[upper]; ok {
		return true
	}
	return strings.HasSuffix(upper, "_GENERATE")
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
	path := filepath.Join(runtime.GOROOT(), "bin", "go")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("resolve Go tool bundled with portgen: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("Go tool path %s is a directory", path)
	}
	return path, nil
}

func runFixtureChecks(repoRoot string) error {
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
		if err := runFixtureCheckTarget(goTool, repoRoot, environment, target); err != nil {
			return err
		}
	}
	return nil
}
