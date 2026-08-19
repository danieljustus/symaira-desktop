package selfhost

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
)

// ShareLink represents a time-limited read-only share link for a single
// vault document. The plain-text token is never persisted — only its
// SHA-256 hash is stored and the caller is responsible for handing the
// token to the intended recipient.
type ShareLink struct {
	ID        string     `json:"id"`
	Path      string     `json:"path"`       // vault-relative document path
	CreatedBy string     `json:"created_by"` // creator identifier
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	TokenHash string     `json:"token_hash"` // SHA-256 of the access token
	Expired   bool       `json:"expired,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// Token is the plain-text secret returned on creation and never persisted.
	Token string `json:"token,omitempty"`
}

// IsActive reports whether the share link is currently valid: not revoked
// and not expired.
func (l *ShareLink) IsActive() bool {
	return !l.Expired && time.Now().UTC().Before(l.ExpiresAt)
}

// ShareStore persists share links as a JSON file under the vault's .symdesk
// server state directory. It is safe for concurrent use.
type ShareStore struct {
	path string
	mu   sync.RWMutex
}

// NewShareStore creates or opens the share link store at the given
// config directory.
func NewShareStore(configDir string) (*ShareStore, error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, fmt.Errorf("shares: create config dir: %w", err)
	}
	return &ShareStore{path: filepath.Join(configDir, "shares.json")}, nil
}

// loadLocked reads and parses shares.json. The caller must already hold
// s.mu (either the read lock or the write lock).
func (s *ShareStore) loadLocked() ([]ShareLink, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("shares: read store: %w", err)
	}
	var links []ShareLink
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("shares: parse store: %w", err)
	}
	return links, nil
}

// saveLocked marshals links and writes them to shares.json atomically (via
// a temp file in the same directory followed by a rename), so a crash or
// power loss mid-write cannot leave a truncated store. The caller must
// already hold s.mu.Lock().
func (s *ShareStore) saveLocked(links []ShareLink) error {
	data, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return fmt.Errorf("shares: marshal store: %w", err)
	}
	return atomicWriteFile(s.path, data, 0600)
}

// load returns a point-in-time snapshot of the stored links under a read
// lock. Callers that need to read-modify-write must not use this — take
// s.mu.Lock() and call loadLocked/saveLocked directly instead, so the
// whole transaction is covered by a single lock (see Create and Revoke).
func (s *ShareStore) load() ([]ShareLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadLocked()
}

// atomicWriteFile writes data to path by first writing to a temporary file
// in the same directory and then renaming it into place. This ensures a
// reader never observes a partially written file and a crash mid-write
// cannot truncate the previous contents.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".shares-*.tmp")
	if err != nil {
		return fmt.Errorf("shares: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("shares: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("shares: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("shares: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("shares: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("shares: rename temp file: %w", err)
	}
	return nil
}

// Create produces a new share link for the document at the given vault-
// relative path. The plain-text token is returned so the caller can hand
// it to the recipient; only its hash is stored.
func (s *ShareStore) Create(path, createdBy string, duration time.Duration) (*ShareLink, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	links, err := s.loadLocked()
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	id, err := newRandomHex(12)
	if err != nil {
		return nil, "", err
	}
	token, err := newRandomHex(32)
	if err != nil {
		return nil, "", err
	}
	tokenHash := hashToken(token)

	link := ShareLink{
		ID:        id,
		Path:      path,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(duration),
		TokenHash: tokenHash,
	}
	link.Token = token

	links = append(links, link)
	if err := s.saveLocked(links); err != nil {
		return nil, "", err
	}
	return &link, token, nil
}

// Lookup finds a share link by its plain-text access token and returns it
// if the token matches and the link is still active. An error is returned
// for missing, expired, or revoked links.
func (s *ShareStore) Lookup(token string) (*ShareLink, error) {
	if token == "" {
		return nil, fmt.Errorf("shares: token required")
	}
	target := hashToken(token)
	links, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := range links {
		if !permissions.ConstantTimeEqual(links[i].TokenHash, target) {
			continue
		}
		if !links[i].IsActive() {
			return nil, fmt.Errorf("shares: link expired")
		}
		return &links[i], nil
	}
	return nil, fmt.Errorf("shares: link not found")
}

// List returns every share link (active, expired, and revoked) ordered
// newest-first. Token hashes are excluded from the response.
func (s *ShareStore) List() ([]ShareLink, error) {
	links, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]ShareLink, len(links))
	copy(out, links)
	for i := range out {
		out[i].TokenHash = ""
		out[i].Token = ""
	}
	// Reverse → newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Revoke marks a share link as expired and records the revocation time.
// It is a no-op for already-revoked links.
func (s *ShareStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	links, err := s.loadLocked()
	if err != nil {
		return err
	}
	found := false
	now := time.Now().UTC()
	for i := range links {
		if links[i].ID != id {
			continue
		}
		if links[i].Expired {
			return fmt.Errorf("shares: link already revoked")
		}
		links[i].Expired = true
		links[i].RevokedAt = &now
		found = true
		break
	}
	if !found {
		return fmt.Errorf("shares: link not found")
	}
	return s.saveLocked(links)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newRandomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// handleCreateShare handles POST /api/v1/share — create a time-limited
// read-only share link for a vault document. Only "user" and "admin" role
// principals may mint a link (the worker-scoped credential is rejected
// outright), and the caller must additionally hold read access to the
// requested document under the permissions manager, exactly like every
// other read route (see handleGetFile).
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	if user == nil || (!user.HasRole("user") && !user.HasRole("admin")) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	var req struct {
		Path   string `json:"path"`
		Expiry int    `json:"expiry"` // hours, max 168 (7 days)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	rel, err := resolveRequestPath(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Expiry <= 0 || req.Expiry > 168 {
		writeError(w, http.StatusBadRequest, "expiry must be between 1 and 168 hours")
		return
	}

	if !s.perm.CanRead(user, rel) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	// Verify the document exists in the vault, through the same sandboxed
	// os.Root the rest of the API uses (symlink containment included).
	file, err := s.vaultRoot.Open(rel)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	info, statErr := file.Stat()
	_ = file.Close()
	if statErr != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	link, token, err := s.shares.Create(rel, user.Name, time.Duration(req.Expiry)*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}
	resp := struct {
		ID        string    `json:"id"`
		Token     string    `json:"token"`
		Path      string    `json:"path"`
		CreatedAt time.Time `json:"created_at"`
		ExpiresAt time.Time `json:"expires_at"`
		URL       string    `json:"url"`
	}{
		ID:        link.ID,
		Token:     token,
		Path:      rel,
		CreatedAt: link.CreatedAt,
		ExpiresAt: link.ExpiresAt,
		URL:       "/s/" + token,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleListShares handles GET /api/v1/shares — list share links. Admins
// see every link; everyone else sees only the links they created, so a
// worker or restricted-user credential cannot enumerate other users'
// share links.
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	links, err := s.shares.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	user := userFromContext(r)
	if user == nil {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	if !user.HasRole("admin") {
		owned := links[:0]
		for _, l := range links {
			if l.CreatedBy == user.Name {
				owned = append(owned, l)
			}
		}
		links = owned
	}
	if links == nil {
		links = []ShareLink{}
	}
	writeJSON(w, http.StatusOK, links)
}

// handleRevokeShare handles DELETE /api/v1/share/{id} — revoke a share
// link. Admins may revoke any link; everyone else may only revoke a link
// they created themselves.
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	user := userFromContext(r)
	if user == nil {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	if !user.HasRole("admin") {
		links, err := s.shares.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to revoke share")
			return
		}
		owned := false
		for _, l := range links {
			if l.ID == id {
				owned = l.CreatedBy == user.Name
				break
			}
		}
		if !owned {
			// Same response as "not found" — do not leak whether a link
			// owned by someone else exists.
			writeError(w, http.StatusNotFound, "share link not found or already revoked")
			return
		}
	}
	if err := s.shares.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, "share link not found or already revoked")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleAccessShare handles GET /s/{token} — serve a shared document
// without authentication. The document is served through s.vaultRoot, the
// same sandboxed os.Root every other file route uses, so a symlink inside
// the vault that points outside it is never followed on this
// authentication-free route either.
func (s *Server) handleAccessShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	link, err := s.shares.Lookup(token)
	if err != nil {
		// Share-token lookup failed — check rate limit before responding.
		ip := clientIP(r)
		if allowed, retryAfter := s.throttle.recordShareFailure(ip); !allowed {
			w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
			writeError(w, http.StatusTooManyRequests, "too many share access attempts")
			return
		}
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.serveVaultFile(w, r, link.Path, "inline", filepath.Base(link.Path))
}
