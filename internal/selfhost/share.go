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
	"strings"
	"sync"
	"time"
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

func (s *ShareStore) load() ([]ShareLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
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

func (s *ShareStore) save(links []ShareLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return fmt.Errorf("shares: marshal store: %w", err)
	}
	return os.WriteFile(s.path, data, 0600)
}

// Create produces a new share link for the document at the given vault-
// relative path. The plain-text token is returned so the caller can hand
// it to the recipient; only its hash is stored.
func (s *ShareStore) Create(path, createdBy string, duration time.Duration) (*ShareLink, string, error) {
	links, err := s.load()
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
	if err := s.save(links); err != nil {
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
		if links[i].TokenHash != target {
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
	links, err := s.load()
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
	return s.save(links)
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
// read-only share link for a vault document.
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
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
	if strings.Contains(req.Path, "..") || filepath.IsAbs(req.Path) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	if req.Expiry <= 0 || req.Expiry > 168 {
		writeError(w, http.StatusBadRequest, "expiry must be between 1 and 168 hours")
		return
	}

	// Verify the document exists in the vault.
	fullPath := filepath.Join(s.cfg.VaultRoot, req.Path)
	_, err := os.Stat(fullPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	user := userFromContext(r)
	link, token, err := s.shares.Create(req.Path, user.Name, time.Duration(req.Expiry)*time.Hour)
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
		Path:      req.Path,
		CreatedAt: link.CreatedAt,
		ExpiresAt: link.ExpiresAt,
		URL:       "/s/" + token,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleListShares handles GET /api/v1/shares — list all share links.
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	links, err := s.shares.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	if links == nil {
		links = []ShareLink{}
	}
	writeJSON(w, http.StatusOK, links)
}

// handleRevokeShare handles DELETE /api/v1/share/{id} — revoke a share link.
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.shares.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, "share link not found or already revoked")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleAccessShare handles GET /s/{token} — serve a shared document
// without authentication.
func (s *Server) handleAccessShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	link, err := s.shares.Lookup(token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	fullPath := filepath.Join(s.cfg.VaultRoot, link.Path)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(link.Path)))
	http.ServeFile(w, r, fullPath)
}
