package identity

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolatedLoadEnv(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tempDir)
	t.Setenv("PATH", tempDir)
	t.Setenv("SYMROOM_IDENTITY_KEY", "")
	return tempDir
}

func TestGenerateAndSaveLoad(t *testing.T) {
	isolatedLoadEnv(t)

	id, err := Generate("alice")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	if !strings.HasPrefix(id.MemberID, "mem_") {
		t.Errorf("expected member_id prefix mem_, got %s", id.MemberID)
	}

	if err := Save(id); err != nil {
		t.Fatalf("failed to save identity: %v", err)
	}

	// Assert key file permissions 0600
	filePath := filepath.Join(IdentitiesDir(), "alice.json")
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to stat identity file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected file mode 0600, got %o", info.Mode().Perm())
	}

	// Load back
	loaded, err := Load("alice")
	if err != nil {
		t.Fatalf("failed to load identity: %v", err)
	}

	if loaded.Name != id.Name || loaded.MemberID != id.MemberID {
		t.Errorf("loaded identity mismatched: got %+v, want %+v", loaded, id)
	}

	if !bytes.Equal(loaded.PublicKey, id.PublicKey) {
		t.Errorf("public key mismatch")
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("private key mismatch")
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	id, err := Generate("bob")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	msg := []byte("hello symaira-room")
	sig := Sign(id.PrivateKey, msg)

	if !Verify(id.PublicKey, msg, sig) {
		t.Errorf("signature verification failed")
	}

	if Verify(id.PublicKey, []byte("tampered msg"), sig) {
		t.Errorf("expected tampered msg verification to fail")
	}
}

func TestNoPrivateKeyLogging(t *testing.T) {
	id, err := Generate("charlie")
	if err != nil {
		t.Fatalf("failed to generate identity: %v", err)
	}

	// Convert Identity struct to string or JSON (public representation)
	str := id.MemberID + id.Name
	privHex := hex.EncodeToString(id.PrivateKey)

	if strings.Contains(str, privHex) {
		t.Fatalf("private key leaked in string representation")
	}
}

func TestSymvaultFallbackToFile(t *testing.T) {
	isolatedLoadEnv(t)

	id, err := Generate("dave")
	if err != nil {
		t.Fatalf("failed to generate: %v", err)
	}
	if err := Save(id); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	loaded, err := Load("dave")
	if err != nil {
		t.Fatalf("expected fallback to file without error, got %v", err)
	}

	if loaded.MemberID != id.MemberID {
		t.Errorf("expected member ID %s, got %s", id.MemberID, loaded.MemberID)
	}
}

// writeFakeBin drops an executable shell script named binName into dir and
// returns its path. Used to fake symvault/security on PATH so the Load
// fallback chains can be exercised hermetically without real binaries.
func writeFakeBin(t *testing.T, dir, binName, script string) string {
	t.Helper()
	path := filepath.Join(dir, binName)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", binName, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake %s: %v", binName, err)
	}
	return path
}

// writeIdentityFile writes a raw identity JSON file at the Load file-chain
// location (XDG_DATA_HOME/symroom/identities/<name>.json).
func writeIdentityFile(t *testing.T, dataHome, name, content string) {
	t.Helper()
	dir := filepath.Join(dataHome, "symroom", "identities")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir identities dir: %v", err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
}

// TestLoadEnvVarChain covers chain 1: SYMROOM_IDENTITY_KEY (full private key
// hex form). The env key must win even when a file identity exists.
func TestLoadEnvVarChain(t *testing.T) {
	isolatedLoadEnv(t)

	envID, err := Generate("env-user")
	if err != nil {
		t.Fatalf("generate env identity: %v", err)
	}
	fileID, err := Generate("file-user")
	if err != nil {
		t.Fatalf("generate file identity: %v", err)
	}
	if err := Save(fileID); err != nil {
		t.Fatalf("save file identity: %v", err)
	}

	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(envID.PrivateKey))

	loaded, err := Load("env-user")
	if err != nil {
		t.Fatalf("Load with env key: %v", err)
	}
	if loaded.Name != "env-user" {
		t.Errorf("expected name env-user, got %s", loaded.Name)
	}
	if loaded.MemberID != envID.MemberID {
		t.Errorf("expected member %s, got %s", envID.MemberID, loaded.MemberID)
	}
	if !bytes.Equal(loaded.PrivateKey, envID.PrivateKey) {
		t.Errorf("env chain did not win over file chain")
	}
}

