// Package history provides local version snapshots and a soft-delete trash
// for vault files. Snapshots are content-addressed blobs stored under
// <vaultRoot>/.symdesk/history, the trash lives under
// <vaultRoot>/.symdesk/trash. Both directories are hidden and therefore
// invisible to vault.Walk and the sidecar index.
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry describes one stored snapshot of a vault file.
type Entry struct {
	// ID is the hex SHA-256 of the snapshot content and addresses the blob.
	ID string `json:"id"`
	// Timestamp is when the snapshot was taken (UTC, RFC 3339 nano).
	Timestamp time.Time `json:"timestamp"`
	// Size is the content size in bytes.
	Size int64 `json:"size"`
}

// RetentionPolicy controls Prune.
type RetentionPolicy struct {
	// MaxPerFile keeps at most this many snapshots per file (0 = unlimited).
	MaxPerFile int
	// MaxAge drops snapshots older than this (0 = unlimited). The newest
	// snapshot of a file is always kept regardless of age.
	MaxAge time.Duration
	// MaxCheckpointAge drops task checkpoints older than this (0 =
	// unlimited). Aged-out checkpoints lose their blob protection before
	// the GC runs, so their blobs become collectable once no manifest
	// references them.
	MaxCheckpointAge time.Duration
}

// Store manages snapshots and trash for a single vault.
type Store struct {
	vaultRoot string
}

// NewStore creates a Store rooted at vaultRoot. No directories are created
// until the first snapshot or trash operation.
func NewStore(vaultRoot string) *Store {
	return &Store{vaultRoot: vaultRoot}
}

func (s *Store) historyDir() string {
	return filepath.Join(s.vaultRoot, ".symdesk", "history")
}

func (s *Store) objectsDir() string {
	return filepath.Join(s.historyDir(), "objects")
}

func (s *Store) manifestPath(relPath string) (string, error) {
	rel, err := cleanRel(relPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.historyDir(), "manifest", rel+".json"), nil
}

// cleanRel normalizes a vault-relative path and rejects traversal.
func cleanRel(relPath string) (string, error) {
	rel := filepath.ToSlash(filepath.Clean(relPath))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") || rel == ".." || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("invalid vault-relative path: %q", relPath)
	}
	return filepath.FromSlash(rel), nil
}

// Snapshot stores the current content of the vault file at relPath. It is a
// no-op when the file does not exist or when its content equals the most
// recent snapshot, so callers can invoke it unconditionally before any write.
func (s *Store) Snapshot(relPath string) (*Entry, error) {
	rel, err := cleanRel(relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.vaultRoot, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])

	entries, err := s.List(rel)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 && entries[0].ID == id {
		return &entries[0], nil // unchanged since last snapshot
	}

	if err := os.MkdirAll(s.objectsDir(), 0755); err != nil {
		return nil, err
	}
	objPath := filepath.Join(s.objectsDir(), id)
	if _, err := os.Stat(objPath); os.IsNotExist(err) {
		if err := writeFileAtomic(objPath, data, 0644); err != nil {
			return nil, err
		}
	}

	entry := Entry{ID: id, Timestamp: time.Now().UTC(), Size: int64(len(data))}
	entries = append([]Entry{entry}, entries...)
	if err := s.writeManifest(rel, entries); err != nil {
		return nil, err
	}
	return &entry, nil
}

