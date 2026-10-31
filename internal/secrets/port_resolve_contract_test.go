package secrets

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// Records ResolveKey and Source resolution order against loopback doubles for
// `security` and `symvault` on a private PATH. Only synthetic canary values
// are used; the operator keychain and vault are never reached.

type portSecretsCase struct {
	ID        string `json:"id"`
	Ref       string `json:"ref"`
	EnvKey    string `json:"env_key,omitempty"`
	Security  string `json:"security,omitempty"` // double behaviour; "" = not installed
	Symvault  string `json:"symvault,omitempty"` // double behaviour; "" = not installed
	Key       string `json:"key"`
	Source    string `json:"source"`
	Log       string `json:"log,omitempty"`
	LeakCount int    `json:"canary_leaks"`
}

const portSecretsCanary = "port-canary-3f9a1c"

const portSecretsScript = `#!/bin/sh
tool=$(basename "$0")
{
  printf '%s' "$tool"
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf '\n'
} >> "$FAKE_SECRETS_LOG"
if [ "$tool" = symvault ] && [ "$1" = version ]; then
  printf '{"tool":"symvault","version":"1.0.0","schema_version":1}\n'; exit 0
fi
mode="$FAKE_SECURITY_MODE"
[ "$tool" = symvault ] && mode="$FAKE_SYMVAULT_MODE"
case "$mode" in
  ok) printf '  port-canary-3f9a1c \n' ;;
  empty) : ;;
  fail) printf 'denied port-canary-3f9a1c\n' >&2; exit 44 ;;
  slow) exec sleep 5 ;;
esac
`

func portSecretsCases() []portSecretsCase {
	return []portSecretsCase{
		{ID: "raw-key", Ref: "sk-raw-key"},
		{ID: "raw-key-untrimmed", Ref: "  sk-spaced \n"},
		{ID: "op-prefix-is-case-sensitive", Ref: "OP://vault/item", Symvault: "ok"},
		{ID: "env-key", EnvKey: "sk-from-env", Security: "ok"},
		{ID: "env-reference-resolved", EnvKey: "op://vault/env-item", Symvault: "ok"},
		{ID: "keychain-ok", Security: "ok"},
		{ID: "keychain-fail", Security: "fail"},
		{ID: "keychain-empty", Security: "empty"},
		{ID: "keychain-missing"},
		{ID: "symvault-ok", Ref: "op://vault/item", Symvault: "ok", Security: "ok"},
		{ID: "symvault-fail", Ref: "op://vault/item", Symvault: "fail", Security: "ok"},
		{ID: "symvault-empty", Ref: "op://vault/item", Symvault: "empty"},
		{ID: "symvault-missing", Ref: "op://vault/item", Security: "ok"},
		{ID: "symvault-slow", Ref: "op://vault/item", Symvault: "slow"},
	}
}

func runPortSecretsCase(t *testing.T, c portSecretsCase) portSecretsCase {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	install := func(name string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(portSecretsScript), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
			t.Fatal(err)
		}
	}
	if c.Security != "" {
		install("security")
	}
	if c.Symvault != "" {
		install("symvault")
	}
	logPath := filepath.Join(root, "tools.log")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("SYMAIRA_BIN", "")
	t.Setenv("SYMDESK_LLM_API_KEY", c.EnvKey)
	t.Setenv("FAKE_SECRETS_LOG", logPath)
	t.Setenv("FAKE_SECURITY_MODE", c.Security)
	t.Setenv("FAKE_SYMVAULT_MODE", c.Symvault)
	compose.ResetCache()
	c.Key = ResolveKey(c.Ref)
	c.Source = Source(c.Ref)
	if b, err := os.ReadFile(logPath); err == nil { //nolint:gosec // test-owned log
		// The version probe is cache/provenance traffic, not secret traffic.
		c.Log = strings.ReplaceAll(string(b), root, "<ROOT>")
	}
	// SEC-002 canary scan: the resolved secret may only be the return value.
	for _, surface := range []string{c.Source, c.Log} {
		c.LeakCount += strings.Count(surface, portSecretsCanary)
	}
	return c
}

func TestPortSecretsResolveContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell doubles are Unix-only")
	}
	var cases []portSecretsCase
	for _, c := range portSecretsCases() {
		cases = append(cases, runPortSecretsCase(t, c))
	}
	document := struct {
		SchemaVersion int               `json:"schema_version"`
		Canary        string            `json:"canary"`
		Script        string            `json:"double_script"`
		Cases         []portSecretsCase `json:"cases"`
	}{1, portSecretsCanary, portSecretsScript, cases}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "testdata/port/ai/secrets-resolve.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	expected, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, expected) {
		t.Fatal("secrets resolution fixture drift; regenerate deliberately with make secrets-resolve-fixtures-generate")
	}
}