// TestLoadEnvVarSeedForm covers chain 1 with the 32-byte seed form: the seed is
// expanded via ed25519.NewKeyFromSeed and must reproduce the same key.
func TestLoadEnvVarSeedForm(t *testing.T) {
	isolatedLoadEnv(t)

	id, err := Generate("seed-user")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(id.PrivateKey.Seed()))

	loaded, err := Load("seed-user")
	if err != nil {
		t.Fatalf("Load with seed key: %v", err)
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("seed form did not reconstruct the same private key")
	}
	if loaded.MemberID != id.MemberID {
		t.Errorf("expected member %s, got %s", id.MemberID, loaded.MemberID)
	}
}

// TestLoadInvalidEnvKeyFallsThroughToFile covers the env chain rejection path:
// a non-hex or wrong-length env key must be skipped in favor of the file chain.
func TestLoadInvalidEnvKeyFallsThroughToFile(t *testing.T) {
	isolatedLoadEnv(t)
	t.Setenv("SYMROOM_IDENTITY_KEY", "test-key-this-is-not-valid-hex")

	id, err := Generate("fallback-user")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if err := Save(id); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	loaded, err := Load("fallback-user")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("expected file-chain key, got mismatch")
	}
}

// TestLoadSymvaultChain covers chain 2: a fake symvault on PATH that answers
// `symvault get symroom/identities/<name>` with a valid private key hex.
func TestLoadSymvaultChain(t *testing.T) {
	tempDir := isolatedLoadEnv(t)

	id, err := Generate("vault-user")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	writeFakeBin(t, tempDir, "symvault", "#!/bin/sh\necho "+hex.EncodeToString(id.PrivateKey)+"\n")

	loaded, err := Load("vault-user")
	if err != nil {
		t.Fatalf("Load via symvault: %v", err)
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("expected symvault-chain key, got mismatch")
	}
	if loaded.MemberID != id.MemberID {
		t.Errorf("expected member %s, got %s", id.MemberID, loaded.MemberID)
	}
}

// TestLoadSymvaultGarbageFallsThroughToFile covers the symvault rejection path:
// a symvault that returns non-hex output must be skipped in favor of the file chain.
func TestLoadSymvaultGarbageFallsThroughToFile(t *testing.T) {
	tempDir := isolatedLoadEnv(t)
	writeFakeBin(t, tempDir, "symvault", "#!/bin/sh\necho test-key-garbage-not-hex\n")

	id, err := Generate("vault-fallback")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if err := Save(id); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	loaded, err := Load("vault-fallback")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("expected file-chain key after garbage symvault output")
	}
}

// TestLoadKeychainChain covers chain 3 (darwin only): a fake `security` binary
// on PATH that answers find-generic-password with a valid private key hex.
func TestLoadKeychainChain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keychain chain only runs on darwin")
	}
	tempDir := isolatedLoadEnv(t)

	id, err := Generate("keychain-user")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	writeFakeBin(t, tempDir, "security", "#!/bin/sh\necho "+hex.EncodeToString(id.PrivateKey)+"\n")

	loaded, err := Load("keychain-user")
	if err != nil {
		t.Fatalf("Load via keychain: %v", err)
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("expected keychain-chain key, got mismatch")
	}
	if loaded.MemberID != id.MemberID {
		t.Errorf("expected member %s, got %s", id.MemberID, loaded.MemberID)
	}
}

