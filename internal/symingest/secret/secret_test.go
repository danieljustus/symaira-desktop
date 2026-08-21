package secret

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func createMockExecutable(t *testing.T, dir, name, content string) {
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte(content), 0755)
	if err != nil {
		t.Fatalf("failed to create mock executable: %v", err)
	}
}

func TestResolve(t *testing.T) {
	// Save originals to restore at end of TestResolve
	origSym := symvaultGetFn
	origKey := keychainGetFn
	defer func() {
		symvaultGetFn = origSym
		keychainGetFn = origKey
	}()

	symvaultGetFn = func(ctx context.Context, ref string) (string, error) {
		return "symvault-secret", nil
	}
	keychainGetFn = func(ctx context.Context, service, account string) (string, error) {
		return "keychain-secret", nil
	}

	t.Run("env", func(t *testing.T) {
		os.Setenv("TEST_ENV_VAR", "env-secret")
		defer os.Unsetenv("TEST_ENV_VAR")
		val, err := Resolve(context.Background(), "env://TEST_ENV_VAR")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "env-secret" {
			t.Errorf("expected env-secret, got %q", val)
		}
	})

	t.Run("symvault", func(t *testing.T) {
		val, err := Resolve(context.Background(), "symvault://my.ref")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "symvault-secret" {
			t.Errorf("expected symvault-secret, got %q", val)
		}
	})

	t.Run("keychain", func(t *testing.T) {
		val, err := Resolve(context.Background(), "keychain://my-service/my-account")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "keychain-secret" {
			t.Errorf("expected keychain-secret, got %q", val)
		}
	})

	t.Run("plaintext", func(t *testing.T) {
		val, err := Resolve(context.Background(), "just-a-string")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "just-a-string" {
			t.Errorf("expected just-a-string, got %q", val)
		}
	})
}

