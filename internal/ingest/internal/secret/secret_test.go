package secret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/secretref"
)

func TestSharedResolverSupportsEnvironmentReferences(t *testing.T) {
	t.Setenv("SECRET_TEST_VALUE", "resolved-secret")

	got, err := secretref.Resolve(context.Background(), "env://SECRET_TEST_VALUE", "")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	if got != "resolved-secret" {
		t.Fatalf("Resolve = %q, want resolved-secret", got)
	}
}

func TestSharedResolverRejectsUnknownSchemes(t *testing.T) {
	_, err := secretref.Resolve(context.Background(), "vault://legacy/path", "")
	if err == nil {
		t.Fatal("Resolve accepted an unknown scheme")
	}
	if !strings.Contains(err.Error(), "vault://legacy/path") {
		t.Errorf("error %q does not identify the configured reference", err)
	}
}

func TestSharedResolverPassesVaultPathAfterArgumentSeparator(t *testing.T) {
	dir := t.TempDir()
	symvault := filepath.Join(dir, "symvault")
	script := []byte(`#!/bin/sh
if [ "$1" != "get" ] || [ "$2" != "--" ] || [ "$3" != "safe/path" ] || [ "$4" != "--print" ]; then
  printf 'unexpected arguments: %s %s %s %s\n' "$1" "$2" "$3" "$4" >&2
  exit 1
fi
printf 'resolved-secret\n'
`)
	if err := os.WriteFile(symvault, script, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(symvault, 0o700); err != nil { // #nosec G302 -- subprocess fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := secretref.Resolve(context.Background(), "symvault://safe/path", "")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	if got != "resolved-secret" {
		t.Fatalf("Resolve = %q, want resolved-secret", got)
	}
}

func TestSharedResolverReportsMissingSymvault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := secretref.Resolve(context.Background(), "symvault://safe/path", "")
	if !errors.Is(err, secretref.ErrSymvaultNotFound) {
		t.Fatalf("Resolve error = %v, want ErrSymvaultNotFound", err)
	}
}

func TestSharedResolverRejectsInvalidKeychainReference(t *testing.T) {
	_, err := secretref.Resolve(context.Background(), "keychain://service-only", "")
	if err == nil || !strings.Contains(err.Error(), "invalid keychain reference") {
		t.Fatalf("Resolve error = %v, want invalid keychain reference", err)
	}
}

func TestIsPlaintext(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"bare reference", "just-a-string", true},
		{"env", "env://SOME_VAR", false},
		{"symvault", "symvault://my.ref", false},
		{"keychain", "keychain://service/account", false},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPlaintext(tt.in); got != tt.want {
				t.Errorf("IsPlaintext(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
