package retrieval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceRegistryValidatesExternalFolders(t *testing.T) {
	vaultRoot := t.TempDir()
	externalRoot := t.TempDir()

	canonical, err := ValidateExternalFolder(vaultRoot, externalRoot)
	if err != nil {
		t.Fatalf("ValidateExternalFolder: %v", err)
	}
	want, err := filepath.EvalSymlinks(externalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != want {
		t.Fatalf("canonical path = %q, want %q", canonical, want)
	}

	if _, err := ValidateExternalFolder(vaultRoot, vaultRoot); !errors.Is(err, ErrSourceInsideVault) {
		t.Fatalf("vault root validation error = %v, want ErrSourceInsideVault", err)
	}
	inside := filepath.Join(vaultRoot, "nested")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateExternalFolder(vaultRoot, inside); !errors.Is(err, ErrSourceInsideVault) {
		t.Fatalf("nested vault validation error = %v, want ErrSourceInsideVault", err)
	}
	externalTarget := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(externalTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	vaultLink := filepath.Join(vaultRoot, "external-link")
	if err := os.Symlink(externalTarget, vaultLink); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateExternalFolder(vaultRoot, vaultLink); !errors.Is(err, ErrSourceInsideVault) {
		t.Fatalf("vault symlink validation error = %v, want ErrSourceInsideVault", err)
	}
	if _, err := ValidateExternalFolder(vaultRoot, filepath.Join(vaultRoot, "missing")); err == nil {
		t.Fatal("missing source folder unexpectedly validated")
	}
}

func TestSourceRegistryUsesStableCanonicalIdentity(t *testing.T) {
	vaultRoot := t.TempDir()
	realRoot := filepath.Join(t.TempDir(), "library")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(realRoot), "library-alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}

	registry, err := NewSourceRegistry(vaultRoot)
	if err != nil {
		t.Fatalf("NewSourceRegistry: %v", err)
	}
	first, err := registry.Add(alias)
	if err != nil {
		t.Fatalf("Add alias: %v", err)
	}
	second, err := registry.Add(realRoot)
	if err != nil {
		t.Fatalf("Add real path: %v", err)
	}
	if first.ID != second.ID || first.Path != second.Path {
		t.Fatalf("alias and real registration differ: first=%+v second=%+v", first, second)
	}

	sources, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("source count = %d, want 1", len(sources))
	}

	reopened, err := NewSourceRegistry(vaultRoot)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	persisted, err := reopened.List()
	if err != nil {
		t.Fatalf("reopened List: %v", err)
	}
	if len(persisted) != 1 || persisted[0] != first {
		t.Fatalf("persisted sources = %+v, want [%+v]", persisted, first)
	}
}

func TestSourceRegistryRemoveLeavesExternalFolderUntouched(t *testing.T) {
	vaultRoot := t.TempDir()
	externalRoot := t.TempDir()
	marker := filepath.Join(externalRoot, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := NewSourceRegistry(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	source, err := registry.Add(externalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Remove(source.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("external marker after remove: %v", err)
	}
	sources, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("sources after remove = %+v, want empty", sources)
	}
}
