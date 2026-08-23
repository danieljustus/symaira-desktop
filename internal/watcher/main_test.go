package watcher

import (
	"os"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/testsupport"
)

// TestMain keeps the in-process seams of the absorbed tools inert, so this
// package's tests never touch the developer's real search index or contact
// store. Individual tests override a specific seam where they need results.
func TestMain(m *testing.M) {
	testsupport.IsolateSideEffects()
	os.Exit(m.Run())
}
