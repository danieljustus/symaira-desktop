package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var fixtureTestTargets = []struct {
	pkg string
	run string
}{
	{"./cmd/symdesk", "TestSymdeskCobraInventory"},
	{"./cmd/symroom", "TestSymRoomParserGrammar|TestSymRoomMCPInventory"},
	{"./internal/tools", "TestSymdeskMCPInventory"},
	{"./internal/selfhost", "TestSelfhostHTTPInventory"},
	{"./internal/service", "TestVaultResolutionInventory"},
	{"./internal/health", "TestHealthLinkResolutionInventory"},
	{"./internal/notebook", "TestNotebookParseInventory"},
	{"./internal/retrieval/internal/engine", "TestSearchMetadataInventory"},
	{"./internal/vault", "TestMobileWriterFixture"},
	{"./internal/sidecar", "TestPortSidecar(Contract|LifecycleContract)"},
}

var fixtureGenerationEnvironment = map[string]struct{}{
	"GENERATE_PORT_FIXTURES": {},
	"PORT_FIXTURES_GENERATE": {},
	"PORTGEN_GENERATE":       {},
	"PORT_GENERATE":          {},
	"SYMDESK_PORT_GENERATE":  {},
}

func sanitizedCheckEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if isFixtureGenerationEnvironment(name) || strings.EqualFold(name, "GOWORK") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "GOWORK=off")
}

func isFixtureGenerationEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	if _, ok := fixtureGenerationEnvironment[upper]; ok {
		return true
	}
	return strings.HasSuffix(upper, "_GENERATE")
}

func runFixturePackageChecks(repoRoot string) error {
	environment := sanitizedCheckEnvironment(os.Environ())
	for _, target := range fixtureTestTargets {
		//nolint:gosec // target package and test expression are static generator controls.
		command := exec.Command("go", "test", "-count=1", target.pkg, "-run", target.run)
		command.Dir = repoRoot
		command.Env = environment
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s (%s): %w\noutput: %s", target.pkg, target.run, err, string(output))
		}
	}
	return nil
}
