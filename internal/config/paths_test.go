package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaths_FreshInstallUnderXDG(t *testing.T) {
	dataHome := t.TempDir()
	configHome := t.TempDir()
	cacheHome := t.TempDir()

	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv(EnvLegacyContactsDataHome, "")
	t.Setenv(EnvLegacyContactsConfigHome, "")
	t.Setenv(EnvLegacyContactsCacheHome, "")

	if got := DataDir(); got != filepath.Join(dataHome, "symdesk") {
		t.Errorf("DataDir() = %q, want %q", got, filepath.Join(dataHome, "symdesk"))
	}
	if got := ConfigDir(); got != filepath.Join(configHome, "symdesk") {
		t.Errorf("ConfigDir() = %q, want %q", got, filepath.Join(configHome, "symdesk"))
	}
	if got := CacheDir(); got != filepath.Join(cacheHome, "symdesk") {
		t.Errorf("CacheDir() = %q, want %q", got, filepath.Join(cacheHome, "symdesk"))
	}

	// For a fresh install, every store resolves inside symdesk/
	ingestDB, err := IngestDBPath()
	if err != nil {
		t.Fatalf("IngestDBPath() error = %v", err)
	}
	if want := filepath.Join(dataHome, "symdesk", "symingest.db"); ingestDB != want {
		t.Errorf("IngestDBPath() = %q, want %q", ingestDB, want)
	}

	ingestArchive, err := IngestDataPath("archive")
	if err != nil {
		t.Fatalf("IngestDataPath(archive) error = %v", err)
	}
	if want := filepath.Join(dataHome, "symdesk", "archive"); ingestArchive != want {
		t.Errorf("IngestDataPath(archive) = %q, want %q", ingestArchive, want)
	}

	contactsDB := ContactsDBPath()
	if want := filepath.Join(dataHome, "symdesk", "symrelate.db"); contactsDB != want {
		t.Errorf("ContactsDBPath() = %q, want %q", contactsDB, want)
	}

	sidecarStandalone, err := SidecarPath("")
	if err != nil {
		t.Fatalf("SidecarPath(\"\") error = %v", err)
	}
	if want := filepath.Join(dataHome, "symdesk", "sidecar.db"); sidecarStandalone != want {
		t.Errorf("SidecarPath(\"\") = %q, want %q", sidecarStandalone, want)
	}

	retrievalStandalone, err := RetrievalPath("")
	if err != nil {
		t.Fatalf("RetrievalPath(\"\") error = %v", err)
	}
	if want := filepath.Join(dataHome, "symdesk", "retrieval.db"); retrievalStandalone != want {
		t.Errorf("RetrievalPath(\"\") = %q, want %q", retrievalStandalone, want)
	}
}

func TestPaths_DefaultHomeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv(EnvLegacyContactsDataHome, "")
	t.Setenv(EnvLegacyContactsConfigHome, "")
	t.Setenv(EnvLegacyContactsCacheHome, "")

	if got := DataHome(); got != filepath.Join(home, ".local", "share") {
		t.Errorf("DataHome() = %q, want %q", got, filepath.Join(home, ".local", "share"))
	}
	if got := ConfigHome(); got != filepath.Join(home, ".config") {
		t.Errorf("ConfigHome() = %q, want %q", got, filepath.Join(home, ".config"))
	}
	if got := CacheHome(); got != filepath.Join(home, ".cache") {
		t.Errorf("CacheHome() = %q, want %q", got, filepath.Join(home, ".cache"))
	}
}

func TestSidecarVaultDirDistinguishesTemporaryAndPersistentVaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	volumeRoot := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	persistentVault := filepath.Join(volumeRoot, "symdesk-persistent-vault")
	persistentDir, err := SidecarVaultDir(persistentVault)
	if err != nil {
		t.Fatalf("SidecarVaultDir(persistent) error = %v", err)
	}
	wantRoot := filepath.Join(home, ".local", "share", AppName, "vaults")
	if rel, err := filepath.Rel(wantRoot, persistentDir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("persistent vault sidecar = %q, want below %q", persistentDir, wantRoot)
	}

	temporaryVault := filepath.Join(os.TempDir(), "symdesk-temporary-vault")
	temporaryDir, err := SidecarVaultDir(temporaryVault)
	if err != nil {
		t.Fatalf("SidecarVaultDir(temporary) error = %v", err)
	}
	wantTemporaryRoot := filepath.Join(os.TempDir(), AppName, "test-vaults")
	if rel, err := filepath.Rel(wantTemporaryRoot, temporaryDir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("temporary vault sidecar = %q, want below %q", temporaryDir, wantTemporaryRoot)
	}
}

