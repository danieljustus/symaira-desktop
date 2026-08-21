package main

import (
	"io"
	"os"
	"testing"

	"github.com/danieljustus/symaira-print/internal/config"
	"github.com/danieljustus/symaira-print/internal/press"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. The commands under test print via fmt.Println
// directly to os.Stdout rather than cmd.OutOrStdout(), so cobra's own output
// buffering does not capture it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

// resetGlobalState resets package-global command state (jsonOut,
// configWarnings, the config loader cache, and the typst probe cache) and
// isolates the environment (HOME plus SYMPRINT_* env vars) so command-level
// tests are order-independent and the developer's real config cannot leak
// into CI. It returns the isolated HOME so tests can plant a config file
// there. An empty SYMPRINT_* value is a no-op for the config loader, so the
// explicit empty assignments neutralize any host-shell values deterministically.
func resetGlobalState(t *testing.T) string {
	t.Helper()
	jsonOut = false
	configWarnings = nil
	config.ResetForTest()
	press.ResetProbeCache()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SYMPRINT_DEFAULTS_PROFILE", "")
	t.Setenv("SYMPRINT_ENGINE_TYPST", "")
	t.Setenv("SYMPRINT_MCP_OUTPUT_ROOT", "")
	return home
}
