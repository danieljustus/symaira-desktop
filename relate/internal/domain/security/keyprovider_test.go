package security

import (
	"context"
	"errors"
	"os"
	"testing"
)

type keyProviderFunc func(context.Context) ([]byte, string, error)

func (f keyProviderFunc) Resolve(ctx context.Context) ([]byte, string, error) {
	return f(ctx)
}

func TestChain_UsesFirstSuccessAndHandlesUnavailableProviders(t *testing.T) {
	ctx := context.Background()
	key, source, err := Chain{
		StaticKeyProvider{},
		StaticKeyProvider{Passphrase: []byte("passphrase")},
		keyProviderFunc(func(context.Context) ([]byte, string, error) {
			t.Fatal("provider after success was called")
			return nil, "", nil
		}),
	}.Resolve(ctx)
	if err != nil || string(key) != "passphrase" || source != "explicit" {
		t.Fatalf("successful chain = (%q, %q, %v), want passphrase/explicit/nil", key, source, err)
	}

	if _, _, err := (Chain{StaticKeyProvider{}}).Resolve(ctx); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("all-unavailable chain error = %v, want ErrKeyUnavailable", err)
	}
}

func TestChain_StopsOnUnexpectedError(t *testing.T) {
	want := errors.New("provider failed")
	_, _, err := Chain{keyProviderFunc(func(context.Context) ([]byte, string, error) {
		return nil, "", want
	}), StaticKeyProvider{Passphrase: []byte("unused")}}.Resolve(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("chain error = %v, want %v", err, want)
	}
}

func TestStaticAndEnvKeyProviders(t *testing.T) {
	if _, _, err := (StaticKeyProvider{}).Resolve(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("empty static provider error = %v, want unavailable", err)
	}
	key, source, err := (StaticKeyProvider{Passphrase: []byte("static")}).Resolve(context.Background())
	if err != nil || string(key) != "static" || source != "explicit" {
		t.Fatalf("static provider = (%q, %q, %v)", key, source, err)
	}

	t.Setenv("TEST_SYMRELATE_PASSPHRASE", "environment")
	key, source, err = (EnvKeyProvider{VarName: "TEST_SYMRELATE_PASSPHRASE"}).Resolve(context.Background())
	if err != nil || string(key) != "environment" || source != "env:TEST_SYMRELATE_PASSPHRASE" {
		t.Fatalf("env provider = (%q, %q, %v)", key, source, err)
	}
	if _, _, err := (EnvKeyProvider{VarName: "MISSING_SYMRELATE_PASSPHRASE"}).Resolve(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("missing env provider error = %v, want unavailable", err)
	}
}

func TestSymVaultAndTerminalProvidersUnavailableWithoutExternalInput(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, err := (SymVaultKeyProvider{}).Resolve(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("missing SymVault error = %v, want unavailable", err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	if _, _, err := (TerminalKeyProvider{StdinFunc: func() *os.File { return readEnd }}).Resolve(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("non-terminal provider error = %v, want unavailable", err)
	}
}

func TestDefaultKeyProvidersIncludeExpectedFallbacks(t *testing.T) {
	chain := DefaultKeyProviders([]byte("explicit"))
	if len(chain) != 4 {
		t.Fatalf("DefaultKeyProviders() length = %d, want 4", len(chain))
	}
	if _, ok := chain[0].(StaticKeyProvider); !ok {
		t.Fatalf("first provider = %T, want StaticKeyProvider", chain[0])
	}

	confirmed := DefaultKeyProvidersConfirm(nil)
	terminal, ok := confirmed[3].(TerminalKeyProvider)
	if !ok || !terminal.Confirm {
		t.Fatalf("confirm chain terminal provider = %#v, want confirming TerminalKeyProvider", confirmed[3])
	}
}
