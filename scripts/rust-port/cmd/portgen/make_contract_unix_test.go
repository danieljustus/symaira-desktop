//go:build !windows

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMakeCheckEnvironmentCannotBeCommandLineOverridden(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("make", "-n", "PORTGEN_CHECK_ENV=:", "port-fixtures-check")
	command.Dir = repoRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make dry run: %v\n%s", err, output)
	}
	line := "env -u PORT_GENERATE -u port_generate -u PORT_FIXTURES_GENERATE -u port_fixtures_generate -u PORTGEN_GENERATE -u portgen_generate -u GENERATE_PORT_FIXTURES -u generate_port_fixtures -u SYMDESK_PORT_GENERATE -u symdesk_port_generate GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/portgen --check"
	if !strings.Contains(string(output), line) {
		t.Fatalf("port-fixtures-check accepted PORTGEN_CHECK_ENV override:\n%s", output)
	}
}