func TestIngestDataPathRejectsTraversal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, name := range []string{"", ".", "../escape", "nested/file", string(filepath.Separator) + "absolute"} {
		if _, err := IngestDataPath(name); err == nil {
			t.Errorf("IngestDataPath(%q) succeeded, want validation error", name)
		}
	}
}

func TestPaths_LegacyIngestFallback(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	legacyDir := filepath.Join(dataHome, "symingest")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyDB := filepath.Join(legacyDir, "symingest.db")
	if err := os.WriteFile(legacyDB, []byte("legacy db"), 0600); err != nil {
		t.Fatal(err)
	}

	// With only legacyDB existing, IngestDBPath returns legacy path.
	got, err := IngestDBPath()
	if err != nil {
		t.Fatalf("IngestDBPath() error = %v", err)
	}
	if got != legacyDB {
		t.Errorf("IngestDBPath() = %q, want legacy %q", got, legacyDB)
	}

	// When primary exists, primary takes priority over legacy.
	primaryDir := filepath.Join(dataHome, "symdesk")
	if err := os.MkdirAll(primaryDir, 0700); err != nil {
		t.Fatal(err)
	}
	primaryDB := filepath.Join(primaryDir, "symingest.db")
	if err := os.WriteFile(primaryDB, []byte("new db"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err = IngestDBPath()
	if err != nil {
		t.Fatalf("IngestDBPath() error = %v", err)
	}
	if got != primaryDB {
		t.Errorf("IngestDBPath() after primary creation = %q, want primary %q", got, primaryDB)
	}
}

func TestPaths_LegacyContactsFallback(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv(EnvLegacyContactsDataHome, "")

	legacyDir := filepath.Join(dataHome, "symrelate")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyDB := filepath.Join(legacyDir, "symrelate.db")
	if err := os.WriteFile(legacyDB, []byte("legacy contacts db"), 0600); err != nil {
		t.Fatal(err)
	}

	// Falls back to legacy DB when primary doesn't exist
	if got := ContactsDBPath(); got != legacyDB {
		t.Errorf("ContactsDBPath() = %q, want legacy %q", got, legacyDB)
	}

	bundle := ContactsPaths()
	if bundle.DBPath != legacyDB {
		t.Errorf("bundle.DBPath = %q, want %q", bundle.DBPath, legacyDB)
	}
	if bundle.DataDir != legacyDir {
		t.Errorf("bundle.DataDir = %q, want legacy dir %q", bundle.DataDir, legacyDir)
	}

	// An explicit SYMRELATE_DATA_HOME is authoritative even when the
	// directory is still empty and a primary database already exists.
	primaryDir := filepath.Join(dataHome, AppName)
	if err := os.MkdirAll(primaryDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryDir, "symrelate.db"), []byte("primary db"), 0600); err != nil {
		t.Fatal(err)
	}
	customDir := t.TempDir()
	t.Setenv(EnvLegacyContactsDataHome, customDir)
	customDB := filepath.Join(customDir, "symrelate.db")
	if got := ContactsDBPath(); got != customDB {
		t.Errorf("ContactsDBPath() with SYMRELATE_DATA_HOME = %q, want %q", got, customDB)
	}
	bundle = ContactsPaths()
	if bundle.DataDir != customDir || bundle.DBPath != customDB {
		t.Errorf("ContactsPaths() override = data %q db %q, want %q and %q", bundle.DataDir, bundle.DBPath, customDir, customDB)
	}
}

func TestPaths_LegacyRetrievalFallback(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	legacyDir := filepath.Join(dataHome, "symaira-seek")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyDB := filepath.Join(legacyDir, "symseek.db")
	if err := os.WriteFile(legacyDB, []byte("legacy retrieval"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := RetrievalPath("")
	if err != nil {
		t.Fatalf("RetrievalPath(\"\") error = %v", err)
	}
	if got != legacyDB {
		t.Errorf("RetrievalPath(\"\") = %q, want legacy %q", got, legacyDB)
	}

	// If symdesk/symseek.db exists, it takes precedence over legacy
	primaryDir := filepath.Join(dataHome, "symdesk")
	if err := os.MkdirAll(primaryDir, 0700); err != nil {
		t.Fatal(err)
	}
	symseekDB := filepath.Join(primaryDir, "symseek.db")
	if err := os.WriteFile(symseekDB, []byte("symseek in symdesk"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = RetrievalPath("")
	if err != nil {
		t.Fatalf("RetrievalPath(\"\") error = %v", err)
	}
	if got != symseekDB {
		t.Errorf("RetrievalPath(\"\") = %q, want %q", got, symseekDB)
	}

	// If symdesk/retrieval.db exists, it takes highest precedence
	retrievalDB := filepath.Join(primaryDir, "retrieval.db")
	if err := os.WriteFile(retrievalDB, []byte("retrieval in symdesk"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = RetrievalPath("")
	if err != nil {
		t.Fatalf("RetrievalPath(\"\") error = %v", err)
	}
	if got != retrievalDB {
		t.Errorf("RetrievalPath(\"\") = %q, want %q", got, retrievalDB)
	}
}

func TestResolveStorePaths(t *testing.T) {
	dataHome := t.TempDir()
	configHome := t.TempDir()
	cacheHome := t.TempDir()

	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv(EnvLegacyContactsDataHome, "")

	// 1. Without vault
	paths, err := ResolveStorePaths("")
	if err != nil {
		t.Fatalf("ResolveStorePaths(\"\") error = %v", err)
	}
	if paths.DataDir != filepath.Join(dataHome, "symdesk") {
		t.Errorf("DataDir = %q", paths.DataDir)
	}
	if paths.ConfigDir != filepath.Join(configHome, "symdesk") {
		t.Errorf("ConfigDir = %q", paths.ConfigDir)
	}
	if paths.CacheDir != filepath.Join(cacheHome, "symdesk") {
		t.Errorf("CacheDir = %q", paths.CacheDir)
	}
	if paths.Sidecar != filepath.Join(dataHome, "symdesk", "sidecar.db") {
		t.Errorf("Sidecar = %q", paths.Sidecar)
	}
	if paths.Retrieval != filepath.Join(dataHome, "symdesk", "retrieval.db") {
		t.Errorf("Retrieval = %q", paths.Retrieval)
	}
	if paths.Ingest != filepath.Join(dataHome, "symdesk", "symingest.db") {
		t.Errorf("Ingest = %q", paths.Ingest)
	}
	if paths.Contacts != filepath.Join(dataHome, "symdesk", "symrelate.db") {
		t.Errorf("Contacts = %q", paths.Contacts)
	}

	// 2. With vault
	vaultDir := t.TempDir()
	pathsVault, err := ResolveStorePaths(vaultDir)
	if err != nil {
		t.Fatalf("ResolveStorePaths(vault) error = %v", err)
	}
	if filepath.Base(pathsVault.Sidecar) != "sidecar.db" {
		t.Errorf("Sidecar base = %q, want sidecar.db", filepath.Base(pathsVault.Sidecar))
	}
	if filepath.Base(pathsVault.Retrieval) != "retrieval.db" {
		t.Errorf("Retrieval base = %q, want retrieval.db", filepath.Base(pathsVault.Retrieval))
	}
	if filepath.Dir(pathsVault.Sidecar) != filepath.Dir(pathsVault.Retrieval) {
		t.Errorf("sidecar and retrieval should share the same per-vault directory: %s vs %s",
			pathsVault.Sidecar, pathsVault.Retrieval)
	}
}
