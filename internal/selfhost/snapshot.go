package selfhost

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// perUserCacheEntry is the value stored in each perUserLRU element: the
// owning user name (so an eviction from the back of the list knows which
// map key to delete) plus the cached snapshot itself.
type perUserCacheEntry struct {
	user string
	snap perUserCachedSnapshot
}

// perUserCachedSnapshot holds a filtered snapshot payload for a specific
// non-admin user together with the invalidation signals that identify when
// it was computed. Only the gzip-compressed representation is retained —
// each entry is a full copy of every readable note, so keeping an
// uncompressed copy alongside it would roughly double resident memory for
// no benefit; the rare non-gzip request decompresses on demand instead.
type perUserCachedSnapshot struct {
	compressed  []byte
	etag        string
	adminETag   string    // vault ETag at cache time
	permsMtime  time.Time // permissions.json mtime at cache time
	groupsMtime time.Time // groups.json mtime at cache time
}

// defaultPerUserCacheBudget bounds the total size of every cached per-user
// snapshot combined (see Server.perUserCacheBudget). Each entry is a full
// gzip copy of every note a given user can read, so this is a real memory
// ceiling, not a per-entry count: worst case resident memory for the whole
// per-user cache is bounded by this many bytes regardless of how many
// distinct users request a snapshot.
const defaultPerUserCacheBudget = 64 << 20 // 64 MiB

type snapshotNote struct {
	Path       string    `json:"path"`
	Content    string    `json:"content"`
	ModifiedAt time.Time `json:"modified_at"`
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	// Admins (and the nil/unauthenticated-in-tests case) are served from
	// the single global snapshot cache, which already keeps plain alongside
	// its gzip — reuse it as-is rather than round-tripping through gzip.
	// Everyone else goes through the bounded, gzip-only per-user cache.
	var plain, compressed []byte
	var etag string
	var err error
	if user == nil || user.HasRole("admin") {
		plain, compressed, etag, err = s.snapshotPayload()
	} else {
		compressed, etag, err = s.snapshotPayloadFiltered(user)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	if r.Header.Get("If-None-Match") == `"`+etag+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed) //nolint:gosec // G705: JSON API response (Content-Type set above), not HTML — no XSS surface
		return
	}
	if plain == nil {
		// Non-admin path (or a client that skipped gzip): the per-user cache
		// only retains the compressed body, so decompress on demand for this
		// rare case instead of keeping a second, uncompressed copy resident
		// for every cached user.
		plain, err = gunzipAll(compressed)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	_, _ = w.Write(plain) //nolint:gosec // G705: JSON API response (Content-Type set above), not HTML — no XSS surface
}

// gunzipAll decompresses a complete gzip payload into memory. Used only for
// the rare non-gzip-accepting client hitting the per-user snapshot cache,
// which retains just the compressed representation.
func gunzipAll(compressed []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()
	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	return data, nil
}

type snapshotFile struct {
	path       string
	relative   string
	modifiedAt time.Time
}

