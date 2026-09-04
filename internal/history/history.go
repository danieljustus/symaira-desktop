// Package history provides local version snapshots and a soft-delete trash
// for vault files. Snapshots are content-addressed blobs stored under
// <vaultRoot>/.symdesk/history, the trash lives under
// <vaultRoot>/.symdesk/trash. Both directories are hidden and therefore
// invisible to vault.Walk and the sidecar index.
package history

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

	rootOnce sync.Once
	root     *os.Root
	rootErr  error
}

// NewStore creates a Store rooted at vaultRoot. No directories are created
// until the first snapshot or trash operation.
func NewStore(vaultRoot string) *Store {
	return &Store{vaultRoot: vaultRoot}
}

// openRoot lazily opens (and caches) an *os.Root confined to vaultRoot.
// Every filesystem access in this package goes through it — the same
// os.Root pattern internal/selfhost uses — so a snapshot, restore, trash, or
// checkpoint operation can never be redirected outside the vault via a
// symlink or a crafted relative path, closing the path-traversal and
// symlink-TOCTOU findings that raw path joins left open.
func (s *Store) openRoot() (*os.Root, error) {
	s.rootOnce.Do(func() {
		s.root, s.rootErr = os.OpenRoot(s.vaultRoot)
	})
	return s.root, s.rootErr
}

// ---- vaultRoot-relative path helpers (used for os.Root-scoped I/O) ----

func historyRelDir() string {
	return filepath.Join(".symdesk", "history")
}

func objectsRelDir() string {
	return filepath.Join(historyRelDir(), "objects")
}

