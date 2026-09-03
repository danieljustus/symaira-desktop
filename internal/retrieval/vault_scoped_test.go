package retrieval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

func TestOpenForVaultKeepsTwoVaultsIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	vaultA := filepath.Join(t.TempDir(), "vault-a")
	vaultB := filepath.Join(t.TempDir(), "vault-b")
	if err := os.MkdirAll(vaultA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vaultB, 0o700); err != nil {
		t.Fatal(err)
	}

	clientA, err := OpenForVault(vaultA)
	if err != nil {
		t.Fatalf("OpenForVault(vaultA): %v", err)
	}
	defer func() { _ = clientA.Close() }()
	clientB, err := OpenForVault(vaultB)
	if err != nil {
		t.Fatalf("OpenForVault(vaultB): %v", err)
	}
	defer func() { _ = clientB.Close() }()

	if clientA.indexPath == clientB.indexPath {
		t.Fatalf("vaults share retrieval index path %q", clientA.indexPath)
	}
	if err := clientA.Index("vault-a.md", "only in vault A"); err != nil {
		t.Fatalf("index vault A: %v", err)
	}
	if err := clientB.Index("vault-b.md", "only in vault B"); err != nil {
		t.Fatalf("index vault B: %v", err)
	}

	statsA, err := clientA.db.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	statsB, err := clientB.db.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if statsA.DocumentCount != 1 || statsB.DocumentCount != 1 {
		t.Fatalf("document counts = %d and %d, want one document per vault", statsA.DocumentCount, statsB.DocumentCount)
	}

	pathA, err := IndexPathForVault(vaultA)
	if err != nil {
		t.Fatal(err)
	}
	pathB, err := IndexPathForVault(vaultB)
	if err != nil {
		t.Fatal(err)
	}
	if clientA.indexPath != pathA || clientB.indexPath != pathB {
		t.Fatalf("client paths = %q and %q, resolver paths = %q and %q", clientA.indexPath, clientB.indexPath, pathA, pathB)
	}
}

func TestOpenForVaultHonorsIndexPathOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	override := filepath.Join(t.TempDir(), "custom", "retrieval.db")
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("index_path = \""+override+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := OpenForVault(filepath.Join(t.TempDir(), "vault"))
	if err != nil {
		t.Fatalf("OpenForVault: %v", err)
	}
	defer func() { _ = client.Close() }()

	resolved, err := filepath.Abs(override)
	if err != nil {
		t.Fatal(err)
	}
	if client.indexPath != filepath.Clean(resolved) {
		t.Fatalf("index path = %q, want %q", client.indexPath, filepath.Clean(resolved))
	}
	if client.vaultScoped {
		t.Fatal("explicit index override was reported as vault-scoped")
	}
}

func TestOpenForVaultMigratesLegacyIndexAtomically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	legacyPath, err := db.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := db.OpenAt(legacyPath)
	if err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	legacyInfo, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	vault := filepath.Join(t.TempDir(), "vault")
	client, err := OpenForVault(vault)
	if err != nil {
		t.Fatalf("OpenForVault: %v", err)
	}
	defer func() { _ = client.Close() }()

	migratedInfo, err := os.Stat(client.indexPath)
	if err != nil {
		t.Fatalf("stat migrated index: %v", err)
	}
	if migratedInfo.Size() != legacyInfo.Size() {
		t.Fatalf("migrated index size = %d, legacy size = %d", migratedInfo.Size(), legacyInfo.Size())
	}
	if migratedInfo.Mode().Perm() != 0o600 {
		t.Fatalf("migrated index mode = %o, want 600", migratedInfo.Mode().Perm())
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy index still exists after migration: %v", err)
	}

	// A second open must reuse the completed destination rather than copying
	// the legacy file again over a live per-vault database.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenForVault(vault)
	if err != nil {
		t.Fatalf("second OpenForVault: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
