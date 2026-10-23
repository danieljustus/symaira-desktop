package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const identityProviderCLIContractPath = "testdata/port/room/identity-provider-cli.json"

// identityProviderCLIContract pins `identity.Load`'s resolution chain
// (SYMROOM_IDENTITY_KEY, `symvault get`, macOS `security`, identity file)
// through the real Go process, with fake provider executables on a scoped PATH
// and a disposable HOME/XDG root per case.
type identityProviderCLIContract struct {
	SchemaVersion  int                           `json:"schema_version"`
	OracleRevision string                        `json:"oracle_revision"`
	SourceHashes   map[string]string             `json:"source_hashes"`
	Cases          []identityProviderCLICaseData `json:"cases"`
}

type identityProviderStub struct {
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Exit   int    `json:"exit,omitempty"`
}

// identityProviderFile carries bytes as text when they are valid UTF-8 and as
// hex otherwise, because encoding/json would replace invalid bytes.
type identityProviderFile struct {
	Name       string `json:"name"`
	Content    string `json:"content,omitempty"`
	ContentHex string `json:"content_hex,omitempty"`
}

func newIdentityProviderFile(name string, data []byte) identityProviderFile {
	if utf8.Valid(data) {
		return identityProviderFile{Name: name, Content: string(data)}
	}
	return identityProviderFile{Name: name, ContentHex: hex.EncodeToString(data)}
}

func (f identityProviderFile) bytes() []byte {
	if f.ContentHex != "" {
		data, _ := hex.DecodeString(f.ContentHex)
		return data
	}
	return []byte(f.Content)
}

type identityProviderCLICaseData struct {
	Name       string                 `json:"name"`
	Args       []string               `json:"args"`
	EnvKey     *string                `json:"env_key,omitempty"`
	Symvault   *identityProviderStub  `json:"symvault,omitempty"`
	Security   *identityProviderStub  `json:"security,omitempty"`
	DarwinOnly bool                   `json:"darwin_only,omitempty"`
	Files      []identityProviderFile `json:"identity_files,omitempty"`
	ExitCode   int                    `json:"exit_code"`
	Stdout     string                 `json:"stdout"`
	Stderr     string                 `json:"stderr"`
	ToolCalls  string                 `json:"tool_calls"`
	FinalFiles []identityProviderFile `json:"final_files"`
}

