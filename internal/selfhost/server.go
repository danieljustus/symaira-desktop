package selfhost

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

const (
	maxUploadBytes  = 100 << 20
	maxNoteBytes    = 8 << 20
	maxCommandBytes = 32 << 20
	maxOCRBytes     = 24 << 20
)

type ServerConfig struct {
	ListenAddress string
	VaultRoot     string
	// Token is the admin/client credential. It authenticates every route,
	// including the worker-scoped ones, for back-compat with single-token
	// deployments that predate WorkerToken.
	Token string
	// WorkerToken, when set, is a separate credential that only authenticates
	// the worker/lease/complete/fail routes. Deployments that have not
	// migrated yet leave this empty and keep sharing Token with their workers.
	WorkerToken string
	Version     string
	Executable  string
	TLSCert     string
	TLSKey      string
}

type Server struct {
	cfg          ServerConfig
	db           *sidecar.DB
	jobs         *JobStore
	perm         *permissions.Manager
	shares       *ShareStore
	throttle     *throttle
	vaultRoot    *os.Root
	http         *http.Server
	mux          *http.ServeMux
	snapshotMu   sync.Mutex
	snapshotETag string
	snapshotJSON []byte
	snapshotGZIP []byte
	// snapshotDirty tracks whether the vault may have changed since the last
	// snapshotPayload computation. When a vault watcher is running, a clean
	// (false) state lets snapshotPayload skip its full stat-every-file walk
	// entirely and serve the cached payload straight away.
	snapshotDirty atomic.Bool
	vaultWatcher  *fsnotify.Watcher
	watcherDone   chan struct{}

	// Per-user filtered snapshot cache. Non-admin users re-use a previously
	// computed and filtered payload when neither the vault nor the permissions
	// files have changed since it was cached. The map is keyed by user name.
	perUserCacheMu sync.Mutex
	perUserCache   map[string]perUserCachedSnapshot
}

// perUserCachedSnapshot holds a filtered-and-compressed snapshot payload for
// a specific non-admin user together with the invalidation signals that
// identify when it was computed.
type perUserCachedSnapshot struct {
	plain       []byte
	compressed  []byte
	etag        string
	adminETag   string    // vault ETag at cache time
	permsMtime  time.Time // permissions.json mtime at cache time
	groupsMtime time.Time // groups.json mtime at cache time
}

const maxPerUserCachedSnapshots = 64

