package xdg

import (
	"path/filepath"
	"testing"
)

func TestResolve_UsesOverrides(t *testing.T) {
	t.Setenv(EnvConfigHome, "/tmp/cfg")
	t.Setenv(EnvDataHome, "/tmp/data")
	t.Setenv(EnvCacheHome, "/tmp/cache")

	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if p.ConfigDir != "/tmp/cfg" {
		t.Errorf("ConfigDir = %q, want /tmp/cfg", p.ConfigDir)
	}
	if p.DataDir != "/tmp/data" {
		t.Errorf("DataDir = %q, want /tmp/data", p.DataDir)
	}
	if p.CacheDir != "/tmp/cache" {
		t.Errorf("CacheDir = %q, want /tmp/cache", p.CacheDir)
	}
}

func TestResolve_FallsBackToXDGEnv(t *testing.T) {
	t.Setenv(EnvConfigHome, "")
	t.Setenv(EnvDataHome, "")
	t.Setenv(EnvCacheHome, "")
	t.Setenv("XDG_CONFIG_HOME", "/xdgtest/config")
	t.Setenv("XDG_DATA_HOME", "/xdgtest/data")
	t.Setenv("XDG_CACHE_HOME", "/xdgtest/cache")

	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := filepath.Join("/xdgtest/config", appDirName)
	if p.ConfigDir != want {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, want)
	}
}

func TestDatabasePath(t *testing.T) {
	p := Paths{DataDir: "/tmp/data"}
	want := filepath.Join("/tmp/data", "symrelate.db")
	if got := p.DatabasePath(); got != want {
		t.Errorf("DatabasePath() = %q, want %q", got, want)
	}
}

func TestEnsureDirs_CreatesAll(t *testing.T) {
	base := t.TempDir()
	p := Paths{
		ConfigDir: filepath.Join(base, "config"),
		DataDir:   filepath.Join(base, "data"),
		CacheDir:  filepath.Join(base, "cache"),
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error = %v", err)
	}
	for _, dir := range []string{p.ConfigDir, p.DataDir, p.CacheDir} {
		if _, err := filepath.Abs(dir); err != nil {
			t.Fatalf("filepath.Abs(%q) error = %v", dir, err)
		}
	}
}