func TestPortIdentityProviderCLIContract(t *testing.T) {
	root := identityCLIRoot(t)
	fixture, err := makeIdentityProviderCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, identityProviderCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if runtime.GOOS != "darwin" {
			t.Fatal("generate the identity provider fixture on darwin so the Keychain chain is recorded")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", identityProviderCLIContractPath)
		return
	}
	if runtime.GOOS != "darwin" {
		// The fixture was recorded on darwin; Keychain cases cannot be
		// regenerated elsewhere, so compare only the portable cases.
		var committed identityProviderCLIContract
		raw, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			t.Fatalf("read %s: %v", identityProviderCLIContractPath, err)
		}
		if err := json.Unmarshal(raw, &committed); err != nil {
			t.Fatal(err)
		}
		replayed := 0
		for i, c := range fixture.Cases {
			if committed.Cases[i].DarwinOnly {
				t.Logf("SKIP %s: macOS Keychain chain", c.Name)
				continue
			}
			want, _ := json.Marshal(committed.Cases[i])
			got, _ := json.Marshal(c)
			if !bytes.Equal(want, got) {
				t.Fatalf("Go identity provider case %s differs from the fixture\nwant %s\ngot  %s", c.Name, want, got)
			}
			replayed++
		}
		if replayed == 0 {
			t.Fatal("no identity provider case replayed")
		}
		t.Logf("identity provider cases: %d total, %d replayed", len(fixture.Cases), replayed)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", identityProviderCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go identity provider CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

type providerKey struct {
	seed    []byte
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func newProviderKey(label string) providerKey {
	seed := sha256.Sum256([]byte("symroom-identity-provider-fixture-" + label))
	private := ed25519.NewKeyFromSeed(seed[:])
	return providerKey{seed: seed[:], private: private, public: private.Public().(ed25519.PublicKey)}
}

func (k providerKey) seedHex() string    { return hex.EncodeToString(k.seed) }
func (k providerKey) privateHex() string { return hex.EncodeToString(k.private) }

func (k providerKey) storedFile(name, storedName string) identityProviderFile {
	stored := identity.StoredIdentity{
		Name: storedName, MemberID: identity.ComputeMemberID(k.public),
		PublicKey: hex.EncodeToString(k.public), PrivateKey: k.privateHex(),
	}
	encoded, _ := json.MarshalIndent(stored, "", "  ") //nolint:gosec // deterministic synthetic fixture keys
	return identityProviderFile{Name: name + ".json", Content: string(encoded)}
}

func strPtr(s string) *string { return &s }

func makeIdentityProviderCLIContract(t *testing.T, root string) (identityProviderCLIContract, error) {
	t.Helper()
	fixture := identityProviderCLIContract{
		SchemaVersion: 1, OracleRevision: fixtureoracle.Current().Commit,
		SourceHashes: map[string]string{
			"cmd/symroom/cmd_identity.go":        identityCLIFileHash(t, root, "cmd/symroom/cmd_identity.go"),
			"internal/room/identity/identity.go": identityCLIFileHash(t, root, "internal/room/identity/identity.go"),
		},
	}
	envKey, vaultKey, secKey, fileKey, other := newProviderKey("env"), newProviderKey("vault"), newProviderKey("security"), newProviderKey("file"), newProviderKey("other")
	mismatched := hex.EncodeToString(append(append([]byte{}, envKey.seed...), other.public...))
	vaultOK := &identityProviderStub{Stdout: vaultKey.privateHex() + "\n"}
	fileOK := fileKey.storedFile("probe", "probe")
	name := "probe"
	show := []string{"identity", "show", name}
	storedJSON := func(mutate func(map[string]any)) identityProviderFile {
		var m map[string]any
		_ = json.Unmarshal([]byte(fileOK.Content), &m)
		mutate(m)
		out, _ := json.MarshalIndent(m, "", "  ")
		return identityProviderFile{Name: "probe.json", Content: string(out)}
	}
	raw := func(content string) []identityProviderFile {
		return []identityProviderFile{newIdentityProviderFile("probe.json", []byte(content))}
	}
	one := func(f identityProviderFile) []identityProviderFile { return []identityProviderFile{f} }

	cases := []identityProviderCLICaseData{
		// Chain 1: SYMROOM_IDENTITY_KEY.
		{Name: "env-seed-wins", Args: show, EnvKey: strPtr(envKey.seedHex()), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-private64-wins", Args: show, EnvKey: strPtr(envKey.privateHex()), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-private64-mismatched-public-half", Args: show, EnvKey: strPtr(mismatched), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-private64-mismatched-export-public", Args: []string{"identity", "export", "--public", name}, EnvKey: strPtr(mismatched)},
		{Name: "env-padded-whitespace", Args: show, EnvKey: strPtr(" \t" + envKey.seedHex() + "\r\n ")},
		{Name: "env-uppercase-hex", Args: show, EnvKey: strPtr(strings.ToUpper(envKey.seedHex()))},
		{Name: "env-odd-length-falls-to-symvault", Args: show, EnvKey: strPtr(envKey.seedHex() + "a"), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-31-bytes-falls-to-symvault", Args: show, EnvKey: strPtr(envKey.seedHex()[:62]), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-33-bytes-falls-to-symvault", Args: show, EnvKey: strPtr(envKey.seedHex() + "00"), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-65-bytes-falls-to-file", Args: show, EnvKey: strPtr(envKey.privateHex() + "00"), Files: one(fileOK)},
		{Name: "env-non-hex-falls-to-symvault", Args: show, EnvKey: strPtr(strings.Repeat("zz", 32)), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-0x-prefix-falls-to-file", Args: show, EnvKey: strPtr("0x" + envKey.seedHex()), Files: one(fileOK)},
		{Name: "env-inner-space-falls-to-file", Args: show, EnvKey: strPtr(envKey.seedHex()[:32] + " " + envKey.seedHex()[32:]), Files: one(fileOK)},
		{Name: "env-empty-is-unset", Args: show, EnvKey: strPtr(""), Symvault: vaultOK, Files: one(fileOK)},
		{Name: "env-whitespace-only-falls-to-file", Args: show, EnvKey: strPtr("   "), Files: one(fileOK)},
		{Name: "env-seed-export-public", Args: []string{"identity", "export", "--public", name}, EnvKey: strPtr(envKey.seedHex())},
		{Name: "env-empty-name-argument", Args: []string{"identity", "show", ""}, EnvKey: strPtr(envKey.seedHex())},

		// Chain 2: `symvault get symroom/identities/<name>`.
		{Name: "symvault-valid-private64", Args: show, Symvault: vaultOK, Files: one(fileOK)},
		{Name: "symvault-export-public", Args: []string{"identity", "export", "--public", name}, Symvault: vaultOK},
		{Name: "symvault-no-newline", Args: show, Symvault: &identityProviderStub{Stdout: vaultKey.privateHex()}},
		{Name: "symvault-padded-crlf", Args: show, Symvault: &identityProviderStub{Stdout: "  \r\n" + vaultKey.privateHex() + " \r\n\r\n"}},
		{Name: "symvault-uppercase-hex", Args: show, Symvault: &identityProviderStub{Stdout: strings.ToUpper(vaultKey.privateHex()) + "\n"}},
		{Name: "symvault-stderr-noise-ignored", Args: show, Symvault: &identityProviderStub{Stdout: vaultKey.privateHex() + "\n", Stderr: "warning: noisy\n"}},
		{Name: "symvault-mismatched-public-half", Args: show, Symvault: &identityProviderStub{Stdout: hex.EncodeToString(append(append([]byte{}, vaultKey.seed...), other.public...)) + "\n"}, Files: one(fileOK)},
		{Name: "symvault-seed32-rejected-falls-to-file", Args: show, Symvault: &identityProviderStub{Stdout: vaultKey.seedHex() + "\n"}, Files: one(fileOK)},
		{Name: "symvault-65-bytes-falls-to-file", Args: show, Symvault: &identityProviderStub{Stdout: vaultKey.privateHex() + "00\n"}, Files: one(fileOK)},
		{Name: "symvault-nonzero-exit-ignores-valid-stdout", Args: show, Symvault: &identityProviderStub{Stdout: vaultKey.privateHex() + "\n", Exit: 3}, Files: one(fileOK)},
		{Name: "symvault-garbage-falls-to-file", Args: show, Symvault: &identityProviderStub{Stdout: "no such secret\n"}, Files: one(fileOK)},
		{Name: "symvault-empty-falls-to-file", Args: show, Symvault: &identityProviderStub{}, Files: one(fileOK)},
		{Name: "symvault-two-lines-falls-to-file", Args: show, Symvault: &identityProviderStub{Stdout: vaultKey.privateHex() + "\n" + vaultKey.privateHex() + "\n"}, Files: one(fileOK)},
		{Name: "symvault-garbage-no-file-not-found", Args: show, Symvault: &identityProviderStub{Stdout: "nope\n"}},
		{Name: "symvault-nonzero-no-file-not-found", Args: show, Symvault: &identityProviderStub{Exit: 1}},
		{Name: "symvault-name-with-space", Args: []string{"identity", "show", "my id"}, Symvault: vaultOK},
		{Name: "symvault-name-with-slash", Args: []string{"identity", "show", "a/b"}, Symvault: &identityProviderStub{Stdout: "nope\n"}},
		{Name: "symvault-empty-name-argument", Args: []string{"identity", "show", ""}, Symvault: &identityProviderStub{Stdout: "nope\n"}},
		{Name: "symvault-name-with-dashes", Args: []string{"identity", "show", "--"}, Symvault: vaultOK},
		{Name: "symvault-wins-over-file-name-field", Args: show, Symvault: vaultOK, Files: one(fileKey.storedFile("probe", "stored-name"))},

		// Chain 3: macOS Keychain (`security`), only exercised on darwin.
		{Name: "security-valid-private64", Args: show, Security: &identityProviderStub{Stdout: secKey.privateHex() + "\n"}, Files: one(fileOK), DarwinOnly: true},
		{Name: "security-after-failing-symvault", Args: show, Symvault: &identityProviderStub{Stdout: "nope\n"}, Security: &identityProviderStub{Stdout: secKey.privateHex() + "\n"}, DarwinOnly: true},
		{Name: "symvault-wins-over-security", Args: show, Symvault: vaultOK, Security: &identityProviderStub{Stdout: secKey.privateHex() + "\n"}, DarwinOnly: true},
		{Name: "security-seed32-rejected-falls-to-file", Args: show, Security: &identityProviderStub{Stdout: secKey.seedHex() + "\n"}, Files: one(fileOK), DarwinOnly: true},
		{Name: "security-nonzero-exit-falls-to-file", Args: show, Security: &identityProviderStub{Stdout: secKey.privateHex() + "\n", Exit: 44}, Files: one(fileOK), DarwinOnly: true},
		{Name: "security-garbage-no-file-not-found", Args: show, Security: &identityProviderStub{Stdout: "xx\n"}, DarwinOnly: true},
		{Name: "security-name-with-space", Args: []string{"identity", "show", "my id"}, Security: &identityProviderStub{Stdout: secKey.privateHex() + "\n"}, DarwinOnly: true},
		{Name: "env-wins-over-security", Args: show, EnvKey: strPtr(envKey.seedHex()), Security: &identityProviderStub{Stdout: secKey.privateHex() + "\n"}, DarwinOnly: true},

		// Chain 4: identity file.
		{Name: "file-valid-stored-name-differs", Args: show, Files: one(fileKey.storedFile("probe", "stored-name"))},
		{Name: "file-export-public", Args: []string{"identity", "export", "--public", name}, Files: one(fileOK)},
		{Name: "file-missing", Args: show},
		{Name: "file-bad-private-hex", Args: show, Files: one(storedJSON(func(m map[string]any) { m["private_key"] = "zz" }))},
		{Name: "file-short-private", Args: show, Files: one(storedJSON(func(m map[string]any) { m["private_key"] = fileKey.seedHex() }))},
		{Name: "file-bad-public-hex", Args: show, Files: one(storedJSON(func(m map[string]any) { m["public_key"] = "zz" }))},
		{Name: "file-short-public", Args: show, Files: one(storedJSON(func(m map[string]any) { m["public_key"] = hex.EncodeToString(fileKey.public[:16]) }))},
		{Name: "file-public-private-mismatch-accepted", Args: show, Files: one(storedJSON(func(m map[string]any) {
			m["public_key"] = hex.EncodeToString(other.public)
			m["member_id"] = "mem_not_derived"
		}))},
		{Name: "file-uppercase-hex", Args: show, Files: one(storedJSON(func(m map[string]any) {
			m["private_key"] = strings.ToUpper(fileKey.privateHex())
			m["public_key"] = strings.ToUpper(hex.EncodeToString(fileKey.public))
		}))},
		{Name: "file-missing-member-id", Args: show, Files: one(storedJSON(func(m map[string]any) { delete(m, "member_id") }))},
		{Name: "file-null-fields-invalid-key", Args: show, Files: raw(`{"name":null,"member_id":null,"public_key":null,"private_key":null}`)},
		{Name: "file-unknown-fields-ignored", Args: show, Files: one(storedJSON(func(m map[string]any) { m["extra"] = []any{1, "x"} }))},
		{Name: "file-case-insensitive-keys", Args: show, Files: raw(strings.NewReplacer(`"name"`, `"NAME"`, `"member_id"`, `"Member_ID"`, `"public_key"`, `"PUBLIC_KEY"`, `"private_key"`, `"Private_Key"`).Replace(fileOK.Content))},
		{Name: "file-kelvin-sign-key-folds", Args: show, Files: raw(`{"name":"kelvin","public_key":"` + hex.EncodeToString(fileKey.public) + `","private_\u212aey":"` + fileKey.privateHex() + `","\u212a":1}`)},
		{Name: "file-long-s-key-does-not-fold-to-k", Args: show, Files: raw(`{"name":"long","public_key":"` + hex.EncodeToString(fileKey.public) + `","private_\u017fey":"` + fileKey.privateHex() + `"}`)},
		{Name: "file-duplicate-key-last-wins", Args: show, Files: raw(`{"name":"first","member_id":"m","public_key":"` + hex.EncodeToString(fileKey.public) + `","private_key":"` + fileKey.privateHex() + `","name":"last"}`)},
		{Name: "file-trailing-whitespace", Args: show, Files: raw(fileOK.Content + "\n\n \t\n")},
		{Name: "file-empty", Args: show, Files: raw("")},
		{Name: "file-malformed-json", Args: show, Files: raw("{not json")},
		{Name: "file-truncated-json", Args: show, Files: raw(`{"name":"probe","member_id":`)},
		{Name: "file-trailing-garbage", Args: show, Files: raw(fileOK.Content + " x")},
		{Name: "file-utf8-bom", Args: show, Files: raw("\ufeff" + fileOK.Content)},
		{Name: "file-wrong-type-private", Args: show, Files: raw(`{"name":"probe","member_id":"m","public_key":"00","private_key":7}`)},
		{Name: "file-wrong-type-name", Args: show, Files: raw(`{"name":["probe"],"private_key":"` + fileKey.privateHex() + `"}`)},
		{Name: "file-array-root", Args: show, Files: raw(`[]`)},
		{Name: "file-null-root", Args: show, Files: raw(`null`)},
		{Name: "file-number-root", Args: show, Files: raw(`12`)},
		{Name: "file-invalid-utf8-0x80-garbage", Args: show, Files: raw("{\"x\":1}\x80")},
		{Name: "file-invalid-utf8-0xa0-garbage", Args: show, Files: raw("{\"x\":1}\xa0")},
		{Name: "file-invalid-utf8-0xad-garbage", Args: show, Files: raw("{\"x\":1}\xad")},
		{Name: "file-invalid-utf8-0x7f-garbage", Args: show, Files: raw("{\"x\":1}\x7f")},
		{Name: "file-invalid-utf8-in-name", Args: show, Files: raw("{\"name\":\"a\xffb\xe2\x82\",\"private_key\":\"" + fileKey.privateHex() + "\",\"public_key\":\"" + hex.EncodeToString(fileKey.public) + "\"}")},
		{Name: "file-bool-root", Args: show, Files: raw(`true`)},
		{Name: "file-string-root", Args: show, Files: raw(`"probe"`)},
		{Name: "file-bad-escape", Args: show, Files: raw(`{"name":"a\qb"}`)},
		{Name: "file-bad-unicode-escape", Args: show, Files: raw(`{"name":"\u12G4"}`)},
		{Name: "file-control-char-in-string", Args: show, Files: raw("{\"name\":\"a\tb\"}")},
		{Name: "file-escaped-key", Args: show, Files: raw(`{"n\u0061me":"esc","private_key":"` + fileKey.privateHex() + `","public_key":"` + hex.EncodeToString(fileKey.public) + `"}`)},
		{Name: "file-surrogate-pair-name", Args: show, Files: raw(`{"name":"\ud83d\ude00","private_key":"` + fileKey.privateHex() + `","public_key":"` + hex.EncodeToString(fileKey.public) + `"}`)},
		{Name: "file-lone-surrogate-name", Args: show, Files: raw(`{"name":"a\ud83dz","private_key":"` + fileKey.privateHex() + `","public_key":"` + hex.EncodeToString(fileKey.public) + `"}`)},
		{Name: "file-float-private", Args: show, Files: raw(`{"private_key":1.5e3}`)},
		{Name: "file-bool-private", Args: show, Files: raw(`{"private_key":false}`)},
		{Name: "file-object-public", Args: show, Files: raw(`{"public_key":{"a":1}}`)},
		{Name: "file-first-type-error-wins", Args: show, Files: raw(`{"private_key":true,"name":7}`)},
		{Name: "file-syntax-error-beats-type-error", Args: show, Files: raw(`{"name":7,"x":}`)},
		{Name: "file-bad-number-minus", Args: show, Files: raw(`{"x":-a}`)},
		{Name: "file-bad-number-leading-zero", Args: show, Files: raw(`{"x":01}`)},
		{Name: "file-bad-number-fraction", Args: show, Files: raw(`{"x":1.}`)},
		{Name: "file-bad-number-exponent", Args: show, Files: raw(`{"x":1e}`)},
		{Name: "file-bad-literal-null", Args: show, Files: raw(`{"x":nul}`)},
		{Name: "file-bad-literal-true", Args: show, Files: raw(`{"x":tru3}`)},
		{Name: "file-missing-colon", Args: show, Files: raw(`{"x" 1}`)},
		{Name: "file-missing-comma", Args: show, Files: raw(`{"x":1 "y":2}`)},
		{Name: "file-trailing-comma", Args: show, Files: raw(`{"x":1,}`)},
		{Name: "file-single-quoted-key", Args: show, Files: raw(`{'x':1}`)},
		{Name: "file-array-missing-comma", Args: show, Files: raw(`{"x":[1 2]}`)},
		{Name: "file-nbsp-garbage", Args: show, Files: raw("{\"x\":1}\u00a0")},
		{Name: "file-nul-garbage", Args: show, Files: raw("{\"x\":1}\x00")},
		{Name: "file-deep-unknown-value-skipped", Args: show, Files: raw(`{"x":[[[{"y":[null,true,-1.5E+2,"s"]}]]],"name":"deep","private_key":"` + fileKey.privateHex() + `","public_key":"` + hex.EncodeToString(fileKey.public) + `"}`)},
	}

	goBinary := buildIdentityCLIOracle(t, root)
	temp := t.TempDir()
	helper := buildIdentityProviderHelper(t, root, temp)
	for _, vector := range cases {
		if vector.DarwinOnly && runtime.GOOS != "darwin" {
			// Keep the committed case so the portable comparison stays aligned.
			fixture.Cases = append(fixture.Cases, vector)
			continue
		}
		work := filepath.Join(temp, vector.Name)
		home, dataHome, tools, tmp, cwd, trace := filepath.Join(work, "home"), filepath.Join(work, "data"), filepath.Join(work, "tools"), filepath.Join(work, "tmp"), filepath.Join(work, "cwd"), filepath.Join(work, "trace")
		for _, dir := range []string{home, dataHome, tools, tmp, cwd, trace} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fixture, err
			}
		}
		identities := filepath.Join(dataHome, "symroom", "identities")
		if err := os.MkdirAll(identities, 0o700); err != nil {
			return fixture, err
		}
		for _, file := range vector.Files {
			if err := os.WriteFile(filepath.Join(identities, file.Name), file.bytes(), 0o600); err != nil {
				return fixture, err
			}
		}
		logPath := filepath.Join(trace, "calls.log")
		env := []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome,
			"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp, "PATH=" + tools,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "PROVIDER_LOG=" + logPath,
		}
		if runtime.GOOS == "windows" {
			env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
		}
		if vector.EnvKey != nil {
			env = append(env, "SYMROOM_IDENTITY_KEY="+*vector.EnvKey)
		}
		for tool, stub := range map[string]*identityProviderStub{"symvault": vector.Symvault, "security": vector.Security} {
			if stub == nil {
				continue
			}
			if err := installIdentityProviderTool(tools, tool, helper); err != nil {
				return fixture, err
			}
			upper := strings.ToUpper(tool)
			env = append(env, "PROVIDER_STDOUT_"+upper+"="+stub.Stdout, "PROVIDER_STDERR_"+upper+"="+stub.Stderr, fmt.Sprintf("PROVIDER_EXIT_%s=%d", upper, stub.Exit))
		}
		cmd := exec.Command(goBinary, vector.Args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = cwd
		cmd.Env = env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			exitErr, ok := runErr.(*exec.ExitError)
			if !ok {
				return fixture, fmt.Errorf("run Go identity provider case %s: %w", vector.Name, runErr)
			}
			code = exitErr.ExitCode()
		}
		calls, _ := os.ReadFile(logPath) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		final, err := snapshotIdentityProviderTree(work)
		if err != nil {
			return fixture, err
		}
		vector.ExitCode, vector.Stdout, vector.Stderr = code, string(stdout), stderr.String()
		vector.ToolCalls, vector.FinalFiles = string(calls), final
		fixture.Cases = append(fixture.Cases, vector)
	}
	return fixture, nil
}

func installIdentityProviderTool(dir, tool, helper string) error {
	name := tool
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	data, err := os.ReadFile(helper) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o755) //nolint:gosec // test helper must be executable
}

// snapshotIdentityProviderTree lists every file below the case root except the
// installed provider executables and the call trace, so a stray write by the
// command (HOME, XDG, TMPDIR, cwd) would show up as a difference.
func snapshotIdentityProviderTree(work string) ([]identityProviderFile, error) {
	files := []identityProviderFile{}
	err := filepath.Walk(work, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(work, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel == "tools" || rel == "trace" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return err
		}
		files = append(files, newIdentityProviderFile(rel, data))
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, err
}

func buildIdentityProviderHelper(t *testing.T, root, temp string) string {
	t.Helper()
	source := `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if path := os.Getenv("PROVIDER_LOG"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			_, _ = fmt.Fprintf(f, "%s %s\n", name, strings.Join(os.Args[1:], " "))
			_ = f.Close()
		}
	}
	upper := strings.ToUpper(name)
	_, _ = fmt.Fprint(os.Stdout, os.Getenv("PROVIDER_STDOUT_"+upper))
	_, _ = fmt.Fprint(os.Stderr, os.Getenv("PROVIDER_STDERR_"+upper))
	if code, err := strconv.Atoi(os.Getenv("PROVIDER_EXIT_" + upper)); err == nil {
		os.Exit(code)
	}
}
`
	sourceDir := filepath.Join(temp, "provider-helper-src")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceDir, "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "provider-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(temp, name)
	cmd := exec.Command("go", "build", "-o", output, sourcePath) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go identity provider helper: %v: %s", err, data)
	}
	return output
}
