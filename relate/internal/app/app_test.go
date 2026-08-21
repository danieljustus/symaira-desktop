package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/xdg"
)

func TestOpenMemory_WiresServicesAndSupportsQuickAdd(t *testing.T) {
	ctx := context.Background()
	a, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	defer a.Close()

	if err := a.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	person, err := a.CreatePersonWithContactPoints(ctx, contact.PersonInput{DisplayName: "Ada Lovelace"}, "ada@example.com", "")
	if err != nil {
		t.Fatalf("CreatePersonWithContactPoints() error = %v", err)
	}
	if person.DisplayName != "Ada Lovelace" || len(person.ContactPoints) != 1 {
		t.Fatalf("quick add result = %+v, want Ada Lovelace with one contact point", person)
	}
}

func TestOpenAt_UsesExplicitDatabaseAndPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	paths := xdg.Paths{
		ConfigDir: filepath.Join(dir, "config"),
		DataDir:   filepath.Join(dir, "data"),
		CacheDir:  filepath.Join(dir, "cache"),
	}
	dbPath := filepath.Join(dir, "explicit.db")

	a, err := OpenAt(ctx, dbPath, paths)
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	if a.Paths != paths {
		t.Fatalf("Paths = %+v, want %+v", a.Paths, paths)
	}
	if err := a.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("explicit database missing: %v", err)
	}
}

func TestOpen_ResolvesAndCreatesProfileDirectories(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(xdg.EnvConfigHome, filepath.Join(dir, "config"))
	t.Setenv(xdg.EnvDataHome, filepath.Join(dir, "data"))
	t.Setenv(xdg.EnvCacheHome, filepath.Join(dir, "cache"))

	a, err := Open(context.Background())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer a.Close()

	for _, path := range []string{a.Paths.ConfigDir, a.Paths.DataDir, a.Paths.CacheDir} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("profile directory %q missing: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("profile path %q is not a directory", path)
		}
	}
}
