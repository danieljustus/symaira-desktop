package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/testsupport"
)

// TestMain keeps the in-process seams of the absorbed tools inert and points
// every HOME/XDG-backed store at a process-owned temporary root. Individual
// tests may override a specific seam or directory after this call.
var processFixtureBuild struct {
	sync.Once
	root        string
	environment []string
	binary      string
	err         error
	output      []byte
}

func TestMain(m *testing.M) {
	processFixtureBuild.environment = os.Environ()
	root, err := os.MkdirTemp("", "symdesk-cli-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated CLI test home: %v\n", err)
		os.Exit(1)
	}
	processFixtureBuild.root = root
	for key, value := range map[string]string{
		"HOME":            root,
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_CACHE_HOME":  filepath.Join(root, "cache"),
		"SYMDESK_VAULT":   "",
		"SYMDESK_SIDECAR": "",
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "set isolated CLI test environment %s: %v\n", key, err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}

	testsupport.IsolateSideEffects()
	code := m.Run()
	if err := os.RemoveAll(root); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "remove isolated CLI test home: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// processFixtureBinary builds the uninstrumented CLI once per package run.
// Retain the caller's original Go cache environment before TestMain isolates
// HOME; each invocation still gets its own vault, HOME and subprocess env.
func processFixtureBinary(t *testing.T) string {
	t.Helper()
	processFixtureBuild.Do(func() {
		processFixtureBuild.binary = filepath.Join(processFixtureBuild.root, "symdesk")
		if runtime.GOOS == "windows" {
			processFixtureBuild.binary += ".exe"
		}
		//nolint:gosec // fixed Go build of this package into the process-owned temporary root.
		command := exec.Command("go", "build", "-o", processFixtureBuild.binary, ".")
		command.Env = processFixtureBuild.environment
		processFixtureBuild.output, processFixtureBuild.err = command.CombinedOutput()
	})
	if processFixtureBuild.err != nil {
		t.Fatalf("build Go CLI process fixture: %v\n%s", processFixtureBuild.err, processFixtureBuild.output)
	}
	return processFixtureBuild.binary
}
