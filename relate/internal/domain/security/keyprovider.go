// Package security defines the key-provider abstraction, redaction rules
// and audit/backup/erase domain types for the beta privacy boundary. See
// docs/PRIVACY.md for the full data-handling policy.
package security

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/term"
)

// ErrKeyUnavailable is returned by a KeyProvider that has no key material
// to offer right now (SymVault not installed, no passphrase configured,
// ...). It is not fatal — callers try the next provider in a Chain.
var ErrKeyUnavailable = errors.New("security: key unavailable")

// KeyProvider resolves the passphrase used to derive a backup/export
// encryption key. Implementations must be safe to call in tests with no
// real secret store present — see StaticKeyProvider.
type KeyProvider interface {
	// Resolve returns the passphrase and a short, non-sensitive label
	// identifying where it came from (for diagnostics — never the
	// passphrase itself).
	Resolve(ctx context.Context) (passphrase []byte, source string, err error)
}

// Chain tries each provider in order and returns the first success. If
// every provider reports ErrKeyUnavailable, Chain reports
// ErrKeyUnavailable too; any other error aborts immediately.
type Chain []KeyProvider

func (c Chain) Resolve(ctx context.Context) ([]byte, string, error) {
	for _, p := range c {
		key, source, err := p.Resolve(ctx)
		if err == nil {
			return key, source, nil
		}
		if !errors.Is(err, ErrKeyUnavailable) {
			return nil, "", err
		}
	}
	return nil, "", ErrKeyUnavailable
}

// StaticKeyProvider wraps an explicitly supplied passphrase (a CLI flag,
// a test fixture). This is the standalone fallback that always works with
// no external tool installed.
type StaticKeyProvider struct {
	Passphrase []byte
}

func (p StaticKeyProvider) Resolve(context.Context) ([]byte, string, error) {
	if len(p.Passphrase) == 0 {
		return nil, "", ErrKeyUnavailable
	}
	return p.Passphrase, "explicit", nil
}

// DefaultPassphraseEnvVar is the environment variable EnvKeyProvider reads
// by default.
const DefaultPassphraseEnvVar = "SYMRELATE_BACKUP_PASSPHRASE"

// EnvKeyProvider reads a passphrase from an environment variable.
type EnvKeyProvider struct {
	VarName string // defaults to DefaultPassphraseEnvVar when empty
}

func (p EnvKeyProvider) Resolve(context.Context) ([]byte, string, error) {
	name := p.VarName
	if name == "" {
		name = DefaultPassphraseEnvVar
	}
	v := os.Getenv(name)
	if v == "" {
		return nil, "", ErrKeyUnavailable
	}
	return []byte(v), "env:" + name, nil
}

// DefaultSymVaultKeyName is the key name symrelate asks SymVault for.
const DefaultSymVaultKeyName = "symrelate-backup"

// SymVaultKeyProvider resolves a passphrase from a local SymVault
// installation, detected at runtime via a PATH lookup only — never a
// compile-time dependency (see ARCHITECTURE.md's standalone-first rule).
// When symvault is not installed, Resolve returns ErrKeyUnavailable
// immediately so callers fall back to the next provider: SymVault is
// always a convenience, never a requirement.
type SymVaultKeyProvider struct {
	KeyName string // defaults to DefaultSymVaultKeyName when empty
}

func (p SymVaultKeyProvider) Resolve(ctx context.Context) ([]byte, string, error) {
	binPath, err := exec.LookPath("symvault")
	if err != nil {
		return nil, "", ErrKeyUnavailable
	}
	keyName := p.KeyName
	if keyName == "" {
		keyName = DefaultSymVaultKeyName
	}
	out, err := exec.CommandContext(ctx, binPath, "get", keyName).Output()
	if err != nil {
		return nil, "", ErrKeyUnavailable
	}
	passphrase := bytes.TrimSpace(out)
	if len(passphrase) == 0 {
		return nil, "", ErrKeyUnavailable
	}
	return passphrase, "symvault", nil
}

// TerminalKeyProvider reads a passphrase from the terminal without echo.
// It returns ErrKeyUnavailable when stdin is not a terminal (e.g. piped
// input, cron, CI) so callers fall back to the next provider silently.
type TerminalKeyProvider struct {
	Prompt    string          // defaults to "Passphrase: " when empty
	Confirm   bool            // when true, prompt a second time and reject mismatches
	StdinFunc func() *os.File // override for testing; nil means os.Stdin
}

func (p TerminalKeyProvider) Resolve(_ context.Context) ([]byte, string, error) {
	fd := int(p.getStdin().Fd())
	if !term.IsTerminal(fd) {
		return nil, "", ErrKeyUnavailable
	}

	prompt := p.Prompt
	if prompt == "" {
		prompt = "Passphrase: "
	}

	fmt.Fprint(os.Stderr, prompt)
	passphrase, err := term.ReadPassword(fd)
	if err != nil {
		return nil, "", fmt.Errorf("terminal read failed: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	if p.Confirm {
		confirmPrompt := "Confirm passphrase: "
		fmt.Fprint(os.Stderr, confirmPrompt)
		confirm, err := term.ReadPassword(fd)
		if err != nil {
			return nil, "", fmt.Errorf("terminal read failed: %w", err)
		}
		fmt.Fprintln(os.Stderr)

		if !bytes.Equal(passphrase, confirm) {
			return nil, "", fmt.Errorf("passphrases do not match")
		}
	}

	if len(passphrase) == 0 {
		return nil, "", ErrKeyUnavailable
	}
	return passphrase, "terminal", nil
}

func (p TerminalKeyProvider) getStdin() *os.File {
	if p.StdinFunc != nil {
		return p.StdinFunc()
	}
	return os.Stdin
}

// DefaultKeyProviders returns the standard resolution order: an explicit
// passphrase (if any), then the environment variable, then SymVault if
// installed, then an interactive terminal prompt. Every step before the
// terminal works with zero external dependencies, satisfying the
// documented standalone fallback.
func DefaultKeyProviders(explicit []byte) Chain {
	return Chain{
		StaticKeyProvider{Passphrase: explicit},
		EnvKeyProvider{},
		SymVaultKeyProvider{},
		TerminalKeyProvider{},
	}
}

// DefaultKeyProvidersConfirm returns the same chain but with a confirming
// terminal prompt (used by backup create to catch typos).
func DefaultKeyProvidersConfirm(explicit []byte) Chain {
	return Chain{
		StaticKeyProvider{Passphrase: explicit},
		EnvKeyProvider{},
		SymVaultKeyProvider{},
		TerminalKeyProvider{Confirm: true},
	}
}
