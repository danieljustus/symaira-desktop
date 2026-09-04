package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppName is the unified application name for all SymDesk state.
const AppName = "symdesk"

// Legacy app names for absorbed companions.
const (
	LegacyIngestAppName    = "symingest"
	LegacyContactsAppName  = "symrelate"
	LegacyRetrievalAppName = "symaira-seek"
)

// Legacy environment variable overrides for contacts (symrelate).
const (
	EnvLegacyContactsConfigHome = "SYMRELATE_CONFIG_HOME"
	EnvLegacyContactsDataHome   = "SYMRELATE_DATA_HOME"
	EnvLegacyContactsCacheHome  = "SYMRELATE_CACHE_HOME"
)

// StorePaths contains the resolved filesystem locations for every absorbed store
// as well as the application base directories.
type StorePaths struct {
	DataDir   string `json:"data_dir,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"`
	CacheDir  string `json:"cache_dir,omitempty"`
	Sidecar   string `json:"sidecar"`
	Retrieval string `json:"retrieval"`
	Ingest    string `json:"ingest"`
	Contacts  string `json:"contacts"`
}

// UserHomeDir returns os.UserHomeDir() or wraps any error with "user home dir: ...".
func UserHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return home, nil
}

// ResolveDataHome returns $XDG_DATA_HOME if non-empty, otherwise $HOME/.local/share.
func ResolveDataHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return dir, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// ResolveConfigHome returns $XDG_CONFIG_HOME if non-empty, otherwise $HOME/.config.
func ResolveConfigHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// ResolveCacheHome returns $XDG_CACHE_HOME if non-empty, otherwise $HOME/.cache.
func ResolveCacheHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); dir != "" {
		return dir, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache"), nil
}

// DataHome returns the resolved base data directory, falling back to ".local/share" on error.
func DataHome() string {
	dir, err := ResolveDataHome()
	if err != nil {
		return filepath.Join(".", ".local", "share")
	}
	return dir
}

// ConfigHome returns the resolved base config directory, falling back to ".config" on error.
func ConfigHome() string {
	dir, err := ResolveConfigHome()
	if err != nil {
		return filepath.Join(".", ".config")
	}
	return dir
}

// CacheHome returns the resolved base cache directory, falling back to ".cache" on error.
func CacheHome() string {
	dir, err := ResolveCacheHome()
	if err != nil {
		return filepath.Join(".", ".cache")
	}
	return dir
}

// DataDir returns the unified application data directory: $XDG_DATA_HOME/symdesk.
func DataDir() string {
	return filepath.Join(DataHome(), AppName)
}

// ConfigDir returns the unified application config directory: $XDG_CONFIG_HOME/symdesk.
func ConfigDir() string {
	return filepath.Join(ConfigHome(), AppName)
}

// CacheDir returns the unified application cache directory: $XDG_CACHE_HOME/symdesk.
func CacheDir() string {
	return filepath.Join(CacheHome(), AppName)
}

// LegacyIngestDataDir returns $XDG_DATA_HOME/symingest.
func LegacyIngestDataDir() string {
	return filepath.Join(DataHome(), LegacyIngestAppName)
}

// LegacyContactsDataDir returns SYMRELATE_DATA_HOME or $XDG_DATA_HOME/symrelate.
func LegacyContactsDataDir() string {
	if env := strings.TrimSpace(os.Getenv(EnvLegacyContactsDataHome)); env != "" {
		return env
	}
	return filepath.Join(DataHome(), LegacyContactsAppName)
}

// LegacyContactsConfigDir returns SYMRELATE_CONFIG_HOME or $XDG_CONFIG_HOME/symrelate.
func LegacyContactsConfigDir() string {
	if env := strings.TrimSpace(os.Getenv(EnvLegacyContactsConfigHome)); env != "" {
		return env
	}
	return filepath.Join(ConfigHome(), LegacyContactsAppName)
}

// LegacyContactsCacheDir returns SYMRELATE_CACHE_HOME or $XDG_CACHE_HOME/symrelate.
func LegacyContactsCacheDir() string {
	if env := strings.TrimSpace(os.Getenv(EnvLegacyContactsCacheHome)); env != "" {
		return env
	}
	return filepath.Join(CacheHome(), LegacyContactsAppName)
}

// LegacyRetrievalDataDir returns $XDG_DATA_HOME/symaira-seek.
func LegacyRetrievalDataDir() string {
	return filepath.Join(DataHome(), LegacyRetrievalAppName)
}

// LegacyRetrievalConfigDir returns $XDG_CONFIG_HOME/symseek.
func LegacyRetrievalConfigDir() string {
	return filepath.Join(ConfigHome(), "symseek")
}

// resolveWithLegacyFallback returns primaryPath if it exists or if no legacyPaths exist.
// If primaryPath does not exist and a legacyPath exists, that existing legacyPath is returned.
func resolveWithLegacyFallback(primaryPath string, legacyPaths ...string) string {
	if fileOrDirExists(primaryPath) {
		return primaryPath
	}
	for _, lp := range legacyPaths {
		if fileOrDirExists(lp) {
			return lp
		}
	}
	return primaryPath
}

func fileOrDirExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// ContactsDBPath returns the resolved database path for symrelate/contacts.
func ContactsDBPath() string {
	primary := filepath.Join(DataDir(), "symrelate.db")
	legacy := filepath.Join(LegacyContactsDataDir(), "symrelate.db")
	return resolveWithLegacyFallback(primary, legacy)
}