// snapshotPayload avoids re-reading and re-compressing every note on each app
// refresh. The inexpensive metadata pass still detects files changed outside
// of the API, so the cache remains safe for a user-managed Markdown vault.
// When a vault watcher is running (see newVaultWatcher) and no filesystem
// event has arrived since the last call, it skips even that metadata pass
// entirely and serves the cached payload straight away.
func (s *Server) snapshotPayload() ([]byte, []byte, string, error) {
	if s.vaultWatcher != nil && !s.snapshotDirty.Load() {
		s.snapshotMu.Lock()
		plain, compressed, etag, ready := s.snapshotJSON, s.snapshotGZIP, s.snapshotETag, s.snapshotJSON != nil
		s.snapshotMu.Unlock()
		if ready {
			return plain, compressed, etag, nil
		}
	}
	if s.vaultWatcher != nil {
		// Clear dirty before walking, not after: an edit that lands mid-walk
		// then re-dirties the flag, so the next call redoes the walk instead
		// of silently trusting a snapshot that may already be stale.
		s.snapshotDirty.Store(false)
	}

	files := make([]snapshotFile, 0)
	hash := sha256.New()
	err := vault.Walk(s.cfg.VaultRoot, func(path string) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() > maxNoteBytes {
			return nil
		}
		rel, err := filepath.Rel(s.cfg.VaultRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		files = append(files, snapshotFile{path: path, relative: rel, modifiedAt: info.ModTime().UTC()})
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\n", rel, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return nil, nil, "", err
	}
	etag := hex.EncodeToString(hash.Sum(nil))

	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if etag == s.snapshotETag && s.snapshotJSON != nil {
		return s.snapshotJSON, s.snapshotGZIP, etag, nil
	}

	notes := make([]snapshotNote, 0, len(files))
	for _, file := range files {
		content, err := os.ReadFile(file.path)
		if err != nil {
			return nil, nil, "", err
		}
		notes = append(notes, snapshotNote{Path: file.relative, Content: string(content), ModifiedAt: file.modifiedAt})
	}
	plain, err := json.Marshal(map[string]any{"notes": notes, "generated_at": time.Now().UTC()})
	if err != nil {
		return nil, nil, "", err
	}
	plain = append(plain, '\n')
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(plain); err != nil {
		return nil, nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, "", err
	}
	s.snapshotETag = etag
	s.snapshotJSON = plain
	s.snapshotGZIP = compressed.Bytes()
	return s.snapshotJSON, s.snapshotGZIP, etag, nil
}

// snapshotPayloadFiltered is like snapshotPayload but excludes documents the
// user cannot read, returning only the gzip-compressed body (see
// perUserCachedSnapshot). Callers must handle the admin/nil-user case
// themselves — this is only the non-admin, per-user-cached path. Filtered
// payloads are cached per user and invalidated when the vault ETag or the
// permissions/groups file mtimes change; the cache is bounded by total
// bytes with least-recently-used eviction (see storeUserSnapshotLocked).
func (s *Server) snapshotPayloadFiltered(user *permissions.User) ([]byte, string, error) {
	plain, _, etag, err := s.snapshotPayload()
	if err != nil {
		return nil, "", err
	}

	// Compute the permissions generation: mtimes of the two files that
	// influence what documents a user can see.
	permDir := filepath.Join(s.cfg.VaultRoot, ".symdesk")
	permsMtime := fileMtime(filepath.Join(permDir, "permissions.json"))
	groupsMtime := fileMtime(filepath.Join(permDir, "groups.json"))

	// Check the per-user cache under lock.
	s.perUserCacheMu.Lock()
	if el, ok := s.perUserCache[user.Name]; ok {
		entry := el.Value.(*perUserCacheEntry)
		if entry.snap.adminETag == etag && entry.snap.permsMtime.Equal(permsMtime) && entry.snap.groupsMtime.Equal(groupsMtime) {
			s.perUserLRU.MoveToFront(el)
			comp, userETag := entry.snap.compressed, entry.snap.etag
			s.perUserCacheMu.Unlock()
			return comp, userETag, nil
		}
	}
	s.perUserCacheMu.Unlock()

	// Cache miss or stale — parse, filter, marshal and compress.
	var payload struct {
		Notes       []snapshotNote `json:"notes"`
		GeneratedAt time.Time      `json:"generated_at"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, "", err
	}
	// Use CanReadMany to batch-check all paths in one call instead of
	// hitting the permissions files individually for each note.
	paths := make([]string, len(payload.Notes))
	for i, n := range payload.Notes {
		paths[i] = n.Path
	}
	allowed := s.perm.CanReadMany(user, paths)
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	filtered := payload.Notes[:0]
	for _, n := range payload.Notes {
		if allowedSet[n.Path] {
			filtered = append(filtered, n)
		}
	}
	payload.Notes = filtered
	plain2, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	plain2 = append(plain2, '\n')
	var comp2 bytes.Buffer
	gz := gzip.NewWriter(&comp2)
	if _, err := gz.Write(plain2); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}
	// Use a different etag for filtered snapshots so the client's 304
	// cache doesn't leak data across users.
	newETag := etag + ":" + user.Name
	compressed := comp2.Bytes()

	s.perUserCacheMu.Lock()
	s.storeUserSnapshotLocked(user.Name, perUserCachedSnapshot{
		compressed: compressed, etag: newETag,
		adminETag: etag, permsMtime: permsMtime, groupsMtime: groupsMtime,
	})
	s.perUserCacheMu.Unlock()

	return compressed, newETag, nil
}

// storeUserSnapshotLocked inserts or replaces the cached snapshot for user
// as the most-recently-used entry, evicting entries from the back of
// perUserLRU (least-recently-used first) until the cache's total size fits
// within perUserCacheBudget. The caller must hold s.perUserCacheMu.
//
// Unlike the previous "flush everything past N entries" policy, eviction
// here is driven by actual memory footprint: each entry is a full gzip copy
// of every note a user can read, so a handful of large vaults can dwarf a
// count-based limit while a fleet of small ones would never approach it.
func (s *Server) storeUserSnapshotLocked(user string, snap perUserCachedSnapshot) {
	if s.perUserCache == nil {
		s.perUserCache = make(map[string]*list.Element)
		s.perUserLRU = list.New()
	}

	// Drop any existing entry for this user first so its old size is not
	// double-counted while we decide what (if anything) else to evict.
	if el, ok := s.perUserCache[user]; ok {
		s.perUserCacheBytes -= int64(len(el.Value.(*perUserCacheEntry).snap.compressed))
		s.perUserLRU.Remove(el)
		delete(s.perUserCache, user)
	}

	budget := s.perUserCacheBudget
	if budget <= 0 {
		budget = defaultPerUserCacheBudget
	}
	size := int64(len(snap.compressed))
	for s.perUserCacheBytes+size > budget && s.perUserLRU.Len() > 0 {
		back := s.perUserLRU.Back()
		evicted := back.Value.(*perUserCacheEntry)
		s.perUserCacheBytes -= int64(len(evicted.snap.compressed))
		s.perUserLRU.Remove(back)
		delete(s.perUserCache, evicted.user)
	}

	el := s.perUserLRU.PushFront(&perUserCacheEntry{user: user, snap: snap})
	s.perUserCache[user] = el
	s.perUserCacheBytes += size
}

// fileMtime returns the modification time of path, or the zero time when
// the file does not exist or cannot be stat'd.
func fileMtime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
