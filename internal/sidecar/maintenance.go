package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const metadataFileName = "metadata.json"

type sidecarMetadata struct {
	VaultPath string    `json:"vault_path"`
	LastUsed  time.Time `json:"last_used"`
}

// SidecarRoot returns the persistent per-vault sidecar directory.
func SidecarRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataRoot, "symdesk", "vaults"), nil
}

func isTemporaryVault(path string) bool {
	tmp := filepath.Clean(os.TempDir())
	rel, err := filepath.Rel(tmp, filepath.Clean(path))
	return err == nil && rel != ".." && len(rel) > 3 && rel[:4] != ".."+string(filepath.Separator)
}

func recordSidecarMetadata(dir, vaultPath string) error {
	payload, err := json.Marshal(sidecarMetadata{VaultPath: vaultPath, LastUsed: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("encode sidecar metadata: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create sidecar directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".metadata-*.tmp")
	if err != nil {
		return fmt.Errorf("create sidecar metadata: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure sidecar metadata: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write sidecar metadata: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close sidecar metadata: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, metadataFileName)); err != nil {
		return fmt.Errorf("install sidecar metadata: %w", err)
	}
	return nil
}

// SidecarEntry describes one persistent per-vault sidecar.
type SidecarEntry struct {
	Path      string    `json:"path"`
	VaultPath string    `json:"vault_path"`
	LastUsed  time.Time `json:"last_used"`
	Size      int64     `json:"size"`
	Orphan    bool      `json:"orphan"`
	Metadata  bool      `json:"metadata"`
}

// ListSidecars returns the inventory without opening or modifying databases.
func ListSidecars() ([]SidecarEntry, error) {
	root, err := SidecarRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sidecar root: %w", err)
	}
	result := make([]SidecarEntry, 0, len(entries))
	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		dbPath := filepath.Join(root, dir.Name(), "sidecar.db")
		info, err := os.Stat(dbPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat sidecar %s: %w", dbPath, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		entry := SidecarEntry{Path: dbPath, Size: info.Size()}
		metadata, err := os.ReadFile(filepath.Join(root, dir.Name(), metadataFileName)) //nolint:gosec // dir.Name() comes from the bounded SidecarRoot directory
		if err == nil {
			var recorded sidecarMetadata
			if json.Unmarshal(metadata, &recorded) == nil && recorded.VaultPath != "" {
				entry.VaultPath = recorded.VaultPath
				entry.LastUsed = recorded.LastUsed
				entry.Metadata = true
				_, statErr := os.Stat(recorded.VaultPath)
				entry.Orphan = errors.Is(statErr, fs.ErrNotExist)
			}
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

// RemoveOrphanSidecars removes only metadata-bearing sidecars whose vault path
// no longer exists. Live and unidentified sidecars are never touched.
func RemoveOrphanSidecars() (int, error) {
	entries, err := ListSidecars()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.Orphan || !entry.Metadata {
			continue
		}
		dir := filepath.Dir(entry.Path)
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("remove orphan sidecar %s: %w", entry.Path, err)
		}
		removed++
	}
	return removed, nil
}
