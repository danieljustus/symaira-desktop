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

// PreflightPurgePaths validates every history manifest, referenced object, and
// checkpoint before a destructive purge. It never writes or removes anything.
func (s *Store) PreflightPurgePaths(relPaths ...string) error {
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
	_ = targets
	return preflightPurgeRoot(root, targets)
}

func preflightPurgeRoot(root *os.Root, targets map[string]bool) error {
	manifestRoot := filepath.Join(historyRelDir(), "manifest")
	if err := fs.WalkDir(root.FS(), manifestRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return fmt.Errorf("invalid history manifest entry %q", path)
		}
		data, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		entries, err := decodeHistoryManifest(data)
		if err != nil {
			return fmt.Errorf("invalid history manifest %q: %w", path, err)
		}
		for _, entry := range entries {
			if err := verifyHistoryEntry(root, entry); err != nil {
				return fmt.Errorf("invalid history manifest %q: %w", path, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	checkpointItems, err := fs.ReadDir(root.FS(), checkpointsRelDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, item := range checkpointItems {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
			return fmt.Errorf("invalid checkpoint entry %q", item.Name())
		}
		path := filepath.Join(checkpointsRelDir(), item.Name())
		data, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		cp, err := decodeCheckpointManifest(data)
		if err != nil {
			return fmt.Errorf("invalid checkpoint manifest %q: %w", item.Name(), err)
		}
		if expected := strings.TrimSuffix(item.Name(), ".json"); cp.TaskID != expected {
			return fmt.Errorf("invalid checkpoint manifest %q: task_id %q does not match filename", item.Name(), cp.TaskID)
		}
		for _, file := range cp.Files {
			if err := verifyHistoryEntry(root, file.Entry); err != nil {
				return fmt.Errorf("invalid checkpoint manifest %q: %w", item.Name(), err)
			}
		}
	}

	objects, err := fs.ReadDir(root.FS(), objectsRelDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, object := range objects {
		if object.IsDir() || object.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid history object %q", object.Name())
		}
	}
	return nil
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

	// Read and validate every manifest and checkpoint before touching any of
	// them. Purging recovery metadata is destructive: a broken survivor must
	// fail closed rather than allowing GC to make the damage permanent.
	manifestRoot := filepath.Join(historyRelDir(), "manifest")
	manifests := make([]purgeManifest, 0)
	walkErr := fs.WalkDir(root.FS(), manifestRoot, func(path string, d fs.DirEntry, walkErr error) error {
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
		entries, err := decodeHistoryManifest(data)
		if err != nil {
			return fmt.Errorf("invalid history manifest %q: %w", path, err)
		}
		for _, entry := range entries {
			if err := verifyHistoryEntry(root, entry); err != nil {
				return fmt.Errorf("invalid history manifest %q: %w", path, err)
			}
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(path, manifestRoot+"/"), ".json")
		manifests = append(manifests, purgeManifest{
			path:    path,
			relPath: filepath.ToSlash(rel),
			entries: entries,
		})
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	checkpointItems, err := fs.ReadDir(root.FS(), checkpointsRelDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	checkpoints := make([]purgeCheckpoint, 0, len(checkpointItems))
	for _, item := range checkpointItems {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
			continue
		}
		path := filepath.Join(checkpointsRelDir(), item.Name())
		data, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		cp, err := decodeCheckpointManifest(data)
		if err != nil {
			return fmt.Errorf("invalid checkpoint manifest %q: %w", item.Name(), err)
		}
		if expected := strings.TrimSuffix(item.Name(), ".json"); cp.TaskID != expected {
			return fmt.Errorf("invalid checkpoint manifest %q: task_id %q does not match filename", item.Name(), cp.TaskID)
		}
		for _, file := range cp.Files {
			if err := verifyHistoryEntry(root, file.Entry); err != nil {
				return fmt.Errorf("invalid checkpoint manifest %q: %w", item.Name(), err)
			}
		}
		checkpoints = append(checkpoints, purgeCheckpoint{path: path, checkpoint: cp})
	}

	// Compute all post-purge checkpoint states in memory. No checkpoint write
	// occurs until the complete preflight above has succeeded.
	survivingCheckpoints := make([]purgeCheckpoint, 0, len(checkpoints))
	for _, record := range checkpoints {
		cp := record.checkpoint
		matched := false
		files := make([]CheckpointFile, 0, len(cp.Files))
		for _, file := range cp.Files {
			if targets[filepath.ToSlash(file.RelPath)] {
				matched = true
				continue
			}
			files = append(files, file)
		}
		cp.Files = files
		newFiles := make([]string, 0, len(cp.NewFiles))
		for _, path := range cp.NewFiles {
			if targets[filepath.ToSlash(path)] {
				matched = true
				continue
			}
			newFiles = append(newFiles, path)
		}
		cp.NewFiles = newFiles
		skipped := make([]string, 0, len(cp.Skipped))
		for _, path := range cp.Skipped {
			if targets[filepath.ToSlash(path)] {
				matched = true
				continue
			}
			skipped = append(skipped, path)
		}
		cp.Skipped = skipped
		survivingCheckpoints = append(survivingCheckpoints, purgeCheckpoint{
			path:       record.path,
			checkpoint: cp,
			matched:    matched,
		})
	}

	// Only now mutate manifests and checkpoint references.
	for _, manifest := range manifests {
		if targets[manifest.relPath] {
			if err := root.Remove(manifest.path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, record := range survivingCheckpoints {
		if !record.matched {
			continue
		}
		if len(record.checkpoint.Files) == 0 && len(record.checkpoint.NewFiles) == 0 && len(record.checkpoint.Skipped) == 0 {
			if err := root.Remove(record.path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := s.saveCheckpoint(&record.checkpoint); err != nil {
			return err
		}
	}

	referenced := make(map[string]bool)
	for _, manifest := range manifests {
		if targets[manifest.relPath] {
			continue
		}
		for _, entry := range manifest.entries {
			referenced[entry.ID] = true
		}
	}
	for _, record := range survivingCheckpoints {
		for _, file := range record.checkpoint.Files {
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

type purgeManifest struct {
	path    string
	relPath string
	entries []Entry
}

type purgeCheckpoint struct {
	path       string
	checkpoint Checkpoint
	matched    bool
}

func decodeHistoryManifest(data []byte) ([]Entry, error) {
	if isJSONNull(data) {
		return nil, fmt.Errorf("manifest must be a non-null array")
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, fmt.Errorf("manifest must be a non-null array")
	}
	return entries, nil
}

func decodeCheckpointManifest(data []byte) (Checkpoint, error) {
	if isJSONNull(data) {
		return Checkpoint{}, fmt.Errorf("checkpoint must be a non-null object")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Checkpoint{}, err
	}
	if raw == nil {
		return Checkpoint{}, fmt.Errorf("checkpoint must be a non-null object")
	}
	for _, key := range []string{"files", "new_files", "skipped"} {
		if value, ok := raw[key]; ok && isJSONNull(value) {
			return Checkpoint{}, fmt.Errorf("%s must be a non-null array", key)
		}
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return Checkpoint{}, err
	}
	if cp.TaskID == "" || cp.Timestamp.IsZero() {
		return Checkpoint{}, fmt.Errorf("task_id and timestamp are required")
	}
	if err := validateTaskID(cp.TaskID); err != nil {
		return Checkpoint{}, err
	}
	seen := make(map[string]bool, len(cp.Files)+len(cp.NewFiles)+len(cp.Skipped))
	for _, file := range cp.Files {
		rel, err := cleanRel(file.RelPath)
		if err != nil || filepath.ToSlash(rel) != file.RelPath {
			return Checkpoint{}, fmt.Errorf("invalid checkpoint file path %q", file.RelPath)
		}
		if seen[file.RelPath] {
			return Checkpoint{}, fmt.Errorf("duplicate checkpoint path %q", file.RelPath)
		}
		seen[file.RelPath] = true
	}
	for _, paths := range [][]string{cp.NewFiles, cp.Skipped} {
		for _, path := range paths {
			rel, err := cleanRel(path)
			if err != nil || filepath.ToSlash(rel) != path {
				return Checkpoint{}, fmt.Errorf("invalid checkpoint path %q", path)
			}
			if seen[path] {
				return Checkpoint{}, fmt.Errorf("duplicate checkpoint path %q", path)
			}
			seen[path] = true
		}
	}
	return cp, nil
}

func verifyHistoryEntry(root *os.Root, entry Entry) error {
	if !isHexID(entry.ID) {
		return fmt.Errorf("invalid referenced object id %q", entry.ID)
	}
	if entry.Timestamp.IsZero() || entry.Size < 0 {
		return fmt.Errorf("invalid snapshot entry %q", entry.ID)
	}
	info, err := root.Lstat(filepath.Join(objectsRelDir(), entry.ID))
	if err != nil {
		return fmt.Errorf("referenced object %s is unavailable: %w", entry.ID, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("referenced object %s is not a regular file", entry.ID)
	}
	data, err := root.ReadFile(filepath.Join(objectsRelDir(), entry.ID))
	if err != nil {
		return fmt.Errorf("read referenced object %s: %w", entry.ID, err)
	}
	if int64(len(data)) != entry.Size {
		return fmt.Errorf("referenced object %s size does not match manifest", entry.ID)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != entry.ID {
		return fmt.Errorf("referenced object %s content hash does not match id", entry.ID)
	}
	return nil
}

func isJSONNull(data []byte) bool {
	return strings.TrimSpace(string(data)) == "null"
}