func TestIsPlaintext(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plaintext", "just-a-string", true},
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

func TestResolve_DelegationAndErrors(t *testing.T) {
	origSym := symvaultGetFn
	origKey := keychainGetFn
	defer func() {
		symvaultGetFn = origSym
		keychainGetFn = origKey
	}()

	t.Run("symvault_delegation", func(t *testing.T) {
		called := false
		symvaultGetFn = func(ctx context.Context, ref string) (string, error) {
			if ref != "my.ref" {
				t.Errorf("expected my.ref, got %s", ref)
			}
			called = true
			return "delegated-sym", nil
		}
		val, err := Resolve(context.Background(), "symvault://my.ref")
		if err != nil {
			t.Fatal(err)
		}
		if val != "delegated-sym" {
			t.Errorf("expected delegated-sym, got %s", val)
		}
		if !called {
			t.Error("expected symvaultGetFn to be called")
		}
	})

	t.Run("keychain_delegation", func(t *testing.T) {
		called := false
		keychainGetFn = func(ctx context.Context, service, account string) (string, error) {
			if service != "srv" || account != "acc" {
				t.Errorf("expected srv/acc, got %s/%s", service, account)
			}
			called = true
			return "delegated-key", nil
		}
		val, err := Resolve(context.Background(), "keychain://srv/acc")
		if err != nil {
			t.Fatal(err)
		}
		if val != "delegated-key" {
			t.Errorf("expected delegated-key, got %s", val)
		}
		if !called {
			t.Error("expected keychainGetFn to be called")
		}
	})

	t.Run("keychain_invalid_reference", func(t *testing.T) {
		_, err := Resolve(context.Background(), "keychain://invalid-reference-no-slash")
		if err == nil {
			t.Error("expected error for invalid keychain reference, got nil")
		}
		if !strings.Contains(err.Error(), "invalid keychain reference") {
			t.Errorf("expected error message to contain 'invalid keychain reference', got %v", err)
		}
	})

	t.Run("env_empty", func(t *testing.T) {
		_, err := Resolve(context.Background(), "env://NONEXISTENT_VAR_12345")
		if err == nil {
			t.Error("expected error for empty env var, got nil")
		}
		if !strings.Contains(err.Error(), "environment variable") {
			t.Errorf("expected error message to contain 'environment variable', got %v", err)
		}
	})
}

func TestSymvaultGet(t *testing.T) {
	defer func() {
		lookPathFn = exec.LookPath
		symvaultGetFn = symvaultGet
	}()

	t.Run("not_installed", func(t *testing.T) {
		lookPathFn = func(file string) (string, error) {
			return "", exec.ErrNotFound
		}
		_, err := symvaultGet(context.Background(), "my.ref")
		if !errors.Is(err, ErrNotInstalled) {
			t.Errorf("expected ErrNotInstalled, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		lookPathFn = exec.LookPath
		dir := t.TempDir()
		createMockExecutable(t, dir, "symvault", `#!/bin/sh
if [ "$1" = "get" ] && [ "$2" = "my.ref" ] && [ "$3" = "--print" ]; then
	echo "my-secret-val"
else
	echo "invalid args: $*" >&2
	exit 1
fi
`)
		origPath := os.Getenv("PATH")
		defer os.Setenv("PATH", origPath)
		os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

		val, err := symvaultGet(context.Background(), "my.ref")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "my-secret-val" {
			t.Errorf("expected my-secret-val, got %q", val)
		}
	})

	t.Run("error", func(t *testing.T) {
		lookPathFn = exec.LookPath
		dir := t.TempDir()
		createMockExecutable(t, dir, "symvault", `#!/bin/sh
echo "database locked" >&2
exit 1
`)
		origPath := os.Getenv("PATH")
		defer os.Setenv("PATH", origPath)
		os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

		val, err := symvaultGet(context.Background(), "my.ref")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if val != "" {
			t.Errorf("expected empty value on error, got %q", val)
		}
		if !strings.Contains(err.Error(), "database locked") {
			t.Errorf("expected error message to contain 'database locked', got %v", err)
		}
		if strings.Contains(err.Error(), "my-secret-val") {
			t.Error("secret leaked in error message")
		}
	})

	t.Run("error_empty_stderr", func(t *testing.T) {
		lookPathFn = exec.LookPath
		dir := t.TempDir()
		createMockExecutable(t, dir, "symvault", `#!/bin/sh
exit 1
`)
		origPath := os.Getenv("PATH")
		defer os.Setenv("PATH", origPath)
		os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

		_, err := symvaultGet(context.Background(), "my.ref")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "exit status 1") {
			t.Errorf("expected error message to contain 'exit status 1', got %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		lookPathFn = exec.LookPath
		dir := t.TempDir()
		createMockExecutable(t, dir, "symvault", `#!/bin/sh
exit 0
`)
		origPath := os.Getenv("PATH")
		defer os.Setenv("PATH", origPath)
		os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

		_, err := symvaultGet(context.Background(), "my.ref")
		if err == nil {
			t.Fatal("expected error for empty value, got nil")
		}
		if !strings.Contains(err.Error(), "empty value") {
			t.Errorf("expected error message to contain 'empty value', got %v", err)
		}
	})
}

func TestKeychainGet(t *testing.T) {
	defer func() {
		lookPathFn = exec.LookPath
		keychainGetFn = keychainGet
	}()

	t.Run("not_installed", func(t *testing.T) {
		lookPathFn = func(file string) (string, error) {
			return "", exec.ErrNotFound
		}
		_, err := keychainGet(context.Background(), "service", "account")
		if !errors.Is(err, ErrNotInstalled) {
			t.Errorf("expected ErrNotInstalled, got %v", err)
		}
	})

	if runtime.GOOS == "darwin" {
		t.Run("success", func(t *testing.T) {
			lookPathFn = exec.LookPath
			dir := t.TempDir()
			createMockExecutable(t, dir, "security", `#!/bin/sh
if [ "$1" = "find-generic-password" ] && [ "$2" = "-s" ] && [ "$3" = "my-service" ] && [ "$4" = "-w" ] && [ "$5" = "-a" ] && [ "$6" = "my-account" ]; then
	echo "my-keychain-secret"
else
	echo "invalid args: $*" >&2
	exit 1
fi
`)
			origPath := os.Getenv("PATH")
			defer os.Setenv("PATH", origPath)
			os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

			val, err := keychainGet(context.Background(), "my-service", "my-account")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val != "my-keychain-secret" {
				t.Errorf("expected my-keychain-secret, got %q", val)
			}
		})

		t.Run("success_no_account", func(t *testing.T) {
			lookPathFn = exec.LookPath
			dir := t.TempDir()
			createMockExecutable(t, dir, "security", `#!/bin/sh
if [ "$1" = "find-generic-password" ] && [ "$2" = "-s" ] && [ "$3" = "my-service" ] && [ "$4" = "-w" ] && [ "$#" -eq 4 ]; then
	echo "my-keychain-secret-no-acc"
else
	echo "invalid args: $*" >&2
	exit 1
fi
`)
			origPath := os.Getenv("PATH")
			defer os.Setenv("PATH", origPath)
			os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

			val, err := keychainGet(context.Background(), "my-service", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val != "my-keychain-secret-no-acc" {
				t.Errorf("expected my-keychain-secret-no-acc, got %q", val)
			}
		})

		t.Run("error", func(t *testing.T) {
			lookPathFn = exec.LookPath
			dir := t.TempDir()
			createMockExecutable(t, dir, "security", `#!/bin/sh
exit 1
`)
			origPath := os.Getenv("PATH")
			defer os.Setenv("PATH", origPath)
			os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

			_, err := keychainGet(context.Background(), "my-service", "my-account")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "keychain entry not found") {
				t.Errorf("expected error message to contain 'keychain entry not found', got %v", err)
			}
			if strings.Contains(err.Error(), "my-keychain-secret") {
				t.Error("secret leaked in error message")
			}
		})
	}
}
