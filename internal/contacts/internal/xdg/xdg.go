// Package xdg resolves symrelate's config, data and cache directories
// following the XDG Base Directory Specification, with explicit overrides
// so tests never touch a real user profile.
package xdg

import (
	"os"
	"path/filepath"
)

const appDirName = "symrelate"

// Env override names. Set directly, they take precedence over both the
// standard XDG_* variables and the platform default — this is what tests
// use to run against a throwaway directory.
const (
	EnvConfigHome = "SYMRELATE_CONFIG_HOME"
	EnvDataHome   = "SYMRELATE_DATA_HOME"
	EnvCacheHome  = "SYMRELATE_CACHE_HOME"
)

// Paths holds the resolved directories for one symrelate invocation.
type Paths struct {
	ConfigDir string
	DataDir   string
	CacheDir  string
}

// Resolve computes Paths from the environment, honoring
// SYMRELATE_*_HOME overrides first, then XDG_*_HOME, then platform
// defaults ($HOME/.config, $HOME/.local/share, $HOME/.cache).
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	configBase := firstNonEmpty(os.Getenv(EnvConfigHome), "")
	if configBase == "" {
		configBase = joinIfSet(firstNonEmpty(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), appDirName)
	}
	dataBase := firstNonEmpty(os.Getenv(EnvDataHome), "")
	if dataBase == "" {
		dataBase = joinIfSet(firstNonEmpty(os.Getenv("XDG_DATA_HOME"), filepath.Join(home, ".local", "share")), appDirName)
	}
	cacheBase := firstNonEmpty(os.Getenv(EnvCacheHome), "")
	if cacheBase == "" {
		cacheBase = joinIfSet(firstNonEmpty(os.Getenv("XDG_CACHE_HOME"), filepath.Join(home, ".cache")), appDirName)
	}

	return Paths{ConfigDir: configBase, DataDir: dataBase, CacheDir: cacheBase}, nil
}

// EnsureDirs creates all resolved directories (0700 — contact data is
// sensitive by default).
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.ConfigDir, p.DataDir, p.CacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// DatabasePath returns the path of the primary SQLite database file.
func (p Paths) DatabasePath() string {
	return filepath.Join(p.DataDir, "symrelate.db")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinIfSet(base, sub string) string {
	if base == "" {
		return sub
	}
	return filepath.Join(base, sub)
}
