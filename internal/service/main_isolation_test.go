package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

// TestMainIsolatesUserStores guards TestMain: the data home every
// vault-scoped retrieval store derives from must live in the private test
// home, not the developer's real home or an ambient XDG override.
func TestMainIsolatesUserStores(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(home), "symdesk-service-tests-") {
		t.Fatalf("HOME = %q, want private TestMain home", home)
	}
	data, err := config.ResolveDataHome()
	if err != nil {
		t.Fatal(err)
	}
	if data != filepath.Join(home, ".local", "share") {
		t.Fatalf("data home = %q, want derived from private HOME %q", data, home)
	}

	perTest := t.TempDir()
	t.Setenv("HOME", perTest)
	if data, _ := config.ResolveDataHome(); data != filepath.Join(perTest, ".local", "share") {
		t.Fatalf("per-test HOME overridden by suite-global data home: %q", data)
	}
}