func manifestRelPath(relPath string) (string, error) {
	rel, err := cleanRel(relPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(historyRelDir(), "manifest", rel+".json"), nil
}

// objectsDir is the absolute form of objectsRelDir. It exists for tests
// that need to inspect the on-disk object store directly; production code
// only ever accesses it through the os.Root-scoped helpers above.
func (s *Store) objectsDir() string {
	return filepath.Join(s.vaultRoot, objectsRelDir())
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
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	data, err := root.ReadFile(rel)
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

	if err := root.MkdirAll(objectsRelDir(), 0750); err != nil {
		return nil, err
	}
	objRel := filepath.Join(objectsRelDir(), id)
	if _, err := root.Stat(objRel); os.IsNotExist(err) {
		if err := writeFileAtomicRoot(root, objRel, data, 0644); err != nil {
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
	relMp, err := manifestRelPath(relPath)
	if err != nil {
		return nil, err
	}
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	data, err := root.ReadFile(relMp)
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
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	data, err := root.ReadFile(filepath.Join(objectsRelDir(), id))
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

	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	if err := writeFileAtomicRoot(root, rel, data, 0644); err != nil {
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

	root, err := s.openRoot()
	if err != nil {
		return removed, err
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
			relPath, err := checkpointRelPath(cp.TaskID)
			if err != nil {
				return removed, err
			}
			if err := root.Remove(relPath); err != nil && !os.IsNotExist(err) {
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

	manifestRelRoot := filepath.Join(historyRelDir(), "manifest")
	err = fs.WalkDir(root.FS(), manifestRelRoot, func(relPath string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(relPath, ".json") {
			return nil
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(relPath, manifestRelRoot+"/"), ".json")

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
			return root.Remove(relPath)
		}
		return s.writeManifest(rel, kept)
	})
	if err != nil {
		return removed, err
	}

	// Garbage-collect unreferenced objects.
	objs, err := fs.ReadDir(root.FS(), objectsRelDir())
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
		if err := root.Remove(filepath.Join(objectsRelDir(), o.Name())); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (s *Store) writeManifest(rel string, entries []Entry) error {
	relMp, err := manifestRelPath(rel)
	if err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(relMp), 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicRoot(root, relMp, data, 0644)
}

func isHexID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// writeFileAtomicRoot atomically writes data to name (relative to root) via
// a temp file created alongside it and renamed into place. Every step is
// confined to root, so the write can never be redirected outside vaultRoot.
func writeFileAtomicRoot(root *os.Root, name string, data []byte, perm os.FileMode) error {
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(name), ".symdesk-history-")
	if err != nil {
		return err
	}
	cleanup := func() { _ = tmp.Close(); _ = root.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = root.Remove(tmpName)
		return err
	}
	if err := root.Chmod(tmpName, perm); err != nil {
		_ = root.Remove(tmpName)
		return err
	}
	if err := root.Rename(tmpName, name); err != nil {
		_ = root.Remove(tmpName)
		return err
	}
	return nil
}

// createRootTemp creates a uniquely-named temp file inside dir (relative to
// root) and returns it along with its root-relative name.
func createRootTemp(root *os.Root, dir, prefix string) (*os.File, string, error) {
	for range 100 {
		random := make([]byte, 12)
		if _, err := cryptorand.Read(random); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(random)+".tmp")
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", fmt.Errorf("create temporary file: too many collisions")
}

// PurgePaths permanently removes history manifests and checkpoint residue for
// the supplied vault-relative paths, then garbage-collects unreferenced blobs.
// It is intentionally explicit; ordinary retention pruning never calls it.
func (s *Store) PurgePaths(relPaths ...string) error {
	targets := make(map[string]bool, len(relPaths))
	for _, relPath := range relPaths {
		rel, err := cleanRel(relPath)
		if err != nil {
			return err
		}
		targets[filepath.ToSlash(rel)] = true
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	manifestRoot := filepath.Join(historyRelDir(), "manifest")
	err = fs.WalkDir(root.FS(), manifestRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(path, manifestRoot+"/"), ".json")
		if targets[filepath.ToSlash(rel)] {
			if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	checkpointItems, err := fs.ReadDir(root.FS(), checkpointsRelDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	checkpoints := make([]Checkpoint, 0, len(checkpointItems))
	for _, item := range checkpointItems {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
			continue
		}
		data, err := root.ReadFile(filepath.Join(checkpointsRelDir(), item.Name()))
		if err != nil {
			return err
		}
		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			return fmt.Errorf("corrupt checkpoint manifest %q: %w", item.Name(), err)
		}
		checkpoints = append(checkpoints, cp)
	}
	survivingCheckpoints := make([]Checkpoint, 0, len(checkpoints))
	for _, cp := range checkpoints {
		matched := false
		files := cp.Files[:0]
		for _, file := range cp.Files {
			if targets[filepath.ToSlash(file.RelPath)] {
				matched = true
				continue
			}
			files = append(files, file)
		}
		cp.Files = files
		newFiles := cp.NewFiles[:0]
		for _, path := range cp.NewFiles {
			if targets[filepath.ToSlash(path)] {
				matched = true
				continue
			}
			newFiles = append(newFiles, path)
		}
		cp.NewFiles = newFiles
		skipped := cp.Skipped[:0]
		for _, path := range cp.Skipped {
			if targets[filepath.ToSlash(path)] {
				matched = true
				continue
			}
			skipped = append(skipped, path)
		}
		cp.Skipped = skipped
		if matched {
			if len(cp.Files) == 0 && len(cp.NewFiles) == 0 && len(cp.Skipped) == 0 {
				rel, err := checkpointRelPath(cp.TaskID)
				if err != nil {
					return err
				}
				if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
			if err := s.saveCheckpoint(&cp); err != nil {
				return err
			}
		}
		survivingCheckpoints = append(survivingCheckpoints, cp)
	}
	referenced := make(map[string]bool)
	err = fs.WalkDir(root.FS(), manifestRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("corrupt history manifest %q: %w", path, err)
		}
		for _, entry := range entries {
			referenced[entry.ID] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, cp := range survivingCheckpoints {
		for _, file := range cp.Files {
			referenced[file.Entry.ID] = true
		}
	}
	objects, err := fs.ReadDir(root.FS(), objectsRelDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, object := range objects {
		if object.IsDir() || referenced[object.Name()] {
			continue
		}
		if err := root.Remove(filepath.Join(objectsRelDir(), object.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
