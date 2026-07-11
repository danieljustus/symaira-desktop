package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TrashEntry describes a soft-deleted vault file.
type TrashEntry struct {
	// Name is the unique name of the item inside the trash.
	Name string `json:"name"`
	// OriginalPath is the vault-relative path the file was deleted from.
	OriginalPath string `json:"original_path"`
	// DeletedAt is when the file was moved to the trash (UTC).
	DeletedAt time.Time `json:"deleted_at"`
	// Size is the file size in bytes at deletion time.
	Size int64 `json:"size"`
}

func (s *Store) trashDir() string {
	return filepath.Join(s.vaultRoot, ".symdesk", "trash")
}

const trashMetaSuffix = ".trashinfo.json"

// Trash moves the vault file at relPath into the trash instead of deleting
// it. A snapshot of the final content is taken first so even a purged trash
// item remains recoverable until history retention drops it.
func (s *Store) Trash(relPath string) (*TrashEntry, error) {
	rel, err := cleanRel(relPath)
	if err != nil {
		return nil, err
	}
	src := filepath.Join(s.vaultRoot, rel)
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cannot trash a directory: %s", relPath)
	}

	if _, err := s.Snapshot(rel); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(s.trashDir(), 0755); err != nil {
		return nil, err
	}

	// Flatten the relative path into a unique trash name.
	base := strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
	name := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(s.trashDir(), name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s.%d", base, i)
	}

	entry := TrashEntry{
		Name:         name,
		OriginalPath: filepath.ToSlash(rel),
		DeletedAt:    time.Now().UTC(),
		Size:         info.Size(),
	}
	meta, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(s.trashDir(), name+trashMetaSuffix), meta, 0644); err != nil {
		return nil, err
	}
	if err := os.Rename(src, filepath.Join(s.trashDir(), name)); err != nil {
		os.Remove(filepath.Join(s.trashDir(), name+trashMetaSuffix))
		return nil, err
	}
	return &entry, nil
}

// TrashList returns all trash entries, newest deletion first.
func (s *Store) TrashList() ([]TrashEntry, error) {
	items, err := os.ReadDir(s.trashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []TrashEntry
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), trashMetaSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.trashDir(), item.Name()))
		if err != nil {
			return nil, err
		}
		var e TrashEntry
		if err := json.Unmarshal(data, &e); err != nil {
			continue // skip corrupt metadata rather than failing the listing
		}
		entries = append(entries, e)
	}
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].DeletedAt.After(entries[i].DeletedAt) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	return entries, nil
}

// TrashRestore moves a trash item back to its original vault path. If the
// original path is occupied, the restore fails and the trash item is kept.
func (s *Store) TrashRestore(name string) (*TrashEntry, error) {
	entry, err := s.trashEntry(name)
	if err != nil {
		return nil, err
	}
	rel, err := cleanRel(entry.OriginalPath)
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(s.vaultRoot, rel)
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("cannot restore %s: %s already exists", name, entry.OriginalPath)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, err
	}
	if err := os.Rename(filepath.Join(s.trashDir(), entry.Name), dst); err != nil {
		return nil, err
	}
	os.Remove(filepath.Join(s.trashDir(), entry.Name+trashMetaSuffix))
	return entry, nil
}

// TrashPurge permanently removes trash items deleted more than maxAge ago.
// maxAge <= 0 purges everything. Returns the number of purged items.
func (s *Store) TrashPurge(maxAge time.Duration) (int, error) {
	entries, err := s.TrashList()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	purged := 0
	for _, e := range entries {
		if maxAge > 0 && e.DeletedAt.After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.trashDir(), e.Name)); err != nil && !os.IsNotExist(err) {
			return purged, err
		}
		if err := os.Remove(filepath.Join(s.trashDir(), e.Name+trashMetaSuffix)); err != nil && !os.IsNotExist(err) {
			return purged, err
		}
		purged++
	}
	return purged, nil
}

func (s *Store) trashEntry(name string) (*TrashEntry, error) {
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid trash item name: %q", name)
	}
	data, err := os.ReadFile(filepath.Join(s.trashDir(), name+trashMetaSuffix))
	if err != nil {
		return nil, fmt.Errorf("trash item %q not found: %w", name, err)
	}
	var e TrashEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("corrupt trash metadata for %q: %w", name, err)
	}
	e.Name = name
	return &e, nil
}
