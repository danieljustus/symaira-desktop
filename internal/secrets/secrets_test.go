package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

func writeMockTool(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil { //nolint:gosec // G306: test fixture must be executable.
		t.Fatal(err)
	}
}

func withMockPath(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

func TestResolveKey(t *testing.T) {
	// Set an env var
	t.Setenv("SYMDESK_LLM_API_KEY", "test-env-key")

	// Empty ref should fallback to env var
	key := ResolveKey("")
	if key != "test-env-key" {
		t.Errorf("Expected test-env-key, got %s", key)
	}

	// Raw string should just return the string
	key = ResolveKey("raw-key")
	if key != "raw-key" {
		t.Errorf("Expected raw-key, got %s", key)
	}
}

func TestSource(t *testing.T) {
	t.Setenv("SYMDESK_LLM_API_KEY", "test-env-key")

	src := Source("")
	if src != "config/env" {
		t.Errorf("Expected config/env, got %s", src)
	}
}

// Whether an op:// reference reports as resolvable depends on symvault's
// presence, so this must run against a mocked PATH rather than whatever the
// host machine happens to have installed.
func TestSourceSymvaultPresent(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symvault", `#!/bin/bash
if [ "$1" = "version" ]; then
	echo '{"tool":"symvault","version":"1.0.0","schema_version":1}'
	exit 0
fi
exit 1
`)
	withMockPath(t, dir)

	src := Source("op://vault/item/key")
	if src != "symvault" {
		t.Errorf("Expected symvault, got %s", src)
	}
}

func TestResolveKeyKeychainSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "security", `#!/bin/bash
if [ "$1" = "find-generic-password" ] && [ "$2" = "-s" ] && [ "$3" = "symaira-desktop" ]; then
	echo "keychain-secret"
	exit 0
fi
exit 1
`)
	withMockPath(t, dir)

	// Ensure no env var gets in the way
	t.Setenv("SYMDESK_LLM_API_KEY", "")

	key := ResolveKey("")
	if key != "keychain-secret" {
		t.Errorf("Expected keychain-secret, got %q", key)
	}

	src := Source("")
	if src != "keychain" {
		t.Errorf("Expected keychain, got %q", src)
	}
}

func TestResolveKeyKeychainFailure(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "security", `#!/bin/bash
exit 1
`)
	withMockPath(t, dir)

	t.Setenv("SYMDESK_LLM_API_KEY", "")

	key := ResolveKey("")
	if key != "" {
		t.Errorf("Expected empty string, got %q", key)
	}

	src := Source("")
	if src != "none" {
		t.Errorf("Expected none, got %q", src)
	}
}

func TestResolveKeySymvaultSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symvault", `#!/bin/bash
if [ "$1" = "version" ]; then
	echo '{"tool":"symvault","version":"1.0.0","schema_version":1}'
	exit 0
fi
if [ "$1" = "get" ] && [ "$2" = "op://vault/item/key" ]; then
	echo "symvault-secret"
	exit 0
fi
exit 1
`)
	withMockPath(t, dir)

	key := ResolveKey("op://vault/item/key")
	if key != "symvault-secret" {
		t.Errorf("Expected symvault-secret, got %q", key)
	}

	src := Source("op://vault/item/key")
	if src != "symvault" {
		t.Errorf("Expected symvault, got %q", src)
	}
}

func TestResolveKeySymvaultFailure(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symvault", `#!/bin/bash
if [ "$1" = "version" ]; then
	echo '{"tool":"symvault","version":"1.0.0","schema_version":1}'
	exit 0
fi
exit 1
`)
	withMockPath(t, dir)

	key := ResolveKey("op://vault/item/key")
	if key != "" {
		t.Errorf("Expected empty string on symvault failure, got %q", key)
	}
}

// With symvault unavailable, an op:// reference must never fall through and
// resolve to the raw reference string — that string would then be sent
// verbatim as the Anthropic API key, leaking the vault/item naming to a
// third party.
func TestResolveKeySymvaultAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	key := ResolveKey("op://vault/item/key")
	if key != "" {
		t.Errorf("Expected empty string when symvault absent, got %q", key)
	}
}

func TestSourceSymvaultAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	src := Source("op://vault/item/key")
	if src != "symvault (missing)" {
		t.Errorf("Expected %q, got %q", "symvault (missing)", src)
	}
}
