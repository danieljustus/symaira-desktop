// Package xdg resolves symrelate's config, data and cache directories
// by consuming the shared internal/config path resolver, while honoring
// legacy SYMRELATE_* environment overrides and legacy storage fallbacks.
package xdg

import (
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

const (
	appDirName       = config.AppName
	legacyAppDirName = config.LegacyContactsAppName
)

// Env override names. Set directly, they take precedence over both the
// standard XDG_* variables and the platform default — this is what tests
// use to run against a throwaway directory.
const (
	EnvConfigHome = config.EnvLegacyContactsConfigHome
	EnvDataHome   = config.EnvLegacyContactsDataHome
	EnvCacheHome  = config.EnvLegacyContactsCacheHome
)

// Paths holds the resolved directories for one symrelate invocation.
type Paths struct {
	ConfigDir string
	DataDir   string
	CacheDir  string
	dbPath    string
}

// Resolve computes Paths using internal/config, honoring legacy SYMRELATE_*_HOME
// overrides and preserving legacy symrelate paths as read-only fallbacks.
func Resolve() (Paths, error) {
	bundle := config.ContactsPaths()
	return Paths{
		ConfigDir: bundle.ConfigDir,
		DataDir:   bundle.DataDir,
		CacheDir:  bundle.CacheDir,
		dbPath:    bundle.DBPath,
	}, nil
}

// EnsureDirs creates all resolved directories (0700 — contact data is
// sensitive by default).
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.ConfigDir, p.DataDir, p.CacheDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

// DatabasePath returns the path of the primary SQLite database file.
func (p Paths) DatabasePath() string {
	if p.dbPath != "" {
		return p.dbPath
	}
	if p.DataDir != "" {
		return filepath.Join(p.DataDir, "symrelate.db")
	}
	return config.ContactsDBPath()
}
