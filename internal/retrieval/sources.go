package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// ErrSourceInsideVault prevents an external source registration from
	// weakening the vault boundary or turning a vault subtree into a second
	// indexing scope.
	ErrSourceInsideVault = errors.New("external source must be outside the vault")
	ErrSourceNotFound    = errors.New("external source directory does not exist")
)

const sourceRegistryVersion = 1

type Source struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type sourceRegistryFile struct {
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
}

// SourceRegistry stores external folders for one vault. It is deliberately
// kept under the vault's .symdesk directory; the folder contents remain
// read-only to SymDesk and Markdown notes remain the vault source of truth.
type SourceRegistry struct {
	vaultRoot string
	path      string
	mu        sync.Mutex
}

// NewSourceRegistry opens the registry for vaultRoot without creating any
// file. The registry is per-vault even though the hybrid retrieval database is
// shared, allowing callers to filter external hits by the active vault.
func NewSourceRegistry(vaultRoot string) (*SourceRegistry, error) {
	canonical, err := canonicalDirectory(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("validate vault root: %w", err)
	}
	return &SourceRegistry{
		vaultRoot: canonical,
		path:      filepath.Join(canonical, ".symdesk", "search-sources.json"),
	}, nil
}

// ValidateExternalFolder validates and canonicalizes a folder before it is
// registered. Symlink resolution is part of validation so the stable identity
// cannot be changed by registering an alias, and no path inside the vault can
// escape the vault boundary through a symlink.
func ValidateExternalFolder(vaultRoot, sourcePath string) (string, error) {
	vault, err := canonicalDirectory(vaultRoot)
	if err != nil {
		return "", fmt.Errorf("validate vault root: %w", err)
	}
	inputAbs, err := filepath.Abs(filepath.Clean(sourcePath))
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	vaultAbs, err := filepath.Abs(filepath.Clean(vaultRoot))
	if err != nil {
		return "", fmt.Errorf("resolve vault root: %w", err)
	}
	if inputAbs == vaultAbs || isWithinPath(inputAbs, vaultAbs) {
		return "", ErrSourceInsideVault
	}
	if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(inputAbs)); parentErr == nil && (parent == vault || isWithinPath(parent, vault)) {
		return "", ErrSourceInsideVault
	}
	source, err := canonicalDirectory(inputAbs)
	if err != nil {
		if errors.Is(err, ErrSourceNotFound) {
			return "", err
		}
		return "", fmt.Errorf("validate source folder: %w", err)
	}
	if source == vault || isWithinPath(source, vault) {
		return "", ErrSourceInsideVault
	}
	return source, nil
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrSourceNotFound
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrSourceNotFound, filepath.Clean(abs))
		}
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("source path is not a directory: %s", canonical)
	}
	f, err := os.Open(canonical) // #nosec G304 -- caller supplies an explicit local folder.
	if err != nil {
		return "", fmt.Errorf("source directory is not readable: %w", err)
	}
	_, readErr := f.Readdirnames(1)
	closeErr := f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("source directory is not readable: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close source directory: %w", closeErr)
	}
	return filepath.Clean(canonical), nil
}

func isWithinPath(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func sourceID(canonicalPath string) string {
	sum := sha256.Sum256([]byte(canonicalPath))
	return "folder-" + hex.EncodeToString(sum[:])
}

// Add validates and persists one external folder. Registering the same
// canonical folder twice is idempotent and returns its existing identity.
func (r *SourceRegistry) Add(sourcePath string) (Source, error) {
	canonical, err := ValidateExternalFolder(r.vaultRoot, sourcePath)
	if err != nil {
		return Source{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := r.loadLocked()
	if err != nil {
		return Source{}, err
	}
	candidate := Source{ID: sourceID(canonical), Path: canonical}
	for _, source := range state.Sources {
		if source.ID == candidate.ID || source.Path == candidate.Path {
			return source, nil
		}
	}
	state.Sources = append(state.Sources, candidate)
	if err := r.saveLocked(state); err != nil {
		return Source{}, err
	}
	return candidate, nil
}

func (r *SourceRegistry) List() ([]Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.loadLocked()
	if err != nil {
		return nil, err
	}
	out := append([]Source(nil), state.Sources...)
	return out, nil
}

// Remove deletes a registry entry by stable ID or canonical path. It never
// deletes or mutates the external folder itself.
func (r *SourceRegistry) Remove(idOrPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.loadLocked()
	if err != nil {
		return err
	}
	for i, source := range state.Sources {
		if source.ID == idOrPath || source.Path == idOrPath {
			state.Sources = append(state.Sources[:i], state.Sources[i+1:]...)
			return r.saveLocked(state)
		}
	}
	return fmt.Errorf("external source %q not registered", idOrPath)
}

func (r *SourceRegistry) loadLocked() (sourceRegistryFile, error) {
	data, err := os.ReadFile(r.path) // #nosec G304 -- path is derived from a validated vault root.
	if errors.Is(err, os.ErrNotExist) {
		return sourceRegistryFile{Version: sourceRegistryVersion, Sources: []Source{}}, nil
	}
	if err != nil {
		return sourceRegistryFile{}, fmt.Errorf("read source registry: %w", err)
	}
	var state sourceRegistryFile
	if err := json.Unmarshal(data, &state); err != nil {
		return sourceRegistryFile{}, fmt.Errorf("parse source registry: %w", err)
	}
	if state.Version != sourceRegistryVersion {
		return sourceRegistryFile{}, fmt.Errorf("unsupported source registry version: %d", state.Version)
	}
	if state.Sources == nil {
		state.Sources = []Source{}
	}
	return state, nil
}

func (r *SourceRegistry) saveLocked(state sourceRegistryFile) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create source registry directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode source registry: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".search-sources-*.tmp")
	if err != nil {
		return fmt.Errorf("create source registry temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect source registry: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write source registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close source registry: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("replace source registry: %w", err)
	}
	return nil
}
