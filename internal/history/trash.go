package history

import (
	"encoding/json"
	"fmt"
	"io/fs"
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

func trashRelDir() string {
	return filepath.Join(".symdesk", "trash")
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
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cannot trash a directory: %s", relPath)
	}

	if _, err := s.Snapshot(rel); err != nil {
		return nil, err
	}

	if err := root.MkdirAll(trashRelDir(), 0750); err != nil {
		return nil, err
	}

	// Flatten the relative path into a unique trash name.
	base := strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
	name := base
	for i := 1; ; i++ {
		if _, err := root.Stat(filepath.Join(trashRelDir(), name)); os.IsNotExist(err) {
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
	trashRelName := filepath.Join(trashRelDir(), name)
	metaRelName := filepath.Join(trashRelDir(), name+trashMetaSuffix)
	if err := writeFileAtomicRoot(root, metaRelName, meta, 0644); err != nil {
		return nil, err
	}
	if err := root.Rename(rel, trashRelName); err != nil {
		if rmErr := root.Remove(metaRelName); rmErr != nil && !os.IsNotExist(rmErr) {
			return nil, fmt.Errorf("rename failed: %w (cleanup also failed: %v)", err, rmErr)
		}
		return nil, err
	}
	return &entry, nil
}

// TrashList returns all trash entries, newest deletion first.
func (s *Store) TrashList() ([]TrashEntry, error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	items, err := fs.ReadDir(root.FS(), trashRelDir())
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
		data, err := root.ReadFile(filepath.Join(trashRelDir(), item.Name()))
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
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	if _, err := root.Stat(rel); err == nil {
		return nil, fmt.Errorf("cannot restore %s: %s already exists", name, entry.OriginalPath)
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	if err := root.Rename(filepath.Join(trashRelDir(), entry.Name), rel); err != nil {
		return nil, err
	}
	if err := root.Remove(filepath.Join(trashRelDir(), entry.Name+trashMetaSuffix)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return entry, nil
}

// TrashPurge permanently removes trash items deleted more than maxAge ago.
// maxAge <= 0 purges everything. Returns the number of purged items.
func (s *Store) TrashPurge(maxAge time.Duration) (int, error) {
	entries, err := s.TrashList()
	if err != nil {
		return 0, err
	}
	root, err := s.openRoot()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	purged := 0
	for _, e := range entries {
		if maxAge > 0 && e.DeletedAt.After(cutoff) {
			continue
		}
		if err := root.Remove(filepath.Join(trashRelDir(), e.Name)); err != nil && !os.IsNotExist(err) {
			return purged, err
		}
		if err := root.Remove(filepath.Join(trashRelDir(), e.Name+trashMetaSuffix)); err != nil && !os.IsNotExist(err) {
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
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	data, err := root.ReadFile(filepath.Join(trashRelDir(), name+trashMetaSuffix))
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
