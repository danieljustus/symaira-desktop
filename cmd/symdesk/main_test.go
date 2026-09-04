package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/testsupport"
)

// TestMain keeps the in-process seams of the absorbed tools inert and points
// every HOME/XDG-backed store at a process-owned temporary root. Individual
// tests may override a specific seam or directory after this call.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "symdesk-cli-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated CLI test home: %v\n", err)
		os.Exit(1)
	}
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
