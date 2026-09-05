package selfhost

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
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
	// StateDSN, when set, routes the server's persistent state store
	// (config/session-cache) to PostgreSQL instead of the default SQLite
	// database under <VaultRoot>/.symdesk/server/state.db. Multi-user
	// production deployments should set this to a PostgreSQL connection
	// string; single-user installs leave it empty.
	StateDSN string
}

type Server struct {
	cfg           ServerConfig
	db            *sidecar.DB
	state         *ServerState
	jobs          *JobStore
	perm          *permissions.Manager
	retrievalPool *retrieval.ClientPool
	shares        *ShareStore
	throttle      *throttle
	vaultRoot     *os.Root
	http          *http.Server
	mux           *http.ServeMux
	snapshotMu    sync.Mutex
	snapshotETag  string
	snapshotJSON  []byte
	snapshotGZIP  []byte
	// snapshotDirty tracks whether the vault may have changed since the last
	// snapshotPayload computation. When a vault watcher is running, a clean
	// (false) state lets snapshotPayload skip its full stat-every-file walk
	// entirely and serve the cached payload straight away.
	snapshotDirty atomic.Bool
	vaultWatcher  *fsnotify.Watcher
	watcherDone   chan struct{}

	// Per-user filtered snapshot cache. Non-admin users re-use a previously
	// computed and filtered payload when neither the vault nor the permissions
	// files have changed since it was cached. perUserCache maps a user name to
	// its entry's element in perUserLRU, which orders entries from
	// most-recently-used (front) to least-recently-used (back) so eviction is
	// O(1). perUserCacheBytes tracks the combined size (in bytes) of every
	// cached entry's gzip payload; once a new or refreshed entry would push
	// the total over perUserCacheBudget, entries are evicted from the back of
	// the list until it fits.
	perUserCacheMu     sync.Mutex
	perUserCache       map[string]*list.Element
	perUserLRU         *list.List
	perUserCacheBytes  int64
	perUserCacheBudget int64
}

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
	// Canonicalize the vault root once so every subsystem — sidecar index,
	// snapshot walk, watcher and service-layer search (which resolves
	// symlinks internally) — agrees on one path form. Without this, a
	// symlinked root (e.g. /var on macOS) yields search results whose
	// relative paths do not resolve through /api/v1/files.
	if resolved, err := filepath.EvalSymlinks(cfg.VaultRoot); err == nil {
		cfg.VaultRoot = resolved
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
	retrievalPool := retrieval.NewClientPool()
	poolReady := false
	defer func() {
		if !poolReady {
			_ = retrievalPool.Close()
		}
	}()
	stateCtx, stateCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer stateCancel()
	state, err := OpenServerState(stateCtx, cfg.VaultRoot, cfg.StateDSN)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	jobs, err := NewJobStore(cfg.VaultRoot)
	if err != nil {
		_ = db.Close()
		_ = state.Close()
		return nil, err
	}
	vaultRoot, err := os.OpenRoot(cfg.VaultRoot)
	if err != nil {
		_ = db.Close()
		_ = state.Close()
		return nil, fmt.Errorf("open vault root: %w", err)
	}
	s := &Server{
		cfg: cfg, db: db, state: state, jobs: jobs, vaultRoot: vaultRoot, mux: http.NewServeMux(), throttle: newThrottle(),
		retrievalPool:      retrievalPool,
		perUserCacheBudget: defaultPerUserCacheBudget,
	}
	s.snapshotDirty.Store(true)

	// Initialise the permissions manager. The config directory lives under
	// .symdesk/ alongside the server state.
	permDir := filepath.Join(cfg.VaultRoot, ".symdesk")
	perm, err := permissions.NewManager(permDir)
	if err != nil {
		_ = vaultRoot.Close()
		_ = db.Close()
		return nil, fmt.Errorf("permissions: %w", err)
	}
	// Migration: when no users exist yet and a legacy token is set, create an
	// admin user whose token hash matches the existing token so existing
	// single-token deployments keep working without interruption. The state
	// store records that the migration ran once, so an administrator who
	// deliberately removes every user later does not get the well-known
	// legacy admin account resurrected on the next restart.
	if cfg.Token != "" {
		users, err := perm.UserList()
		if err != nil {
			_ = vaultRoot.Close()
			_ = db.Close()
			_ = state.Close()
			return nil, fmt.Errorf("permissions: list users: %w", err)
		}
		if len(users) == 0 {
			migrated, err := state.SetIfAbsent(stateCtx, StateKeyLegacyAdminMigrated,
				[]byte(time.Now().UTC().Format(time.RFC3339)))
			if err != nil {
				_ = vaultRoot.Close()
				_ = db.Close()
				_ = state.Close()
				return nil, fmt.Errorf("server state: record legacy admin migration: %w", err)
			}
			if !migrated {
				// Users were removed deliberately after the one-time
				// migration; do not recreate the legacy admin account.
				slog.Warn("legacy-token admin migration already ran; not recreating admin user")
			} else {
				if _, err := perm.UserAdd("admin", "admin", "user"); err != nil {
					_ = vaultRoot.Close()
					_ = db.Close()
					_ = state.Close()
					return nil, fmt.Errorf("permissions: create admin user: %w", err)
				}
				// Set the admin user's token hash to match the legacy token so
				// existing clients authenticate without any change.
				if err := perm.SetTokenHash("admin", permissions.HashToken(cfg.Token)); err != nil {
					_ = vaultRoot.Close()
					_ = db.Close()
					_ = state.Close()
					return nil, fmt.Errorf("permissions: set admin token: %w", err)
				}
			}
		}
	}
	s.perm = perm

	sharesDir := filepath.Join(cfg.VaultRoot, ".symdesk", "server")
	shares, err := NewShareStore(sharesDir)
	if err != nil {
		_ = vaultRoot.Close()
		_ = db.Close()
		_ = state.Close()
		return nil, fmt.Errorf("shares: %w", err)
	}
	s.shares = shares

	if err := s.refreshIndex(); err != nil {
		_ = vaultRoot.Close()
		_ = db.Close()
		_ = state.Close()
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
	poolReady = true
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
	return errors.Join(s.vaultRoot.Close(), s.db.Close(), s.state.Close(), s.retrievalPool.Close())
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
		if _, err := io.WriteString(w, `{"status":"ok"}`); err != nil {
			slog.Error("write health response", "error", err)
		}
	})
	s.mux.Handle("GET /api/v1/status", s.auth(http.HandlerFunc(s.handleStatus)))
	s.mux.Handle("GET /api/v1/snapshot", s.auth(http.HandlerFunc(s.handleSnapshot)))
	s.mux.Handle("GET /api/v1/files", s.auth(http.HandlerFunc(s.handleGetFile)))
	s.mux.Handle("PUT /api/v1/files", s.auth(http.HandlerFunc(s.handlePutFile)))
	s.mux.Handle("POST /api/v1/ingest", s.auth(http.HandlerFunc(s.handleIngest)))
	s.mux.Handle("GET /api/v1/jobs", s.auth(http.HandlerFunc(s.handleJobs)))
	s.mux.Handle("POST /api/v1/jobs/retry", s.auth(http.HandlerFunc(s.handleRetryJob)))
	s.mux.Handle("POST /api/v1/command", s.auth(http.HandlerFunc(s.handleCommand)))
	s.mux.Handle("POST /api/v1/ai/ask", s.auth(http.HandlerFunc(s.handleAIAsk)))
	s.mux.Handle("POST /api/v1/ai/transform", s.auth(http.HandlerFunc(s.handleAITransform)))
	s.mux.Handle("GET /api/v1/notebooks", s.auth(http.HandlerFunc(s.handleListNotebooks)))
	s.mux.Handle("GET /api/v1/notebooks/{id}", s.auth(http.HandlerFunc(s.handleGetNotebook)))
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

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	query := r.URL.Query()
	paged := query.Has("limit") || query.Has("offset")
	limit, offset, err := parseJobPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	jobs, total, err := s.jobs.ListPage(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !paged {
		writeJSON(w, http.StatusOK, jobs)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Jobs   []Job `json:"jobs"`
		Total  int   `json:"total"`
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
	}{Jobs: jobs, Total: total, Limit: limit, Offset: offset})
}

func parseJobPage(r *http.Request) (int, int, error) {
	limit, offset := 100, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
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

func isDatasetBackedPath(rel string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(rel))
	return normalized == "datasets" || strings.HasPrefix(normalized, "datasets/")
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