func NewServer(cfg ServerConfig) (*Server, error) {
	if len(cfg.Token) < 32 {
		return nil, fmt.Errorf("server token must contain at least 32 characters")
	}
	if cfg.WorkerToken != "" {
		if len(cfg.WorkerToken) < 32 {
			return nil, fmt.Errorf("worker token must contain at least 32 characters")
		}
		if permissions.ConstantTimeEqual(cfg.WorkerToken, cfg.Token) {
			return nil, fmt.Errorf("worker token must differ from the server token")
		}
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1:8787"
	}
	if cfg.Executable == "" {
		var err error
		cfg.Executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate symdesk executable: %w", err)
		}
	}
	dbPath := filepath.Join(cfg.VaultRoot, ".symdesk", "server", "sidecar.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, err
	}
	db, err := sidecar.Open(dbPath)
	if err != nil {
		return nil, err
	}
	jobs, err := NewJobStore(cfg.VaultRoot)
	if err != nil {
		db.Close()
		return nil, err
	}
	vaultRoot, err := os.OpenRoot(cfg.VaultRoot)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open vault root: %w", err)
	}
	s := &Server{cfg: cfg, db: db, jobs: jobs, vaultRoot: vaultRoot, mux: http.NewServeMux(), throttle: newThrottle()}
	s.snapshotDirty.Store(true)

	// Initialise the permissions manager. The config directory lives under
	// .symdesk/ alongside the server state.
	permDir := filepath.Join(cfg.VaultRoot, ".symdesk")
	perm, err := permissions.NewManager(permDir)
	if err != nil {
		vaultRoot.Close()
		db.Close()
		return nil, fmt.Errorf("permissions: %w", err)
	}
	// Migration: when no users exist yet and a legacy token is set, create an
	// admin user whose token hash matches the existing token so existing
	// single-token deployments keep working without interruption.
	if cfg.Token != "" {
		users, err := perm.UserList()
		if err != nil {
			vaultRoot.Close()
			db.Close()
			return nil, fmt.Errorf("permissions: list users: %w", err)
		}
		if len(users) == 0 {
			if _, err := perm.UserAdd("admin", "admin", "user"); err != nil {
				vaultRoot.Close()
				db.Close()
				return nil, fmt.Errorf("permissions: create admin user: %w", err)
			}
			// Set the admin user's token hash to match the legacy token so
			// existing clients authenticate without any change.
			if err := perm.SetTokenHash("admin", permissions.HashToken(cfg.Token)); err != nil {
				vaultRoot.Close()
				db.Close()
				return nil, fmt.Errorf("permissions: set admin token: %w", err)
			}
		}
	}
	s.perm = perm

	sharesDir := filepath.Join(cfg.VaultRoot, ".symdesk", "server")
	shares, err := NewShareStore(sharesDir)
	if err != nil {
		vaultRoot.Close()
		db.Close()
		return nil, fmt.Errorf("shares: %w", err)
	}
	s.shares = shares

	if err := s.refreshIndex(); err != nil {
		vaultRoot.Close()
		db.Close()
		return nil, fmt.Errorf("index vault: %w", err)
	}
	s.routes()
	s.http = &http.Server{
		Addr: cfg.ListenAddress, Handler: s.securityHeaders(s.mux),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute,
		WriteTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	if watcher, err := newVaultWatcher(cfg.VaultRoot); err != nil {
		// A watcher is an optimization, not a correctness requirement:
		// snapshotPayload always falls back to a full walk when
		// vaultWatcher is nil, which matches prior behavior exactly.
		slog.Warn("vault change watcher unavailable; every snapshot request will rescan the vault", "error", err)
	} else {
		s.vaultWatcher = watcher
		s.watcherDone = make(chan struct{})
		go s.watchVault()
	}
	return s, nil
}

// newVaultWatcher adds a recursive fsnotify watch over every directory in
// root, excluding the server's own .symdesk state so internal index/queue
// writes never falsely mark a snapshot dirty.
func newVaultWatcher(root string) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr == nil && (rel == ".symdesk" || strings.HasPrefix(rel, ".symdesk"+string(filepath.Separator))) {
			return filepath.SkipDir
		}
		return watcher.Add(path)
	})
	if walkErr != nil {
		_ = watcher.Close()
		return nil, walkErr
	}
	return watcher, nil
}

// watchVault marks the snapshot dirty on any vault change and extends the
// watch to newly created directories so nested content stays covered.
func (s *Server) watchVault() {
	defer close(s.watcherDone)
	for {
		select {
		case event, ok := <-s.vaultWatcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				s.snapshotDirty.Store(true)
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = s.vaultWatcher.Add(event.Name)
				}
			}
		case _, ok := <-s.vaultWatcher.Errors:
			if !ok {
				return
			}
			// A watcher error does not invalidate what we already know; the
			// next real change event (or lack thereof) still drives
			// snapshotDirty correctly. Errors are otherwise unrecoverable
			// per-event, so there is nothing more to do here.
		}
	}
}

func (s *Server) Close() error {
	if s.vaultWatcher != nil {
		_ = s.vaultWatcher.Close()
		<-s.watcherDone
	}
	return errors.Join(s.vaultRoot.Close(), s.db.Close())
}

