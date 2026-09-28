//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	for _, name := range []string{"PORT_GENERATE", "PORTGEN_GENERATE", "COREGEN_GENERATE", "MCPGEN_GENERATE", "PORT_FIXTURE_PATH", "port_fixture_path"} {
		if !strings.Contains(text, "-u "+name) {
			t.Fatalf("port-fixtures-check did not unset %s:\n%s", name, output)
		}
	}
	for _, setting := range []string{"GOWORK=off", "GOENV=off", "GOFLAGS=-mod=readonly"} {
		if !strings.Contains(text, setting) {
			t.Fatalf("port-fixtures-check did not pin %s:\n%s", setting, output)
		}
	}
	if strings.Contains(text, "\n: GOTOOLCHAIN") || strings.HasPrefix(text, ": GOTOOLCHAIN") {
		t.Fatalf("port-fixtures-check accepted PORTGEN_CHECK_ENV override:\n%s", output)
	}
}

func TestMakeGenerationEnvironmentCannotBeCommandLineOverridden(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile")) //nolint:gosec // repository root and fixed Makefile name
	if err != nil {
		t.Fatal(err)
	}
	targetPattern := regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+-fixtures-generate):`)
	matches := targetPattern.FindAllStringSubmatch(string(makefile), -1)
	if len(matches) == 0 {
		t.Fatal("Makefile has no fixture-generation targets")
	}
	args := []string{"-n", "PORTGEN_GENERATE_GO_ENV=:"}
	for _, match := range matches {
		args = append(args, match[1])
	}
	command := exec.Command("make", args...)
	command.Dir = repoRoot
	command.Env = make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOWORK=") || strings.HasPrefix(entry, "GOFLAGS=") || strings.HasPrefix(entry, "GOENV=") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env,
		"GOWORK=/tmp/poisoned-go.work",
		"GOFLAGS=-overlay=/tmp/poisoned-go-overlay.json",
		"GOENV=/tmp/poisoned-go.env",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make dry run: %v\n%s", err, output)
	}
	goCommands := 0
	goCommandPattern := regexp.MustCompile(`\bgo (run|test)\b`)
	for _, line := range strings.Split(string(output), "\n") {
		if goCommandPattern.MatchString(line) {
			goCommands++
			if !strings.HasPrefix(line, "env GOWORK=off GOENV=off GOFLAGS=-mod=readonly ") {
				t.Fatalf("fixture generator retained ambient Go configuration: %s", line)
			}
		}
	}
	if goCommands == 0 {
		t.Fatal("fixture-generation targets emitted no Go commands")
	}
}
