package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	ErrIdentityNotFound = errors.New("identity not found")
	ErrInvalidKey       = errors.New("invalid key data")
)

type Identity struct {
	Name       string             `json:"name"`
	MemberID   string             `json:"member_id"`
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	PrivateKey ed25519.PrivateKey `json:"-"`
}

type StoredIdentity struct {
	Name       string `json:"name"`
	MemberID   string `json:"member_id"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

func ComputeMemberID(pubKey ed25519.PublicKey) string {
	h := sha256.Sum256(pubKey)
	return "mem_" + hex.EncodeToString(h[:8])
}

func Generate(name string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	memID := ComputeMemberID(pub)
	return &Identity{
		Name:       name,
		MemberID:   memID,
		PublicKey:  pub,
		PrivateKey: priv,
	}, nil
}

func Sign(privKey ed25519.PrivateKey, message []byte) []byte {
	return ed25519.Sign(privKey, message)
}

func Verify(pubKey ed25519.PublicKey, message, signature []byte) bool {
	return ed25519.Verify(pubKey, message, signature)
}

func IdentitiesDir() string {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "symroom", "identities")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "symroom", "identities")
}

func Save(id *Identity) error {
	dir := IdentitiesDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create identities dir: %w", err)
	}

	stored := StoredIdentity{
		Name:       id.Name,
		MemberID:   id.MemberID,
		PublicKey:  hex.EncodeToString(id.PublicKey),
		PrivateKey: hex.EncodeToString(id.PrivateKey),
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}

	filePath := filepath.Join(dir, id.Name+".json")
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("write identity file: %w", err)
	}
	return nil
}

func Load(name string) (*Identity, error) {
	// Chain 1: SYMROOM_IDENTITY_KEY env
	if envKey := os.Getenv("SYMROOM_IDENTITY_KEY"); envKey != "" {
		keyBytes, err := hex.DecodeString(strings.TrimSpace(envKey))
		if err == nil && (len(keyBytes) == ed25519.PrivateKeySize || len(keyBytes) == ed25519.SeedSize) {
			var priv ed25519.PrivateKey
			if len(keyBytes) == ed25519.SeedSize {
				priv = ed25519.NewKeyFromSeed(keyBytes)
			} else {
				priv = ed25519.PrivateKey(keyBytes)
			}
			pub := priv.Public().(ed25519.PublicKey)
			return &Identity{
				Name:       name,
				MemberID:   ComputeMemberID(pub),
				PublicKey:  pub,
				PrivateKey: priv,
			}, nil
		}
	}

	// Chain 2: symvault via shell-out when on PATH
	if symvaultPath, err := exec.LookPath("symvault"); err == nil && symvaultPath != "" {
		cmd := exec.Command(symvaultPath, "get", "symroom/identities/"+name)
		if out, err := cmd.Output(); err == nil {
			keyStr := strings.TrimSpace(string(out))
			if keyBytes, err := hex.DecodeString(keyStr); err == nil && len(keyBytes) == ed25519.PrivateKeySize {
				priv := ed25519.PrivateKey(keyBytes)
				pub := priv.Public().(ed25519.PublicKey)
				return &Identity{
					Name:       name,
					MemberID:   ComputeMemberID(pub),
					PublicKey:  pub,
					PrivateKey: priv,
				}, nil
			}
		}
	}

	// Chain 3: macOS Keychain (if on darwin)
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "find-generic-password", "-s", "symroom-identity", "-a", name, "-w")
		if out, err := cmd.Output(); err == nil {
			keyStr := strings.TrimSpace(string(out))
			if keyBytes, err := hex.DecodeString(keyStr); err == nil && len(keyBytes) == ed25519.PrivateKeySize {
				priv := ed25519.PrivateKey(keyBytes)
				pub := priv.Public().(ed25519.PublicKey)
				return &Identity{
					Name:       name,
					MemberID:   ComputeMemberID(pub),
					PublicKey:  pub,
					PrivateKey: priv,
				}, nil
			}
		}
	}

	// Chain 4: File in ~/.local/share/symroom/identities/<name>.json
	filePath := filepath.Join(IdentitiesDir(), name+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrIdentityNotFound
		}
		return nil, fmt.Errorf("read identity file: %w", err)
	}

	var stored StoredIdentity
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal identity file: %w", err)
	}

	privBytes, err := hex.DecodeString(stored.PrivateKey)
	if err != nil || len(privBytes) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKey
	}

	pubBytes, err := hex.DecodeString(stored.PublicKey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, ErrInvalidKey
	}

	return &Identity{
		Name:       stored.Name,
		MemberID:   stored.MemberID,
		PublicKey:  ed25519.PublicKey(pubBytes),
		PrivateKey: ed25519.PrivateKey(privBytes),
	}, nil
}

func List() ([]string, error) {
	dir := IdentitiesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	return names, nil
}