func (s *Server) ListenAndServe() error {
	slog.Info("SymDesk server listening", "address", s.cfg.ListenAddress, "vault", s.cfg.VaultRoot)
	if (s.cfg.TLSCert == "") != (s.cfg.TLSKey == "") {
		return fmt.Errorf("both TLS certificate and key are required")
	}
	if s.cfg.TLSCert != "" {
		return s.http.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
	}
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) Handler() http.Handler              { return s.http.Handler }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})
	s.mux.Handle("GET /api/v1/status", s.auth(http.HandlerFunc(s.handleStatus)))
	s.mux.Handle("GET /api/v1/snapshot", s.auth(http.HandlerFunc(s.handleSnapshot)))
	s.mux.Handle("GET /api/v1/files", s.auth(http.HandlerFunc(s.handleGetFile)))
	s.mux.Handle("PUT /api/v1/files", s.auth(http.HandlerFunc(s.handlePutFile)))
	s.mux.Handle("POST /api/v1/ingest", s.auth(http.HandlerFunc(s.handleIngest)))
	s.mux.Handle("GET /api/v1/jobs", s.auth(http.HandlerFunc(s.handleJobs)))
	s.mux.Handle("POST /api/v1/jobs/retry", s.auth(http.HandlerFunc(s.handleRetryJob)))
	s.mux.Handle("POST /api/v1/command", s.auth(http.HandlerFunc(s.handleCommand)))
	s.mux.Handle("POST /api/v1/worker/lease", s.auth(http.HandlerFunc(s.handleLease)))
	s.mux.Handle("GET /api/v1/worker/input", s.auth(http.HandlerFunc(s.handleWorkerInput)))
	s.mux.Handle("POST /api/v1/worker/complete", s.auth(http.HandlerFunc(s.handleComplete)))
	s.mux.Handle("POST /api/v1/worker/fail", s.auth(http.HandlerFunc(s.handleFail)))

	// Share links — read-only time-limited access to individual documents.
	s.mux.Handle("POST /api/v1/share", s.auth(http.HandlerFunc(s.handleCreateShare)))
	s.mux.Handle("GET /api/v1/shares", s.auth(http.HandlerFunc(s.handleListShares)))
	s.mux.Handle("DELETE /api/v1/share/{id}", s.auth(http.HandlerFunc(s.handleRevokeShare)))
	s.mux.HandleFunc("GET /s/{token}", s.handleAccessShare)
}

// contextKey is the private type used for request-context values.
type contextKey int

const ctxKeyUser contextKey = iota

// auth authenticates the request via the permissions manager, attaches the
// resolved user to the request context, and delegates to next. The legacy
// single-token model is covered by the migration path: the admin token
// created during NewServer matches ServerConfig.Token.
//
// Back-compat: the old worker-token / admin-token distinction still works
// because any user with the "worker" role (and the admin user) can access
// worker routes. The worker must be registered as a named user.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

		// Try the permissions manager first.
		user, err := s.perm.Authenticate(provided)
		if err != nil {
			// Fall back to legacy constant-time comparison for the admin and
			// worker tokens. This handles deployments where the users file hasn't
			// been persisted yet (fresh container restarts with env-var tokens).
			legacyRole := s.legacyTokenRole(provided)
			if legacyRole == "" {
				// Authentication failed — check rate limit before responding.
				ip := clientIP(r)
				if allowed, retryAfter := s.throttle.recordAuthFailure(ip); !allowed {
					w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeError(w, http.StatusTooManyRequests, "too many authentication attempts")
					return
				}
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			// Create a synthetic user reflecting the legacy token's scope.
			if legacyRole == "admin" {
				user = &permissions.User{Name: "admin", Roles: []string{"admin", "user"}}
			} else {
				user = &permissions.User{Name: "worker", Roles: []string{"worker"}}
			}
		}

		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// legacyTokenRole checks the provided token against the server's configured
// admin and worker tokens using constant-time comparison, and returns the
// legacy role name (\"admin\" or \"worker\") or empty string.
func (s *Server) legacyTokenRole(provided string) string {
	if permissions.ConstantTimeEqual(provided, s.cfg.Token) {
		return "admin"
	}
	if s.cfg.WorkerToken != "" && permissions.ConstantTimeEqual(provided, s.cfg.WorkerToken) {
		return "worker"
	}
	return ""
}

// userFromContext extracts the authenticated user from the request context.
// It returns nil when the request is unauthenticated (should not happen behind
// the auth middleware).
func userFromContext(r *http.Request) *permissions.User {
	user, _ := r.Context().Value(ctxKeyUser).(*permissions.User)
	return user
}

// requireAdmin writes a 403 response when the request is not from an admin
// user. Callers must not write a response when this returns false.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user := userFromContext(r)
	if user == nil || !user.HasRole("admin") {
		writeError(w, http.StatusForbidden, "admin role required")
		return false
	}
	return true
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": s.cfg.Version, "schema_version": 1,
		"mode": "self_hosted", "capabilities": []string{"snapshot", "files", "ingest", "remote_worker", "command"},
	})
}

