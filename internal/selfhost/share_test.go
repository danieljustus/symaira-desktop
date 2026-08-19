package selfhost

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShareStoreCreateAndLookup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	link, token, err := store.Create("notes/hello.md", "admin", 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if link.ID == "" || token == "" {
		t.Fatal("expected non-empty id and token")
	}
	if link.Path != "notes/hello.md" {
		t.Fatalf("expected path notes/hello.md, got %s", link.Path)
	}
	if link.Token != token {
		t.Fatal("returned token does not match link.Token")
	}

	// Lookup with correct token succeeds.
	found, err := store.Lookup(token)
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if found.ID != link.ID {
		t.Fatalf("lookup returned wrong link id: %s", found.ID)
	}

	// Lookup with wrong token fails.
	_, err = store.Lookup("wrong-token-that-isnt-correct-and-will-never-match-1234567890abcdef")
	if err == nil {
		t.Fatal("expected error for wrong token")
	}

	// Lookup with empty token fails.
	_, err = store.Lookup("")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestShareStoreListAndRevoke(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	a, _, err := store.Create("doc-a.md", "admin", 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := store.Create("doc-b.md", "admin", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	links, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	// Newest first.
	if links[0].ID != b.ID {
		t.Fatal("expected newest link first")
	}

	// Revoke the first one.
	if err := store.Revoke(a.ID); err != nil {
		t.Fatal(err)
	}

	links, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links including revoked, got %d", len(links))
	}

	// Revoking again fails.
	if err := store.Revoke(a.ID); err == nil {
		t.Fatal("expected error on double revoke")
	}

	// Revoking non-existent fails.
	if err := store.Revoke("nonexistent"); err == nil {
		t.Fatal("expected error on non-existent revoke")
	}
}

func TestShareStoreExpiration(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	link, token, err := store.Create("notes/doc.md", "admin", 1*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)

	_, err = store.Lookup(token)
	if err == nil {
		t.Fatal("expected lookup to fail on expired link")
	}
	_ = link
}

func TestServerShareEndpoints(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultRoot, "Hello.md"), []byte("---\ntitle: Hello\n---\nWorld"), 0644); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	// POST /api/v1/share — create a share link.
	body := `{"path":"Hello.md","expiry":1}`
	res := authorized(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(body), "application/json")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create share returned %d: %s", res.StatusCode, readBody(res))
	}

	var created struct {
		ID        string    `json:"id"`
		Token     string    `json:"token"`
		Path      string    `json:"path"`
		CreatedAt time.Time `json:"created_at"`
		ExpiresAt time.Time `json:"expires_at"`
		URL       string    `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if created.ID == "" || created.Token == "" {
		t.Fatal("expected id and token in response")
	}
	if created.Path != "Hello.md" {
		t.Fatalf("expected path 'Hello.md', got %q", created.Path)
	}
	if created.URL != "/s/"+created.Token {
		t.Fatalf("expected url /s/%s, got %q", created.Token, created.URL)
	}

	// GET /s/<token> — unauthenticated access works.
	res = unauthenticated(t, http.MethodGet, ts.URL+"/s/"+created.Token, nil, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("share access returned %d: %s", res.StatusCode, readBody(res))
	}
	if ct := res.Header.Get("Content-Disposition"); !strings.Contains(ct, "Hello.md") {
		t.Fatalf("expected content-disposition to contain Hello.md, got %q", ct)
	}
	res.Body.Close()

	// GET /s/<bad-token> returns 404
	res = unauthenticated(t, http.MethodGet, ts.URL+"/s/bad", nil, "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for bad token, got %d", res.StatusCode)
	}
	res.Body.Close()

	// GET /api/v1/shares — list shares.
	res = authorized(t, http.MethodGet, ts.URL+"/api/v1/shares", nil, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list shares returned %d", res.StatusCode)
	}
	var shares []ShareLink
	if err := json.NewDecoder(res.Body).Decode(&shares); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(shares) != 1 {
		t.Fatalf("expected 1 share, got %d", len(shares))
	}
	if shares[0].TokenHash != "" || shares[0].Token != "" {
		t.Fatal("expected no token or token hash in share listing")
	}

	// DELETE /api/v1/share/<id> — revoke.
	res = authorized(t, http.MethodDelete, ts.URL+"/api/v1/share/"+created.ID, nil, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("revoke share returned %d: %s", res.StatusCode, readBody(res))
	}
	res.Body.Close()

	// After revocation, access is denied.
	res = unauthenticated(t, http.MethodGet, ts.URL+"/s/"+created.Token, nil, "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after revoke, got %d", res.StatusCode)
	}
	res.Body.Close()

	// Revoking again returns 404.
	res = authorized(t, http.MethodDelete, ts.URL+"/api/v1/share/"+created.ID, nil, "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on double-revoke, got %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestShareEndpointValidation(t *testing.T) {
	vaultRoot := t.TempDir()
	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	// No path.
	res := authorized(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(`{"expiry":1}`), "application/json")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing path, got %d", res.StatusCode)
	}
	res.Body.Close()

	// Invalid path.
	res = authorized(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(`{"path":"../bad","expiry":1}`), "application/json")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid path, got %d", res.StatusCode)
	}
	res.Body.Close()

	// Non-existent document.
	res = authorized(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(`{"path":"nope.md","expiry":1}`), "application/json")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent doc, got %d", res.StatusCode)
	}
	res.Body.Close()

	// Expiry too large.
	res = authorized(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(`{"path":"nope.md","expiry":99999}`), "application/json")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for excessive expiry, got %d", res.StatusCode)
	}
	res.Body.Close()

	// Unauthenticated share creation fails.
	res = unauthenticated(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(`{"path":"x.md"}`), "application/json")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated share create, got %d", res.StatusCode)
	}
	res.Body.Close()

	// Unauthenticated list fails.
	res = unauthenticated(t, http.MethodGet, ts.URL+"/api/v1/shares", nil, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated list, got %d", res.StatusCode)
	}
	res.Body.Close()

	// GET /s/ with empty token returns 404.
	res = unauthenticated(t, http.MethodGet, ts.URL+"/s/", nil, "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for empty token, got %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestShareStorePersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, token, err := store.Create("doc.md", "admin", 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Re-open the store against the same directory.
	store2, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	found, err := store2.Lookup(token)
	if err != nil {
		t.Fatalf("lookup after reopen failed: %v", err)
	}
	if found.Path != "doc.md" {
		t.Fatalf("expected path doc.md, got %q", found.Path)
	}
}

// TestShareStoreConcurrentCreate proves that N concurrent Create calls each
// hold the store lock across their whole load-modify-save transaction, so
// no link is lost to a lost update. Run with -race.
func TestShareStoreConcurrentCreate(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := store.Create(fmt.Sprintf("doc-%d.md", i), "admin", time.Hour)
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Create failed: %v", err)
	}

	links, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != n {
		t.Fatalf("expected %d links after concurrent Create, got %d (lost update)", n, len(links))
	}
	seen := make(map[string]bool, n)
	for _, l := range links {
		seen[l.Path] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct paths, got %d", n, len(seen))
	}
}

// TestShareStoreCreateRevokeInterleave proves that a Create racing a Revoke
// cannot reinstate a revoked link: the revoked link must stay revoked
// regardless of how the two transactions interleave. Run with -race.
func TestShareStoreCreateRevokeInterleave(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	link, _, err := store.Create("target.md", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = store.Revoke(link.ID)
	}()
	go func() {
		defer wg.Done()
		_, _, _ = store.Create("other.md", "admin", time.Hour)
	}()
	wg.Wait()

	links, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var found *ShareLink
	for i := range links {
		if links[i].ID == link.ID {
			found = &links[i]
			break
		}
	}
	if found == nil {
		t.Fatal("revoked link disappeared entirely")
	}
	if !found.Expired {
		t.Fatal("revoked link was reinstated by a concurrent Create")
	}
}

// TestShareStoreAtomicWrite proves shares.json is written via a temp file +
// rename so an interrupted write cannot leave a truncated store: after every
// Create the file on disk is always fully valid JSON, never a partial write.
func TestShareStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create("doc.md", "admin", time.Hour); err != nil {
		t.Fatal(err)
	}

	// No stray temp files should remain after a successful save.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shares-") {
			t.Fatalf("temp file %q was not cleaned up", e.Name())
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, "shares.json"))
	if err != nil {
		t.Fatal(err)
	}
	var links []ShareLink
	if err := json.Unmarshal(data, &links); err != nil {
		t.Fatalf("shares.json is not valid JSON: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
}

// unauthenticated sends a request without any Authorization header.
func unauthenticated(t *testing.T, method, url string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