// TestLoadKeychainFailureFallsThroughToFile covers the keychain rejection path
// (darwin only): a failing `security` invocation must be skipped in favor of
// the file chain.
func TestLoadKeychainFailureFallsThroughToFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keychain chain only runs on darwin")
	}
	tempDir := isolatedLoadEnv(t)
	writeFakeBin(t, tempDir, "security", "#!/bin/sh\necho test-key-not-found >&2\nexit 44\n")

	id, err := Generate("keychain-fallback")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if err := Save(id); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	loaded, err := Load("keychain-fallback")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(loaded.PrivateKey, id.PrivateKey) {
		t.Errorf("expected file-chain key after failing keychain lookup")
	}
}

// TestLoadIdentityNotFound covers the terminal fallback: no env key, no
// symvault, no keychain entry, and no identity file -> ErrIdentityNotFound.
func TestLoadIdentityNotFound(t *testing.T) {
	isolatedLoadEnv(t)

	_, err := Load("nobody")
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("expected ErrIdentityNotFound, got %v", err)
	}
}

// TestLoadCorruptedFileReturnsErrInvalidKey covers the file-chain corruption
// paths: invalid hex, wrong private key length, and wrong public key length
// must all surface as ErrInvalidKey.
func TestLoadCorruptedFileReturnsErrInvalidKey(t *testing.T) {
	tempDir := isolatedLoadEnv(t)

	goodPriv := hex.EncodeToString(func() []byte {
		id, err := Generate("tmp")
		if err != nil {
			t.Fatalf("generate identity: %v", err)
		}
		return id.PrivateKey
	}())

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "private key invalid hex",
			content: `{
  "name": "corrupt",
  "member_id": "mem_deadbeef",
  "public_key": "` + strings.Repeat("ab", 32) + `",
  "private_key": "test-key-not-valid-hex"
}`,
		},
		{
			name: "private key wrong length",
			content: `{
  "name": "corrupt",
  "member_id": "mem_deadbeef",
  "public_key": "` + strings.Repeat("ab", 32) + `",
  "private_key": "deadbeef"
}`,
		},
		{
			name: "public key wrong length",
			content: `{
  "name": "corrupt",
  "member_id": "mem_deadbeef",
  "public_key": "ab",
  "private_key": "` + goodPriv + `"
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeIdentityFile(t, tempDir, "corrupt", tt.content)
			_, err := Load("corrupt")
			if !errors.Is(err, ErrInvalidKey) {
				t.Errorf("expected ErrInvalidKey, got %v", err)
			}
		})
	}
}

// TestLoadUnparsableFile covers a file that is not valid JSON: Load must fail
// with a wrapped unmarshal error (not ErrIdentityNotFound / ErrInvalidKey).
func TestLoadUnparsableFile(t *testing.T) {
	tempDir := isolatedLoadEnv(t)
	writeIdentityFile(t, tempDir, "garbage", "{ this is not json")

	_, err := Load("garbage")
	if err == nil {
		t.Fatal("expected error for unparsable identity file, got nil")
	}
	if errors.Is(err, ErrIdentityNotFound) || errors.Is(err, ErrInvalidKey) {
		t.Errorf("expected wrapped unmarshal error, got %v", err)
	}
	if !strings.Contains(err.Error(), "unmarshal identity file") {
		t.Errorf("expected unmarshal context in error, got %v", err)
	}
}

func TestListIdentities(t *testing.T) {
	isolatedLoadEnv(t)

	if got, err := List(); err != nil {
		t.Fatalf("List on missing directory: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("List on missing directory = %v, want empty", got)
	}

	for _, name := range []string{"alice", "bob"} {
		id, err := Generate(name)
		if err != nil {
			t.Fatalf("Generate(%q): %v", name, err)
		}
		if err := Save(id); err != nil {
			t.Fatalf("Save(%q): %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(IdentitiesDir(), "notes.txt"), nil, 0o600); err != nil {
		t.Fatalf("write non-identity file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(IdentitiesDir(), "nested.json"), 0o700); err != nil {
		t.Fatalf("write identity directory: %v", err)
	}

	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := map[string]bool{"alice": true, "bob": true}
	if len(got) != len(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("List returned unexpected identity %q", name)
		}
	}
}
