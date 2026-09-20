package tools

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// isolateCompanionBinaries removes every resolution tier that compose.Resolve
// consults ahead of PATH, so a missing-companion assertion in this package is
// independent of the machine running the test.
//
// Clearing PATH alone is not enough: Resolve searches $SYMAIRA_BIN and the
// managed runtime directory (~/.symaira/bin) first, so on a host with either
// populated the real binary answers and the absence check fails — or a
// positive test invokes the real installed binary.
func isolateCompanionBinaries(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
	// $HOME drives the managed-runtime tier; USERPROFILE is its Windows
	// equivalent.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv(compose.SymairaBinEnvVar, "")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}
