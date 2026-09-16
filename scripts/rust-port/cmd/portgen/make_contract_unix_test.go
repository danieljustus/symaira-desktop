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
	text := string(output)
	for _, name := range []string{"PORT_GENERATE", "PORTGEN_GENERATE", "COREGEN_GENERATE", "MCPGEN_GENERATE"} {
		if !strings.Contains(text, "-u "+name) {
			t.Fatalf("port-fixtures-check did not unset %s:\n%s", name, output)
		}
	}
	if strings.Contains(text, "\n: GOTOOLCHAIN") || strings.HasPrefix(text, ": GOTOOLCHAIN") {
		t.Fatalf("port-fixtures-check accepted PORTGEN_CHECK_ENV override:\n%s", output)
	}
}
