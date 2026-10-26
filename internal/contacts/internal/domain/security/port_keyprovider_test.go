package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// This test executable acts as the fake symvault. No real symvault or OS
// credential service is ever consulted: PATH is a fresh private directory.
func init() {
	if os.Getenv("SYMDESK_CONTACTS_PORT_KEY_HELPER") == "1" {
		if err := os.WriteFile(os.Getenv("SYMDESK_CONTACTS_PORT_KEY_ARGS"), []byte(os.Args[1]+"\n"+os.Args[2]), 0600); err != nil {
			os.Exit(3)
		}
		b, err := hex.DecodeString(os.Getenv("SYMDESK_CONTACTS_PORT_KEY_OUTPUT"))
		if err != nil {
			os.Exit(4)
		}
		_, _ = os.Stdout.Write(b)
		if os.Getenv("SYMDESK_CONTACTS_PORT_KEY_FAIL") == "1" {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

type portKeyProvider struct {
	index  int
	visits *[]int
	key    []byte
	source string
	err    error
}

func (p portKeyProvider) Resolve(context.Context) ([]byte, string, error) {
	*p.visits = append(*p.visits, p.index)
	return p.key, p.source, p.err
}
func TestPortContactsKeyProviderContract(t *testing.T) {
	ctx := context.Background()
	cases := []any{}
	t.Setenv(DefaultPassphraseEnvVar, "")
	record := func(name string, input any, provider KeyProvider, visits *[]int) {
		key, source, err := provider.Resolve(ctx)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		var calls any
		if visits != nil {
			calls = *visits
		}
		cases = append(cases, map[string]any{"name": name, "input": input, "key_hex": hex.EncodeToString(key), "key_nil": key == nil, "source": source, "error": msg, "unavailable": errors.Is(err, ErrKeyUnavailable), "visits": calls})
	}
	for _, key := range [][]byte{nil, {}, []byte("fixture-only-key"), []byte(" \n"), {0, 255, 1}} {
		record("static", map[string]any{"key_hex": hex.EncodeToString(key)}, StaticKeyProvider{key}, nil)
	}
	for _, name := range []string{"", "PORT_CONTACTS_DUMMY_KEY"} {
		envName := name
		if envName == "" {
			envName = DefaultPassphraseEnvVar
		}
		for _, value := range []string{"", "fixture-only-env", " \n"} {
			t.Setenv(envName, value)
			record("env", map[string]any{"name": name, "value": value}, EnvKeyProvider{name}, nil)
		}
	}
	for _, confirm := range []bool{false, true} {
		for _, explicit := range [][]byte{[]byte("fixture-explicit"), nil} {
			t.Setenv(DefaultPassphraseEnvVar, "fixture-env")
			chain := DefaultKeyProviders(explicit)
			if confirm {
				chain = DefaultKeyProvidersConfirm(explicit)
			}
			record("default", map[string]any{"explicit_hex": hex.EncodeToString(explicit), "env": "fixture-env", "confirm": confirm}, chain, nil)
		}
	}
	for _, states := range [][]string{{}, {"unavailable"}, {"unavailable", "ok", "fatal"}, {"fatal", "ok"}, {"wrapped_unavailable", "ok"}, {"empty_ok", "fatal"}, {"unavailable", "unavailable"}} {
		visits := []int{}
		chain := Chain{}
		for i, state := range states {
			p := portKeyProvider{index: i, visits: &visits}
			switch state {
			case "unavailable":
				p.err = ErrKeyUnavailable
			case "wrapped_unavailable":
				p.err = fmt.Errorf("wrapped: %w", ErrKeyUnavailable)
			case "fatal":
				p.err = errors.New("fixture provider failed")
			case "ok":
				p.key = []byte("fixture-chain")
				p.source = "fixture"
			case "empty_ok":
				p.key = []byte{}
				p.source = "empty"
			}
			chain = append(chain, p)
		}
		record("chain", map[string]any{"states": states}, chain, &visits)
	}
	read, write, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	if e := write.Close(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := read.Close(); e != nil {
			t.Error(e)
		}
	})
	record("terminal_non_tty", nil, TerminalKeyProvider{Confirm: true, StdinFunc: func() *os.File { return read }}, nil)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	record("symvault_absent", nil, SymVaultKeyProvider{}, nil)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	binary, e := os.ReadFile(exe)
	if e != nil {
		t.Fatal(e)
	}
	name := "symvault"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if e := os.WriteFile(filepath.Join(dir, name), binary, 0700); e != nil {
		t.Fatal(e)
	}
	argsFile := filepath.Join(dir, "args")
	t.Setenv("SYMDESK_CONTACTS_PORT_KEY_HELPER", "1")
	t.Setenv("SYMDESK_CONTACTS_PORT_KEY_ARGS", argsFile)
	for _, keyName := range []string{"", "custom-key"} {
		for _, out := range [][]byte{nil, []byte(" \n"), []byte(" \u0085fixture-symvault\u2003\n"), {255, 0, 1}} {
			for _, fail := range []bool{false, true} {
				t.Setenv("SYMDESK_CONTACTS_PORT_KEY_OUTPUT", hex.EncodeToString(out))
				f := "0"
				if fail {
					f = "1"
				}
				t.Setenv("SYMDESK_CONTACTS_PORT_KEY_FAIL", f)
				record("symvault", map[string]any{"name": keyName, "stdout_hex": hex.EncodeToString(out), "fail": fail}, SymVaultKeyProvider{keyName}, nil)
				args, e := os.ReadFile(argsFile)
				if e != nil {
					t.Fatal(e)
				}
				cases[len(cases)-1].(map[string]any)["args"] = string(args)
			}
		}
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	b, e := os.ReadFile(filepath.Join(root, "internal/contacts/internal/domain/security/keyprovider.go"))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(b)
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": map[string]string{"internal/contacts/internal/domain/security/keyprovider.go": hex.EncodeToString(hash[:])}, "cases": cases}
	data, e := json.MarshalIndent(fixture, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/keyprovider.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.WriteFile(path, data, 0644); e != nil {
			t.Fatal(e)
		}
	} else {
		expected, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts keyprovider fixture drift")
		}
	}
	fmt.Printf("contacts keyprovider: %d cases\n", len(cases))
}