// ContactsPathsBundle holds the resolved config, data, and cache directories
// alongside the database path for the contacts facade.
type ContactsPathsBundle struct {
	ConfigDir string
	DataDir   string
	CacheDir  string
	DBPath    string
}

// ContactsPaths returns the directory bundle and DB path for the contacts store.
func ContactsPaths() ContactsPathsBundle {
	dbPath := ContactsDBPath()
	dataDir := DataDir()
	configDir := ConfigDir()
	cacheDir := CacheDir()

	if env := strings.TrimSpace(os.Getenv(EnvLegacyContactsDataHome)); env != "" {
		dataDir = env
	} else if dbPath == filepath.Join(LegacyContactsDataDir(), "symrelate.db") {
		dataDir = LegacyContactsDataDir()
	}
	if env := strings.TrimSpace(os.Getenv(EnvLegacyContactsConfigHome)); env != "" {
		configDir = env
	}
	if env := strings.TrimSpace(os.Getenv(EnvLegacyContactsCacheHome)); env != "" {
		cacheDir = env
	}

	return ContactsPathsBundle{
		ConfigDir: configDir,
		DataDir:   dataDir,
		CacheDir:  cacheDir,
		DBPath:    dbPath,
	}
}

// IngestDBPath returns the resolved database path for symingest.
func IngestDBPath() (string, error) {
	return IngestDataPath("symingest.db")
}

// IngestDataPath resolves a named ingest data artifact (e.g. "symingest.db" or "archive").
func IngestDataPath(name string) (string, error) {
	dataHome, err := ResolveDataHome()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory; set %s explicitly: %w", name, err)
	}
	primary := filepath.Join(dataHome, AppName, name)
	legacy := filepath.Join(dataHome, LegacyIngestAppName, name)
	return resolveWithLegacyFallback(primary, legacy), nil
}

// SidecarRoot returns the persistent per-vault sidecar directory: $XDG_DATA_HOME/symdesk/vaults.
func SidecarRoot() (string, error) {
	dataHome, err := ResolveDataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, AppName, "vaults"), nil
}

// SidecarVaultDir returns the canonical directory for a specific vault root.
func SidecarVaultDir(vaultRoot string) (string, error) {
	canonical, err := filepath.Abs(vaultRoot)
	if err != nil {
		return "", fmt.Errorf("resolve vault for sidecar: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	root, err := SidecarRoot()
	if err != nil {
		return "", err
	}
	if isTemporaryVault(canonical) && strings.TrimSpace(os.Getenv("XDG_DATA_HOME")) == "" {
		root = filepath.Join(os.TempDir(), AppName, "test-vaults")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	return filepath.Join(root, digest[:16]), nil
}

// SidecarPath returns the database path for the sidecar, scoped to vaultRoot when non-empty.
func SidecarPath(vaultRoot string) (string, error) {
	if strings.TrimSpace(vaultRoot) != "" {
		dir, err := SidecarVaultDir(vaultRoot)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "sidecar.db"), nil
	}
	dataHome, err := ResolveDataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, AppName, "sidecar.db"), nil
}

func isTemporaryVault(path string) bool {
	tmp := filepath.Clean(os.TempDir())
	rel, err := filepath.Rel(tmp, filepath.Clean(path))
	return err == nil && rel != ".." && len(rel) > 3 && rel[:4] != ".."+string(filepath.Separator)
}

// RetrievalPath returns the retrieval database path.
// When vaultRoot is non-empty, it returns <SidecarRoot>/<hash>/retrieval.db.
// When vaultRoot is empty, it returns the standalone retrieval index path,
// checking primary symdesk/retrieval.db, symdesk/symseek.db, and legacy symaira-seek/symseek.db.
func RetrievalPath(vaultRoot string) (string, error) {
	if strings.TrimSpace(vaultRoot) != "" {
		dir, err := SidecarVaultDir(vaultRoot)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "retrieval.db"), nil
	}
	dataHome, err := ResolveDataHome()
	if err != nil {
		return "", err
	}
	primary := filepath.Join(dataHome, AppName, "retrieval.db")
	primaryOldName := filepath.Join(dataHome, AppName, "symseek.db")
	legacy := filepath.Join(dataHome, LegacyRetrievalAppName, "symseek.db")
	return resolveWithLegacyFallback(primary, primaryOldName, legacy), nil
}

// ResolveStorePaths resolves the filesystem locations for all absorbed stores.
func ResolveStorePaths(vaultRoot string) (StorePaths, error) {
	sidecarPath, err := SidecarPath(vaultRoot)
	if err != nil {
		return StorePaths{}, err
	}
	retrievalPath, err := RetrievalPath(vaultRoot)
	if err != nil {
		return StorePaths{}, err
	}
	ingestPath, err := IngestDataPath("symingest.db")
	if err != nil {
		return StorePaths{}, err
	}
	contactsPath := ContactsDBPath()

	return StorePaths{
		DataDir:   DataDir(),
		ConfigDir: ConfigDir(),
		CacheDir:  CacheDir(),
		Sidecar:   sidecarPath,
		Retrieval: retrievalPath,
		Ingest:    ingestPath,
		Contacts:  contactsPath,
	}, nil
}
