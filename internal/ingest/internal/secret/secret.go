// Package secret provides resolution for secret strings (e.g. passwords) from various backends.
// Supported schemes:
//   - symvault://<ref>
//   - env://<var_name>
//   - keychain://<service>/<account>
//   - (no scheme) plaintext fallback
package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNotInstalled is returned when a backend's CLI is not available.
var ErrNotInstalled = errors.New("backend CLI not installed")

// Indirection points so tests can stub the backends.
var (
	symvaultGetFn = symvaultGet
	keychainGetFn = keychainGet
	lookPathFn    = exec.LookPath
)

// IsPlaintext reports whether s would resolve via the plaintext fallback,
// i.e. it does not use any of the symvault://, env:// or keychain:// schemes.
func IsPlaintext(s string) bool {
	return !strings.HasPrefix(s, "env://") &&
		!strings.HasPrefix(s, "symvault://") &&
		!strings.HasPrefix(s, "keychain://")
}

// Resolve takes a secret configuration string and resolves it.
// It supports symvault://, env://, keychain:// prefixes. If no prefix
// is matched, it returns the string as a plaintext secret.
func Resolve(ctx context.Context, s string) (string, error) {
	if strings.HasPrefix(s, "env://") {
		envVar := strings.TrimPrefix(s, "env://")
		val := os.Getenv(envVar)
		if val == "" {
			return "", fmt.Errorf("environment variable %q is empty or not set", envVar)
		}
		return val, nil
	}
	if strings.HasPrefix(s, "symvault://") {
		ref := strings.TrimPrefix(s, "symvault://")
		return symvaultGetFn(ctx, ref)
	}
	if strings.HasPrefix(s, "keychain://") {
		ref := strings.TrimPrefix(s, "keychain://")
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid keychain reference %q, expected keychain://service/account", s)
		}
		return keychainGetFn(ctx, parts[0], parts[1])
	}
	return s, nil
}

// symvaultGet shells out to `symvault get <ref> --print` and returns the value.
func symvaultGet(ctx context.Context, ref string) (string, error) {
	if _, err := lookPathFn("symvault"); err != nil {
		return "", fmt.Errorf("%w: symvault", ErrNotInstalled)
	}
	cmd := exec.CommandContext(ctx, "symvault", "get", ref, "--print")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	v := strings.TrimRight(out.String(), "\r\n")
	if v == "" {
		return "", fmt.Errorf("symvault returned an empty value for %q", ref)
	}
	return v, nil
}

// keychainGet reads a password from the macOS Keychain.
func keychainGet(ctx context.Context, service, account string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("%w: keychain is macOS-only", ErrNotInstalled)
	}
	if _, err := lookPathFn("security"); err != nil {
		return "", fmt.Errorf("%w: security", ErrNotInstalled)
	}
	args := []string{"find-generic-password", "-s", service, "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}
	cmd := exec.CommandContext(ctx, "security", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("keychain entry not found (service %q account %q)", service, account)
	}
	return strings.TrimRight(out.String(), "\r\n"), nil
}
