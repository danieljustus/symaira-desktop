package selfhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatedShareRouteCannotExposeDatasetHandleOrRawContent(t *testing.T) {
	vaultRoot := t.TempDir()
	for rel, content := range map[string]string{
		"datasets/restricted.md":       "---\ntype: dataset\ntitle: Restricted\ndataset_id: restricted\nsource: feed\nsensitivity: restricted\nretention_rule: default\n---\n",
		"datasets/restricted/rows.csv": "id,secret\n1,do-not-share\n",
		"datasets/malformed.md":        "---\ntype: note\ntitle: Malformed\n---\n",
	} {
		path := filepath.Join(vaultRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	server, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	// Route truth: self-host exposes authenticated POST /api/v1/share and
	// unauthenticated GET /s/{token}; it has no export endpoint, and sharing
	// accepts vault paths rather than view content. Dataset handles and raw
	// dataset files are rejected before a share link can be created.
	if response := authorized(t, http.MethodGet, ts.URL+"/api/v1/export", nil, ""); response.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected status for nonexistent export route: %d: %s", response.StatusCode, readBody(response))
	} else {
		_ = response.Body.Close()
	}

	for _, rel := range []string{"datasets/restricted.md", "datasets/restricted/rows.csv", "datasets/malformed.md"} {
		response := authorized(t, http.MethodPost, ts.URL+"/api/v1/share", strings.NewReader(`{"path":"`+rel+`","expiry":1}`), "application/json")
		body := readBody(response)
		if response.StatusCode != http.StatusForbidden || !strings.Contains(body, "dataset-backed") {
			t.Fatalf("share %s returned %d: %s", rel, response.StatusCode, body)
		}
	}

	legacy, token, err := server.shares.Create("datasets/restricted.md", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_ = legacy
	response := unauthenticated(t, http.MethodGet, ts.URL+"/s/"+token, nil, "")
	body := readBody(response)
	if response.StatusCode != http.StatusNotFound || strings.Contains(body, "do-not-share") {
		t.Fatalf("legacy dataset share access returned %d: %s", response.StatusCode, body)
	}

	response = authorized(t, http.MethodGet, ts.URL+"/api/v1/shares", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list shares returned %d: %s", response.StatusCode, readBody(response))
	}
	var links []ShareLink
	if err := json.NewDecoder(response.Body).Decode(&links); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(links) != 1 || links[0].Path != "datasets/restricted.md" {
		t.Fatalf("denied dataset sharing created links: %#v", links)
	}
}
