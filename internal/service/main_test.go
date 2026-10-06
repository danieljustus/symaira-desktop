package service

import (
	"fmt"
	"os"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/testsupport"
)

// TestMain keeps this package's tests away from the developer's real search
// index, contact store and settings. The facade seams of the absorbed tools
// are made inert, and the process gets a private HOME/USERPROFILE before any
// service is constructed, so code that opens a vault-scoped retrieval client
// directly cannot read or seed from the user's standalone store.
//
// Ambient XDG overrides are cleared rather than pinned to one suite-global
// directory: XDG paths then derive from HOME, so a test's own
// t.Setenv("HOME", t.TempDir()) still gives it a private store.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "symdesk-service-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated service test home: %v\n", err)
		os.Exit(1)
	}
	for _, key := range []string{
		"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME",
		"SYMDESK_VAULT", "SYMDESK_SIDECAR", "SYMDESK_INBOX", "SYMRELATE_DATA_HOME",
		"SYMINGEST_VAULT", "SYMINGEST_INBOX", "SYMINGEST_DB_PATH", "SYMINGEST_ARCHIVE_PATH",
	} {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "clear ambient %s: %v\n", key, err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, root); err != nil {
			fmt.Fprintf(os.Stderr, "set isolated %s: %v\n", key, err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}

	testsupport.IsolateSideEffects()
	code := m.Run()
	if err := os.RemoveAll(root); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "remove isolated service test home: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