// List returns the snapshots for relPath, newest first.
func (s *Store) List(relPath string) ([]Entry, error) {
	mp, err := s.manifestPath(relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(mp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("corrupt history manifest for %s: %w", relPath, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Timestamp.After(entries[j].Timestamp) })
	return entries, nil
}

// Content returns the stored content of a snapshot.
func (s *Store) Content(id string) ([]byte, error) {
	if !isHexID(id) {
		return nil, fmt.Errorf("invalid snapshot id: %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.objectsDir(), id))
	if err != nil {
		return nil, fmt.Errorf("snapshot object %s not found: %w", id, err)
	}
	return data, nil
}

// Restore writes the snapshot identified by id (or, if id is empty, the most
// recent snapshot) back to the vault file at relPath. The current file
// content is snapshotted first, so a restore is itself undoable. id may be a
// unique prefix of the full hash.
func (s *Store) Restore(relPath, id string) (*Entry, error) {
	rel, err := cleanRel(relPath)
	if err != nil {
		return nil, err
	}
	entries, err := s.List(rel)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no snapshots recorded for %s", relPath)
	}

	var target *Entry
	if id == "" {
		target = &entries[0]
	} else {
		for i := range entries {
			if strings.HasPrefix(entries[i].ID, id) {
				if target != nil {
					return nil, fmt.Errorf("snapshot id prefix %q is ambiguous for %s", id, relPath)
				}
				target = &entries[i]
			}
		}
		if target == nil {
			return nil, fmt.Errorf("no snapshot %q for %s", id, relPath)
		}
	}

	data, err := s.Content(target.ID)
	if err != nil {
		return nil, err
	}

	// Preserve the pre-restore state as its own snapshot.
	if _, err := s.Snapshot(rel); err != nil {
		return nil, err
	}

	dst := filepath.Join(s.vaultRoot, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(dst, data, 0644); err != nil {
		return nil, err
	}
	return target, nil
}

// Prune applies the retention policy across all files and garbage-collects
// unreferenced objects. Aged-out task checkpoints are pruned first, then the
// surviving checkpoints' blobs are protected from GC. It returns the number
// of snapshots and checkpoints removed.
func (s *Store) Prune(policy RetentionPolicy) (int, error) {
	removed := 0
	referenced := map[string]bool{}
	cutoff := time.Time{}
	if policy.MaxAge > 0 {
		cutoff = time.Now().UTC().Add(-policy.MaxAge)
	}

	// Prune aged-out task checkpoints first, so their blobs lose protection
	// before the reference scan below and can be collected once no manifest
	// references them anymore.
	if policy.MaxCheckpointAge > 0 {
		cpCutoff := time.Now().UTC().Add(-policy.MaxCheckpointAge)
		checkpoints, err := s.ListCheckpoints()
		if err != nil {
			return removed, err
		}
		for _, cp := range checkpoints {
			if !cp.Timestamp.Before(cpCutoff) {
				continue
			}
			path, err := s.checkpointPath(cp.TaskID)
			if err != nil {
				return removed, err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return removed, err
			}
			removed++
		}
	}

	// Protect every blob still referenced by a surviving task checkpoint, so
	// the content-addressed GC below never breaks a pending undo-task.
	checkpoints, err := s.ListCheckpoints()
	if err != nil {
		return removed, err
	}
	for _, cp := range checkpoints {
		for _, f := range cp.Files {
			referenced[f.Entry.ID] = true
		}
	}

	manifestRoot := filepath.Join(s.historyDir(), "manifest")
	err = filepath.Walk(manifestRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, err := filepath.Rel(manifestRoot, path)
		if err != nil {
			return err
		}
		rel = strings.TrimSuffix(rel, ".json")

		entries, err := s.List(rel)
		if err != nil {
			return err
		}
		kept := entries[:0]
		for i, e := range entries {
			drop := false
			if policy.MaxPerFile > 0 && len(kept) >= policy.MaxPerFile {
				drop = true
			}
			// Always keep the newest snapshot regardless of age.
			if !drop && i > 0 && !cutoff.IsZero() && e.Timestamp.Before(cutoff) {
				drop = true
			}
			if drop {
				removed++
				continue
			}
			kept = append(kept, e)
		}
		for _, e := range kept {
			referenced[e.ID] = true
		}
		if len(kept) == len(entries) {
			return nil
		}
		if len(kept) == 0 {
			return os.Remove(path)
		}
		return s.writeManifest(rel, kept)
	})
	if err != nil {
		return removed, err
	}

	// Garbage-collect unreferenced objects.
	objs, err := os.ReadDir(s.objectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return removed, nil
		}
		return removed, err
	}
	for _, o := range objs {
		if o.IsDir() || referenced[o.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(s.objectsDir(), o.Name())); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (s *Store) writeManifest(rel string, entries []Entry) error {
	mp, err := s.manifestPath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mp), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(mp, data, 0644)
}

func isHexID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// writeFileAtomic writes data via a temp file + rename in the target dir.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".symdesk-history-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