type snapshotNote struct {
	Path       string    `json:"path"`
	Content    string    `json:"content"`
	ModifiedAt time.Time `json:"modified_at"`
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	plain, compressed, etag, err := s.snapshotPayloadFiltered(user)
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
		_, _ = w.Write(compressed)
		return
	}
	_, _ = w.Write(plain)
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
// user cannot read. Admins see everything (the existing behaviour). Non-admin
// filtered payloads are cached per user and invalidated when the vault ETag
// or the permissions/groups file mtimes change.
func (s *Server) snapshotPayloadFiltered(user *permissions.User) ([]byte, []byte, string, error) {
	plain, compressed, etag, err := s.snapshotPayload()
	if err != nil {
		return nil, nil, "", err
	}
	// Admins and nil users get the full snapshot as before.
	if user == nil || user.HasRole("admin") {
		return plain, compressed, etag, nil
	}

	// Compute the permissions generation: mtimes of the two files that
	// influence what documents a user can see.
	permDir := filepath.Join(s.cfg.VaultRoot, ".symdesk")
	permsMtime := fileMtime(filepath.Join(permDir, "permissions.json"))
	groupsMtime := fileMtime(filepath.Join(permDir, "groups.json"))

	// Check the per-user cache under lock.
	s.perUserCacheMu.Lock()
	if cached, ok := s.perUserCache[user.Name]; ok {
		if cached.adminETag == etag && cached.permsMtime.Equal(permsMtime) && cached.groupsMtime.Equal(groupsMtime) {
			plain2, comp2, userETag := cached.plain, cached.compressed, cached.etag
			s.perUserCacheMu.Unlock()
			return plain2, comp2, userETag, nil
		}
	}
	s.perUserCacheMu.Unlock()

	// Cache miss or stale — parse, filter, marshal and compress.
	var payload struct {
		Notes       []snapshotNote `json:"notes"`
		GeneratedAt time.Time      `json:"generated_at"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, nil, "", err
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
		return nil, nil, "", err
	}
	plain2 = append(plain2, '\n')
	var comp2 bytes.Buffer
	gz := gzip.NewWriter(&comp2)
	if _, err := gz.Write(plain2); err != nil {
		return nil, nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, "", err
	}
	// Use a different etag for filtered snapshots so the client's 304
	// cache doesn't leak data across users.
	newETag := etag + ":" + user.Name

	// Store in per-user cache (bounded).
	s.perUserCacheMu.Lock()
	if s.perUserCache == nil {
		s.perUserCache = make(map[string]perUserCachedSnapshot)
	}
	if len(s.perUserCache) >= maxPerUserCachedSnapshots {
		// Bounds exceeded — clear and rebuild. Individual entries are
		// small; a full flush is simpler than maintaining an LRU list.
		s.perUserCache = make(map[string]perUserCachedSnapshot)
	}
	s.perUserCache[user.Name] = perUserCachedSnapshot{
		plain: plain2, compressed: comp2.Bytes(), etag: newETag,
		adminETag: etag, permsMtime: permsMtime, groupsMtime: groupsMtime,
	}
	s.perUserCacheMu.Unlock()

	return plain2, comp2.Bytes(), newETag, nil
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

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	rel, err := resolveRequestPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user := userFromContext(r)
	if !s.perm.CanRead(user, rel) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	s.serveVaultFile(w, r, rel, "inline", filepath.Base(rel))
}

func (s *Server) serveVaultFile(w http.ResponseWriter, r *http.Request, rel, disposition, filename string) {
	file, err := s.vaultRoot.Open(rel)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, safeFilename(filename)))
	http.ServeContent(w, r, filepath.Base(rel), info.ModTime(), file)
}

func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	rel, err := resolveRequestPath(r.URL.Query().Get("path"))
	if err != nil || strings.ToLower(filepath.Ext(rel)) != ".md" {
		writeError(w, http.StatusBadRequest, "only vault-relative Markdown files can be updated")
		return
	}
	user := userFromContext(r)
	if !s.perm.CanWrite(user, rel) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	data, err := readLimited(r.Body, maxNoteBytes)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err := s.vaultRoot.MkdirAll(filepath.Dir(rel), 0750); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := atomicWrite(s.vaultRoot, rel, data, 0644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	doc, err := vault.ParseBytes(filepath.Join(s.cfg.VaultRoot, rel), data)
	if err == nil {
		err = s.db.IndexDocument(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "upload exceeds 100 MiB or is invalid")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "multipart field 'file' is required")
		return
	}
	defer func() { _ = file.Close() }()

	id, err := NewJobID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := safeFilename(header.Filename)
	rel, err := resolveRequestPath(filepath.ToSlash(filepath.Join("archive", time.Now().UTC().Format("2006/01"), id+"-"+name)))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.vaultRoot.MkdirAll(filepath.Dir(rel), 0750); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := writeUpload(s.vaultRoot, rel, file); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job, err := s.jobs.Create(rel, name, header.Header.Get("Content-Type"))
	if err != nil {
		s.vaultRoot.Remove(rel)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	jobs, err := s.jobs.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	job, err := s.jobs.Retry(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type commandRequest struct {
	Arguments []string `json:"arguments"`
	Stdin     string   `json:"stdin,omitempty"`
}

// streamingCommands emit newline-delimited JSON incrementally as an LLM
// streams tokens (see cmd/symdesk's outputStream), rather than one JSON
// document at completion. Their HTTP response is streamed line-by-line
// instead of buffered, so a client sees partial output as it is produced.
var streamingCommands = map[string]bool{"ask": true, "transform": true}

// subprocessEnvExclude lists environment variables that authenticate the
// server process itself and must never reach a remotely spawned symdesk
// subprocess. The remote command allowlist (validateRemoteCommand) limits
// which commands can run, but does not stop a future command from echoing
// environment state back to an authenticated client, so these are stripped
// unconditionally regardless of which command is invoked.
var subprocessEnvExclude = map[string]bool{
	"SYMDESK_SERVER_TOKEN": true,
	"SYMDESK_WORKER_TOKEN": true,
}

// subprocessEnv builds the environment for a remotely spawned symdesk
// subprocess: a filtered copy of the server's own environment with the
// server/worker auth tokens removed, plus SYMDESK_SIDECAR pointing at this
// server's index database. Filtering (rather than allow-listing from
// scratch) keeps every other variable the allowed commands need — PATH,
// HOME, and the SYMDESK_LLM_*/SYMDESK_OLLAMA_*/SYMDESK_ANTHROPIC_URL
// variables that `ask`/`transform` read via internal/ai and internal/config
// — working exactly as before, while ensuring the credentials that
// authenticate this server can never leak into a subprocess whose arguments
// an already-authenticated remote client controls.
func (s *Server) subprocessEnv() []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, found := strings.Cut(kv, "=")
		if found && subprocessEnvExclude[name] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "SYMDESK_SIDECAR="+filepath.Join(s.cfg.VaultRoot, ".symdesk", "server", "sidecar.db"))
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var request commandRequest
	if err := decodeJSON(r, &request, 2<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRemoteCommand(request.Arguments); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	args := append([]string{}, request.Arguments...)
	if !containsArg(args, "--json") {
		args = append(args, "--json")
	}
	args = append(args, "--vault", s.cfg.VaultRoot)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.Executable, args...)
	cmd.Env = s.subprocessEnv()
	cmd.Stdin = strings.NewReader(request.Stdin)

	if streamingCommands[args[0]] {
		s.streamCommand(w, cmd)
		return
	}

	out := &limitedBuffer{limit: maxCommandBytes}
	errOut := &limitedBuffer{limit: 1 << 20}
	cmd.Stdout = out
	cmd.Stderr = errOut
	err := cmd.Run()
	if errors.Is(out.err, errLimitExceeded) {
		writeError(w, http.StatusRequestEntityTooLarge, "command output exceeded 32 MiB")
		return
	}
	if err != nil {
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = err.Error()
		}
		writeError(w, http.StatusUnprocessableEntity, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

// streamCommand runs cmd and relays its stdout to w one NDJSON line at a
// time, flushing after every line so the client observes output as the
// subprocess produces it. Once the 200 status and first bytes are written,
// errors can no longer change the HTTP status; a failure is instead reported
// as a trailing NDJSON error line so the client can distinguish it from a
// normal event. Cancellation (client disconnect or the 5-minute timeout)
// flows through cmd's context and kills the subprocess.
func (s *Server) streamCommand(w http.ResponseWriter, cmd *exec.Cmd) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	errOut := &limitedBuffer{limit: 1 << 20}
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	reader := bufio.NewReader(stdout)
	var written int64
	truncated := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !truncated {
				if written+int64(len(line)) > maxCommandBytes {
					truncated = true
					_ = cmd.Process.Kill()
				} else {
					written += int64(len(line))
					if _, writeErr := w.Write(line); writeErr != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return
					}
					if canFlush {
						flusher.Flush()
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	runErr := cmd.Wait()
	var event map[string]string
	switch {
	case truncated:
		event = map[string]string{"type": "error", "message": "command output exceeded 32 MiB"}
	case runErr != nil:
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = runErr.Error()
		}
		event = map[string]string{"type": "error", "message": message}
	default:
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = w.Write(append(line, '\n'))
	if canFlush {
		flusher.Flush()
	}
}

type leaseRequest struct {
	WorkerID     string   `json:"worker_id"`
	Capabilities []string `json:"capabilities"`
}

func (s *Server) handleLease(w http.ResponseWriter, r *http.Request) {
	var request leaseRequest
	if err := decodeJSON(r, &request, 64<<10); err != nil || strings.TrimSpace(request.WorkerID) == "" {
		writeError(w, http.StatusBadRequest, "worker_id and capabilities are required")
		return
	}
	job, err := s.jobs.Lease(request.WorkerID, request.Capabilities, 15*time.Minute)
	if errors.Is(err, ErrNoPendingJob) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleWorkerInput(w http.ResponseWriter, r *http.Request) {
	job, err := s.jobs.Get(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	rel, err := resolveRequestPath(job.SourcePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serveVaultFile(w, r, rel, "attachment", job.OriginalName)
}

type completionRequest struct {
	JobID    string `json:"job_id"`
	WorkerID string `json:"worker_id"`
	Text     string `json:"text"`
	Engine   string `json:"engine"`
	Model    string `json:"model,omitempty"`
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var request completionRequest
	if err := decodeJSON(r, &request, maxOCRBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.jobs.Get(request.JobID)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status != "processing" || job.WorkerID != request.WorkerID {
		writeError(w, http.StatusConflict, "job is not leased by this worker")
		return
	}
	notePath, err := s.writeCompletedNote(job, request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job, err = s.jobs.Complete(job.ID, request.WorkerID, request.Engine, request.Model, notePath)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type failRequest struct {
	JobID    string `json:"job_id"`
	WorkerID string `json:"worker_id"`
	Error    string `json:"error"`
	Retry    bool   `json:"retry"`
}

func (s *Server) handleFail(w http.ResponseWriter, r *http.Request) {
	var request failRequest
	if err := decodeJSON(r, &request, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.jobs.Fail(request.JobID, request.WorkerID, request.Error, request.Retry)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) writeCompletedNote(job *Job, result completionRequest) (string, error) {
	base := strings.TrimSuffix(job.OriginalName, filepath.Ext(job.OriginalName))
	base = strings.Trim(strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, base), " .")
	if base == "" {
		base = "Document"
	}
	rel, err := resolveRequestPath(filepath.ToSlash(filepath.Join("inbox", base+"-"+job.ID[:8]+".md")))
	if err != nil {
		return "", err
	}
	if err := s.vaultRoot.MkdirAll(filepath.Dir(rel), 0750); err != nil {
		return "", err
	}
	fm := map[string]any{
		"title": base, "created": time.Now().UTC().Format(time.RFC3339),
		"status": "needs_review", "confidence": 0, "archive_path": job.SourcePath,
		"ocr_engine": result.Engine, "ocr_model": result.Model,
	}
	frontmatter, err := yaml.Marshal(fm)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("---\n%s---\n\n![[%s]]\n\n## OCR text\n\n%s\n", frontmatter, filepath.ToSlash(job.SourcePath), strings.TrimSpace(result.Text))
	content := []byte(body)
	if err := atomicWrite(s.vaultRoot, rel, content, 0644); err != nil {
		return "", err
	}
	doc, err := vault.ParseBytes(filepath.Join(s.cfg.VaultRoot, rel), content)
	if err != nil {
		return "", err
	}
	if err := s.db.IndexDocument(doc); err != nil {
		return "", err
	}
	return rel, nil
}

func resolveRequestPath(rel string) (string, error) {
	if rel == "" || strings.ContainsAny(rel, "\\\x00") || !fs.ValidPath(rel) {
		return "", fmt.Errorf("a vault-relative path is required")
	}
	localized, err := filepath.Localize(rel)
	if err != nil || !filepath.IsLocal(localized) {
		return "", fmt.Errorf("a vault-relative path is required")
	}
	internal := ".symdesk"
	if localized == internal || strings.HasPrefix(localized, internal+string(filepath.Separator)) {
		return "", fmt.Errorf("internal server files are not available through the document API")
	}
	return localized, nil
}

// refreshIndex brings the sidecar index up to date with the vault on disk.
// See sidecar.DB.RefreshIndex for the stat-based fast path that lets a warm
// start skip re-reading and re-hashing files unchanged since the last index.
func (s *Server) refreshIndex() error {
	return s.db.RefreshIndex(s.cfg.VaultRoot)
}

var allowedRemoteCommands = map[string]map[string]bool{
	"doctor": {"": true}, "ls": {"": true}, "search": {"": true}, "backlinks": {"": true},
	"graph": {"": true}, "similar": {"": true}, "transform": {"": true}, "ask": {"": true},
	"note":  {"new": true, "move": true, "delete": true, "daily": true},
	"paperless": {"import": true},
	"props": {"get": true, "edit": true}, "relations": {"inverse": true},
	"views":    {"list": true, "get": true, "save": true, "delete": true, "new-entry": true, "siblings": true, "exec": true},
	"docs":     {"list": true, "review": true},
	"doc":      {"status": true, "due": true, "type": true, "correspondent": true, "tag": true, "asn": true},
	"conflict": {"resolve": true}, "history": {"": true, "prune": true}, "restore": {"": true},
	"trash": {"list": true, "restore": true, "delete": true},
}

func validateRemoteCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("command is required")
	}
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--vault" || strings.HasPrefix(lower, "--vault=") || lower == "--output" || strings.HasPrefix(lower, "--output=") {
			return fmt.Errorf("server-controlled path flags are not allowed")
		}
	}
	subcommands, ok := allowedRemoteCommands[args[0]]
	if !ok {
		return fmt.Errorf("command %q is not available remotely", args[0])
	}
	if subcommands[""] {
		return nil
	}
	sub := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		sub = args[1]
	}
	if !subcommands[sub] {
		return fmt.Errorf("subcommand %q is not available remotely", sub)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(r *http.Request, destination any, limit int64) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, limit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "." || name == "" {
		return "document.bin"
	}
	return name
}

func writeUpload(root *os.Root, path string, src multipart.File) error {
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(path), ".upload-")
	if err != nil {
		return err
	}
	defer root.Remove(tmpName)
	if err := tmp.Chmod(0640); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, io.LimitReader(src, maxUploadBytes+1)); err != nil {
		tmp.Close()
		return err
	}
	if info, err := tmp.Stat(); err != nil || info.Size() > maxUploadBytes {
		tmp.Close()
		return fmt.Errorf("upload exceeds 100 MiB")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, path)
}

func atomicWrite(root *os.Root, path string, data []byte, mode os.FileMode) error {
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer root.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, path)
}

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

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body exceeds limit")
	}
	return data, nil
}

func containsArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

var errLimitExceeded = errors.New("buffer limit exceeded")

type limitedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.err = errLimitExceeded
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.err = errLimitExceeded
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
